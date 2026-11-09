package models

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"penguin.local/app/internal/catalog"
)

// ImportMode selects who owns an imported folder's bytes.
type ImportMode string

const (
	ImportReference ImportMode = "reference"
	ImportCopy      ImportMode = "copy"
)

func (m *Manager) validateFolder(folder string) (string, error) {
	if !filepath.IsAbs(folder) {
		return "", newError(CodeLocationUnavailable, "", errors.New("folder must be absolute"))
	}
	clean := filepath.Clean(folder)
	relative, err := filepath.Rel(m.store.base, clean)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", newError(CodeUnsafePath, "", errors.New("folder is inside managed storage"))
	}
	return clean, nil
}

// ImportFolder verifies a user-selected folder against a known bundle, either
// as a read-only reference or as a managed copy.
func (m *Manager) ImportFolder(bundleID, folder string, mode ImportMode) (result EnqueueResult, err error) {
	clean, err := m.validateFolder(folder)
	if err != nil {
		return EnqueueResult{}, err
	}
	err = m.withLock(func() error {
		bundle, ok := m.catalog.Find(bundleID)
		if !ok {
			return newError(CodeUnknownBundle, "", nil)
		}
		kind, ownership, joinKinds := KindReference, OwnershipExternal, []JobKind{KindReference}
		switch mode {
		case ImportReference:
		case ImportCopy:
			kind, ownership, joinKinds = KindCopy, OwnershipManaged, []JobKind{KindDownload, KindCopy}
		default:
			return newError(CodeInvalidState, "", errors.New("unknown import mode"))
		}
		installationID := ""
		if install := m.findInstallLocked(bundle.BundleID, bundle.ManifestSHA256, ownership); install != nil {
			if install.Status == StatusVerified {
				result = EnqueueResult{InstallationID: install.ID}
				return nil
			}
			if ownership == OwnershipExternal {
				installationID = install.ID
			}
		}
		var requestErr error
		result, requestErr = m.requestFolderJobLocked(kind, bundle, clean, installationID, joinKinds)
		return requestErr
	})
	return result, err
}

// LocateAgain fully verifies a new folder before replacing a reference's
// location. A failed attempt leaves the existing record untouched.
func (m *Manager) LocateAgain(installationID, folder string) (result EnqueueResult, err error) {
	clean, err := m.validateFolder(folder)
	if err != nil {
		return EnqueueResult{}, err
	}
	err = m.withLock(func() error {
		install := m.installByIDLocked(installationID)
		if install == nil {
			return newError(CodeUnknownInstallation, "", nil)
		}
		if install.Ownership != OwnershipExternal {
			return newError(CodeInvalidState, "", nil)
		}
		bundle, ok := m.catalog.Find(install.BundleID)
		if !ok || bundle.ManifestSHA256 != install.ManifestSHA256 {
			return newError(CodeInvalidManifest, "", nil)
		}
		var requestErr error
		result, requestErr = m.requestFolderJobLocked(KindReference, bundle, clean, install.ID, []JobKind{KindReference})
		return requestErr
	})
	return result, err
}

// requestFolderJobLocked joins matching work. A stopped job of the same kind
// is pointed at the newly chosen folder, so a failed attempt never pins the
// user to the folder that failed.
func (m *Manager) requestFolderJobLocked(kind JobKind, bundle catalog.Bundle, folder, installationID string, joinKinds []JobKind) (EnqueueResult, error) {
	existing := m.findJoinableLocked(bundle, joinKinds...)
	if existing == nil {
		return m.enqueueLocked(kind, bundle, folder, installationID)
	}
	if existing.Kind != kind || !existing.settled() {
		return EnqueueResult{JobID: existing.JobID}, nil
	}
	existing.SourcePath, existing.InstallationID = folder, installationID
	existing.State = StateQueued
	existing.ErrorCode, existing.ErrorArtifact = "", ""
	if err := m.persistLocked(existing); err != nil {
		return EnqueueResult{}, err
	}
	m.emitJobLocked(existing)
	m.signal()
	return EnqueueResult{JobID: existing.JobID}, nil
}

func (m *Manager) installByIDLocked(id string) *installation {
	for _, install := range m.installs {
		if install.ID == id {
			return install
		}
	}
	return nil
}

