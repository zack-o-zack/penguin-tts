package models

import (
	"context"
	"errors"
	"io/fs"
	"path"
)

func (m *Manager) reconcile(ctx context.Context) error {
	m.store.removeTemporaries("state")
	m.store.removeTemporaries(jobsDir)

	installs, recovered, err := m.store.loadRegistry()
	if err != nil {
		return storageError(CodeStorageUnavailable, "", err)
	}
	m.installs = installs
	if recovered {
		m.notice = "Saved model records were damaged and rebuilt. Folders added from outside Penguin may need to be added again."
	}
	entries, corrupt, err := m.store.loadJournals()
	if err != nil {
		return storageError(CodeStorageUnavailable, "", err)
	}
	for _, entry := range entries {
		m.jobs[entry.JobID] = &job{journal: *entry}
	}
	for _, id := range corrupt {
		m.jobs[id] = &job{journal: journal{
			SchemaVersion: journalSchema, JobID: id, Kind: KindDownload, State: StateFailed,
			StagingID: id, ErrorCode: CodeInvalidJournal,
		}}
	}
	m.restoreReplaced()
	m.clearReplaced()
	if err := m.adoptOrphans(ctx); err != nil {
		return err
	}
	return m.reconcileJobs()
}

// restoreReplaced undoes a publication that died between moving the old
// directory aside and moving the new one in.
func (m *Manager) restoreReplaced() {
	for _, j := range m.jobs {
		aside := path.Join(replacedRoot, j.JobID)
		if _, err := m.store.root.Lstat(aside); err != nil {
			continue
		}
		final := installDir(j.BundleID, j.ManifestSHA256)
		if _, err := m.store.root.Lstat(final); errors.Is(err, fs.ErrNotExist) {
			_ = m.hooks.rename(m.store.root, aside, final)
		}
	}
}

func (m *Manager) clearReplaced() {
	entries, err := fs.ReadDir(m.store.root.FS(), replacedRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		_ = m.store.root.RemoveAll(path.Join(replacedRoot, entry.Name()))
	}
}

func (m *Manager) reconcileJobs() error {
	for id, j := range m.jobs {
		if j.State == StateInstalled || (j.State == StatePublishing && m.jobCommitted(j)) {
			if j.StagingID != "" {
				if _, err := m.store.root.Lstat(stagingDir(j.StagingID)); err == nil {
					_ = m.store.root.RemoveAll(stagingDir(j.StagingID))
				}
			}
			delete(m.jobs, id)
			if err := m.store.removeJournal(id); err != nil {
				return storageError(CodeStorageUnavailable, "", err)
			}
			continue
		}
		if j.ErrorCode != CodeInvalidJournal {
			if code := m.journalProblem(j); code != "" {
				j.State, j.ErrorCode, j.ErrorArtifact = StateFailed, code, ""
				if err := m.persistLocked(j); err != nil {
					return err
				}
			}
		}
		switch j.State {
		case StateQueued, StateDownloading, StateCopying, StateVerifying, StatePublishing:
			j.State = StateInterrupted
			if err := m.persistLocked(j); err != nil {
				return err
			}
		}
		j.retained = m.stagedBytesLocked(j)
	}
	return m.refreshInstallStatuses()
}

// journalProblem binds a recovered journal to the catalog before any resume.
func (m *Manager) journalProblem(j *job) Code {
	bundle, err := m.bundleFor(&j.journal)
	if err != nil {
		return CodeInvalidManifest
	}
	if len(j.Artifacts) != len(bundle.Artifacts) {
		return CodeInvalidJournal
	}
	for index, artifact := range bundle.Artifacts {
		if j.Artifacts[index].ArtifactID != artifact.ArtifactID || j.Artifacts[index].Path != artifact.Path {
			return CodeInvalidJournal
		}
	}
	return ""
}

func (m *Manager) jobCommitted(j *job) bool {
	for _, install := range m.installs {
		if install.BundleID != j.BundleID || install.ManifestSHA256 != j.ManifestSHA256 || install.Status != StatusVerified {
			continue
		}
		if j.Kind == KindReference {
			if install.Ownership == OwnershipExternal && install.Location == j.SourcePath {
				return true
			}
			continue
		}
		if install.Ownership == OwnershipManaged {
			return true
		}
	}
	return false
}
