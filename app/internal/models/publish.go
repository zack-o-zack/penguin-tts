package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"time"

	"penguin.local/app/internal/catalog"
)

const hashBufferSize = 1 << 20

// fileCheck records what verification observed so later steps can detect change.
type fileCheck struct {
	size     int64
	modTime  time.Time
	identity uint64
}

func checkOf(info os.FileInfo) fileCheck {
	return fileCheck{size: info.Size(), modTime: info.ModTime(), identity: fileIdentity(info)}
}

func openError(artifact string, err error) *Error {
	switch {
	case errors.Is(err, errBaseUnavailable):
		if errors.Is(err, fs.ErrPermission) {
			return newError(CodeAccessDenied, "", err)
		}
		return newError(CodeLocationUnavailable, "", err)
	case errors.Is(err, fs.ErrNotExist):
		return newError(CodeMissingArtifact, artifact, err)
	}
	return storageError(CodeLocationUnavailable, artifact, err)
}

// fileOpener opens a relative path read-only without following links.
type fileOpener func(rel string) (*os.File, error)

func (s *store) reader() fileOpener {
	return func(rel string) (*os.File, error) { return s.open(rel, os.O_RDONLY, 0, false) }
}

func folderReader(folder string) fileOpener {
	return func(rel string) (*os.File, error) { return openBeneath(folder, rel, os.O_RDONLY, 0, false) }
}

// hashFile streams one file against its expected size and digest, reading at
// most one byte beyond the size limit.
func hashFile(ctx context.Context, open fileOpener, rel string, want sealedArtifact, progress func(int64)) (fileCheck, error) {
	file, err := open(rel)
	if err != nil {
		return fileCheck{}, openError(want.Path, err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return fileCheck{}, openError(want.Path, err)
	}
	if before.Size() != want.SizeBytes {
		return fileCheck{}, newError(CodeSizeMismatch, want.Path, nil)
	}
	hash := sha256.New()
	reader := io.LimitReader(file, want.SizeBytes+1)
	buffer := make([]byte, hashBufferSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return fileCheck{}, context.Cause(ctx)
		}
		count, readErr := reader.Read(buffer)
		hash.Write(buffer[:count])
		total += int64(count)
		if progress != nil && count > 0 {
			progress(total)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fileCheck{}, openError(want.Path, readErr)
		}
	}
	after, err := file.Stat()
	if err != nil {
		return fileCheck{}, openError(want.Path, err)
	}
	if checkOf(before) != checkOf(after) {
		return fileCheck{}, newError(CodeSourceChanged, want.Path, nil)
	}
	if total != want.SizeBytes {
		return fileCheck{}, newError(CodeSizeMismatch, want.Path, nil)
	}
	if hex.EncodeToString(hash.Sum(nil)) != want.SHA256 {
		return fileCheck{}, newError(CodeHashMismatch, want.Path, nil)
	}
	return checkOf(after), nil
}

// enterPublishing is the serialization point with Cancel: exactly one wins.
func (m *Manager) enterPublishing(j *job) error {
	return m.withLock(func() error {
		if j.stop != stopNone {
			return errStopped
		}
		j.State = StatePublishing
		return m.saveAndEmitLocked(j)
	})
}

func publishError(artifact string, err error) *Error {
	return storageError(CodePublishFailed, artifact, err)
}

// publish makes staged bytes durable, then moves them into place. A job is
// installed only after the registry commit succeeds.
func (m *Manager) publish(_ context.Context, j *job, bundle catalog.Bundle, verified map[string]fileCheck) error {
	if err := m.enterPublishing(j); err != nil {
		return err
	}
	sealed := sealedManifest{
		SchemaVersion:  sealedSchema,
		BundleID:       bundle.BundleID,
		ManifestSHA256: bundle.ManifestSHA256,
		PublishedAt:    time.Now().UTC(),
		Artifacts:      sealedArtifacts(bundle),
	}
	if err := m.store.seal(j.StagingID, &sealed, verified); err != nil {
		return err
	}
	change, err := m.store.install(j.JobID, j.StagingID, bundle.BundleID, bundle.ManifestSHA256)
	if err != nil {
		return err
	}
	if err := m.commitManaged(j, bundle, sealed.Artifacts); err != nil {
		return errors.Join(err, change.rollback())
	}
	_ = change.finish()
	return m.markInstalled(j)
}

func sealedFrom(artifact catalog.Artifact) sealedArtifact {
	return sealedArtifact{
		ArtifactID: artifact.ArtifactID,
		Path:       artifact.Path,
		SizeBytes:  artifact.Size(),
		SHA256:     artifact.SHA256,
	}
}