func (m *Manager) runReference(ctx context.Context, j *job, bundle catalog.Bundle) error {
	if err := m.setState(j, StateVerifying); err != nil {
		return err
	}
	observations, err := m.verifyExternal(ctx, j, j.SourcePath, sealedArtifacts(bundle))
	if err != nil {
		return err
	}
	if err := m.enterPublishing(j); err != nil {
		return err
	}
	if err := m.commitReference(j, bundle, observations); err != nil {
		return err
	}
	return m.markInstalled(j)
}

func (m *Manager) verifyExternal(ctx context.Context, j *job, folder string, artifacts []sealedArtifact) ([]observation, error) {
	observations := make([]observation, 0, len(artifacts))
	for _, artifact := range artifacts {
		if j != nil {
			m.setCurrent(j, artifact.Path)
		}
		m.hashing(artifact.Path)
		var progress func(int64)
		if j != nil {
			progress = func(done int64) {
				m.reportProgress(j, func() { j.verified[artifact.Path] = done })
			}
		}
		check, err := hashFile(ctx, folderReader(folder), artifact.Path, artifact, progress)
		if err != nil {
			return nil, err
		}
		observations = append(observations, observation{Path: artifact.Path, Size: check.size, ModTime: check.modTime, Identity: check.identity})
	}
	return observations, nil
}

func (m *Manager) commitReference(j *job, bundle catalog.Bundle, observations []observation) error {
	return m.withLock(func() error {
		entry := &installation{ID: j.InstallationID}
		isNew := entry.ID == "" || m.installByIDLocked(entry.ID) == nil
		if isNew {
			entry.ID = newInstallationID()
		}
		entry.Ownership = OwnershipExternal
		entry.BundleID = bundle.BundleID
		entry.ManifestSHA256 = bundle.ManifestSHA256
		entry.Status = StatusVerified
		entry.Location = j.SourcePath
		entry.SizeBytes = bundle.Space.RequiredBytes
		entry.LastVerifiedAt = time.Now().UTC()
		entry.Artifacts = sealedArtifacts(bundle)
		entry.Observations = observations
		next := append(slices.Clone(m.installs), entry)
		if !isNew {
			next = replaceInstall(m.installs, entry)
		}
		return m.commitInstallsLocked(next)
	})
}

func (m *Manager) runCopy(ctx context.Context, j *job, bundle catalog.Bundle) error {
	if err := m.preflight(j, bundle, bundle.Space.CopyHeadroomBytes); err != nil {
		return err
	}
	if err := m.setState(j, StateCopying); err != nil {
		return err
	}
	for _, artifact := range bundle.Artifacts {
		if err := m.copyArtifact(ctx, j, artifact); err != nil {
			return err
		}
	}
	if err := m.setState(j, StateVerifying); err != nil {
		return err
	}
	verified := map[string]fileCheck{}
	for _, artifact := range bundle.Artifacts {
		check, err := m.verifyStaged(ctx, j, artifact)
		if err != nil {
			if isContentMismatch(err) {
				_ = m.clearStaged(j, artifact)
			}
			return err
		}
		verified[artifact.Path] = check
	}
	return m.publish(ctx, j, bundle, verified)
}

// copyArtifact streams only the declared bytes; the source is opened read-only.
func (m *Manager) copyArtifact(ctx context.Context, j *job, artifact catalog.Artifact) (err error) {
	m.setCurrent(j, artifact.Path)
	source, err := openBeneath(j.SourcePath, artifact.Path, os.O_RDONLY, 0, false)
	if err != nil {
		return openError(artifact.Path, err)
	}
	defer source.Close()
	destination, err := m.store.open(stagingFile(j.StagingID, artifact.Path), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600, true)
	if err != nil {
		return storageError(CodeWriteFailed, artifact.Path, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = destination.Close()
		}
	}()
	reader := io.LimitReader(source, artifact.Size()+1)
	buffer := make([]byte, copyBufferSize)
	var total int64
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			total += int64(count)
			if total > artifact.Size() {
				return newError(CodeSizeMismatch, artifact.Path, nil)
			}
			if err := m.store.writeAll(destination, buffer[:count]); err != nil {
				return storageError(CodeWriteFailed, artifact.Path, err)
			}
			done := total
			m.reportProgress(j, func() { j.fileBytes[artifact.Path] = done })
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return openError(artifact.Path, readErr)
		}
	}
	if total != artifact.Size() {
		return newError(CodeSizeMismatch, artifact.Path, nil)
	}
	closed = true
	if err := m.store.closeFile(destination); err != nil {
		return storageError(CodeWriteFailed, artifact.Path, err)
	}
	return nil
}

// RemoveInstallation forgets an external reference without touching its files,
// or deletes a managed install from owned storage.
func (m *Manager) RemoveInstallation(installationID string) error {
	return m.withLock(func() error {
		install := m.installByIDLocked(installationID)
		if install == nil {
			return newError(CodeUnknownInstallation, "", nil)
		}
		for _, j := range m.jobs {
			if j.running && j.State != StateInstalled && j.BundleID == install.BundleID && j.ManifestSHA256 == install.ManifestSHA256 && j.ownership() == install.Ownership {
				return newError(CodeInvalidState, "", nil)
			}
		}
		next := slices.DeleteFunc(slices.Clone(m.installs), func(entry *installation) bool { return entry.ID == installationID })
		if install.Ownership == OwnershipExternal {
			return m.commitInstallsLocked(next)
		}
		change, err := m.store.uninstall(install.ID, install.BundleID, install.ManifestSHA256)
		if err != nil {
			return err
		}
		if err := m.commitInstallsLocked(next); err != nil {
			return errors.Join(err, change.rollback())
		}
		return change.finish()
	})
}

func (j *job) ownership() Ownership {
	if j.Kind == KindReference {
		return OwnershipExternal
	}
	return OwnershipManaged
}

// Revalidate re-hashes every file of an installation and records the result.
func (m *Manager) Revalidate(ctx context.Context, installationID string) error {
	var snapshot installation
	if err := m.withLock(func() error {
		install := m.installByIDLocked(installationID)
		if install == nil {
			return newError(CodeUnknownInstallation, "", nil)
		}
		snapshot = *install
		return nil
	}); err != nil {
		return err
	}
	open, prefix := m.installFiles(&snapshot)
	observations := make([]observation, 0, len(snapshot.Artifacts))
	var failure *Error
	for _, artifact := range snapshot.Artifacts {
		m.hashing(artifact.Path)
		check, err := hashFile(ctx, open, path.Join(prefix, artifact.Path), artifact, nil)
		if err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			failure = asError(err)
			break
		}
		observations = append(observations, observation{Path: artifact.Path, Size: check.size, ModTime: check.modTime, Identity: check.identity})
	}
	return m.withLock(func() error {
		install := m.installByIDLocked(installationID)
		if install == nil {
			return newError(CodeUnknownInstallation, "", nil)
		}
		// A relocation or newer check won while this one was hashing.
		if install.Location != snapshot.Location || !install.LastVerifiedAt.Equal(snapshot.LastVerifiedAt) {
			return nil
		}
		updated := *install
		updated.ErrorCode, updated.ErrorArtifact = "", ""
		if failure == nil {
			updated.Status = StatusVerified
			updated.LastVerifiedAt = time.Now().UTC()
			if updated.Ownership == OwnershipExternal {
				updated.Observations = observations
			}
		} else {
			updated.Status = statusForFailure(failure.Code)
			updated.ErrorCode, updated.ErrorArtifact = failure.Code, failure.Artifact
		}
		return m.commitInstallsLocked(replaceInstall(m.installs, &updated))
	})
}

func (m *Manager) installFiles(install *installation) (fileOpener, string) {
	if install.Ownership == OwnershipManaged {
		return m.store.reader(), path.Join(installDir(install.BundleID, install.ManifestSHA256), filesDir)
	}
	return folderReader(install.Location), ""
}

func statusForFailure(code Code) InstallStatus {
	switch code {
	case CodeLocationUnavailable, CodeAccessDenied, CodeMissingArtifact:
		return StatusUnavailable
	}
	return StatusChanged
}