func sealedArtifacts(bundle catalog.Bundle) []sealedArtifact {
	artifacts := make([]sealedArtifact, 0, len(bundle.Artifacts))
	for _, artifact := range bundle.Artifacts {
		artifacts = append(artifacts, sealedFrom(artifact))
	}
	return artifacts
}

func (m *Manager) commitManaged(j *job, bundle catalog.Bundle, artifacts []sealedArtifact) error {
	return m.withLock(func() error {
		install := m.findInstallLocked(bundle.BundleID, bundle.ManifestSHA256, OwnershipManaged)
		next := m.installs
		entry := &installation{}
		if install != nil {
			*entry = *install
		} else {
			entry.ID = newInstallationID()
			next = append(slices.Clone(next), entry)
		}
		entry.Ownership = OwnershipManaged
		entry.BundleID = bundle.BundleID
		entry.ManifestSHA256 = bundle.ManifestSHA256
		entry.Status = StatusVerified
		entry.SizeBytes = bundle.Space.RequiredBytes
		entry.LastVerifiedAt = time.Now().UTC()
		entry.Artifacts = artifacts
		entry.ErrorCode, entry.ErrorArtifact = "", ""
		if install != nil {
			next = replaceInstall(next, entry)
		}
		return m.commitInstallsLocked(next)
	})
}

func replaceInstall(installs []*installation, entry *installation) []*installation {
	next := slices.Clone(installs)
	for index, existing := range next {
		if existing.ID == entry.ID {
			next[index] = entry
		}
	}
	return next
}

func (m *Manager) commitInstallsLocked(next []*installation) error {
	if err := m.store.saveRegistry(next); err != nil {
		return publishError("", err)
	}
	m.installs = next
	m.emitInstallationsLocked()
	return nil
}

func (m *Manager) markInstalled(j *job) error {
	return m.withLock(func() error {
		j.State = StateInstalled
		// The registry already committed the install, so a journal write
		// failure here is corrected by reconciliation on the next launch.
		_ = m.persistLocked(j)
		return nil
	})
}

// adoptOrphans finishes a publication interrupted before its registry commit.
func (m *Manager) adoptOrphans(ctx context.Context) error {
	keys, err := m.store.installDirs()
	if err != nil {
		return storageError(CodeStorageUnavailable, "", err)
	}
	changed := false
	for _, key := range keys {
		existing := m.findInstallLocked(key.bundleID, key.manifest, OwnershipManaged)
		if existing != nil && (existing.Status == StatusVerified || !m.repairWasMoved(key)) {
			continue
		}
		adopted, err := m.adopt(ctx, key.bundleID, key.manifest)
		if err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			m.notice = "An incomplete model folder was left in storage and is not in use."
			continue
		}
		if existing != nil {
			adopted.ID = existing.ID
			m.installs = replaceInstall(m.installs, adopted)
		} else {
			m.installs = append(m.installs, adopted)
		}
		changed = true
	}
	if changed {
		if err := m.store.saveRegistry(m.installs); err != nil {
			return storageError(CodeStorageUnavailable, "", err)
		}
	}
	return nil
}

// repairWasMoved reports a publishing job whose staging already reached the
// final directory, so that directory holds the repair rather than the damage.
func (m *Manager) repairWasMoved(key installKey) bool {
	for _, j := range m.jobs {
		if j.State == StatePublishing && j.Kind != KindReference && j.BundleID == key.bundleID &&
			j.ManifestSHA256 == key.manifest && m.store.stagingGone(j.StagingID) {
			return true
		}
	}
	return false
}

func (m *Manager) adopt(ctx context.Context, bundleID, manifest string) (*installation, error) {
	dir := installDir(bundleID, manifest)
	sealed, err := m.store.readSealed(dir)
	if err != nil {
		return nil, err
	}
	if sealed.BundleID != bundleID || sealed.ManifestSHA256 != manifest {
		return nil, errors.New("sealed manifest does not match its directory")
	}
	var total int64
	for _, artifact := range sealed.Artifacts {
		rel := path.Join(dir, filesDir, artifact.Path)
		if _, err := hashFile(ctx, m.store.reader(), rel, artifact, nil); err != nil {
			return nil, fmt.Errorf("verify sealed artifact: %w", err)
		}
		total += artifact.SizeBytes
	}
	return &installation{
		ID:             newInstallationID(),
		Ownership:      OwnershipManaged,
		BundleID:       bundleID,
		ManifestSHA256: manifest,
		Status:         StatusVerified,
		SizeBytes:      total,
		LastVerifiedAt: time.Now().UTC(),
		Artifacts:      sealed.Artifacts,
	}, nil
}