// refreshInstallStatuses uses metadata only, so startup reads no model bytes
// and writes nothing to external folders. Observations invalidate; they never
// prove a file is correct.
func (m *Manager) refreshInstallStatuses() error {
	changed := false
	next := make([]*installation, 0, len(m.installs))
	for _, install := range m.installs {
		updated := *install
		status, code, artifact := m.inspect(install)
		if status == StatusVerified {
			// A failed check is only cleared by a full Revalidate.
			if install.Status != StatusVerified {
				status, code, artifact = install.Status, install.ErrorCode, install.ErrorArtifact
			}
		}
		updated.Status, updated.ErrorCode, updated.ErrorArtifact = status, code, artifact
		changed = changed || updated.Status != install.Status || updated.ErrorCode != install.ErrorCode || updated.ErrorArtifact != install.ErrorArtifact
		next = append(next, &updated)
	}
	m.installs = next
	if !changed {
		return nil
	}
	if err := m.store.saveRegistry(next); err != nil {
		return storageError(CodeStorageUnavailable, "", err)
	}
	return nil
}

func (m *Manager) inspect(install *installation) (InstallStatus, Code, string) {
	open, prefix := m.installFiles(install)
	for index, artifact := range install.Artifacts {
		file, err := open(path.Join(prefix, artifact.Path))
		if err != nil {
			failure := openError(artifact.Path, err)
			return statusForFailure(failure.Code), failure.Code, failure.Artifact
		}
		info, err := file.Stat()
		_ = file.Close()
		if err != nil {
			failure := openError(artifact.Path, err)
			return statusForFailure(failure.Code), failure.Code, failure.Artifact
		}
		if info.Size() != artifact.SizeBytes {
			return StatusChanged, CodeSizeMismatch, artifact.Path
		}
		if install.Ownership == OwnershipExternal && index < len(install.Observations) {
			observed := install.Observations[index]
			if observed.Size != info.Size() || !observed.ModTime.Equal(info.ModTime()) || observed.Identity != fileIdentity(info) {
				return StatusChanged, CodeSourceChanged, artifact.Path
			}
		}
	}
	return StatusVerified, "", ""
}

// ListInstallations returns installed bundles and storage usage.
func (m *Manager) ListInstallations() InstallationList {
	var list InstallationList
	_ = m.withLock(func() error {
		list.Sequence = m.sequence
		list.ModelsPath = modelsPath(m.store.base)
		list.Notice = m.notice
		list.Installations = make([]InstallationSnapshot, 0, len(m.installs))
		for _, install := range m.installs {
			list.Installations = append(list.Installations, m.installSnapshotLocked(install))
			if install.Ownership == OwnershipManaged {
				list.ManagedBytes += install.SizeBytes
			} else {
				list.ExternalBytes += install.SizeBytes
			}
		}
		for _, j := range m.jobs {
			if j.running {
				transferred, _ := j.snapshotBytes()
				list.RetainedBytes += transferred
			} else {
				list.RetainedBytes += j.retained
			}
		}
		return nil
	})
	slices.SortFunc(list.Installations, func(a, b InstallationSnapshot) int {
		return strings.Compare(a.DisplayName+a.ID, b.DisplayName+b.ID)
	})
	return list
}

func (m *Manager) installSnapshotLocked(install *installation) InstallationSnapshot {
	snapshot := InstallationSnapshot{
		ID:             install.ID,
		BundleID:       install.BundleID,
		DisplayName:    install.BundleID,
		ManifestSHA256: install.ManifestSHA256,
		Ownership:      string(install.Ownership),
		Status:         string(install.Status),
		Location:       install.Location,
		SizeBytes:      install.SizeBytes,
		LastVerifiedAt: install.LastVerifiedAt,
		ErrorCode:      string(install.ErrorCode),
		ErrorArtifact:  install.ErrorArtifact,
		EngineStatus:   catalog.EngineUnknown,
	}
	if install.Ownership == OwnershipManaged {
		snapshot.Location = filepath.Join(m.store.base, filepath.FromSlash(installDir(install.BundleID, install.ManifestSHA256)))
	}
	if install.ErrorCode != "" {
		snapshot.ErrorMessage = newError(install.ErrorCode, install.ErrorArtifact, nil).Error()
	}
	if bundle, ok := m.catalog.Find(install.BundleID); ok {
		snapshot.DisplayName = bundle.DisplayName
		snapshot.Current = bundle.ManifestSHA256 == install.ManifestSHA256
		if snapshot.Current {
			snapshot.EngineStatus = bundle.Engine.Status
		}
	}
	return snapshot
}
