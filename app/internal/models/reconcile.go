package models

import (
	"context"
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
	m.store.recoverParked(m.parkOwners())
	if err := m.adoptOrphans(ctx); err != nil {
		return err
	}
	return m.reconcileJobs()
}

// parkOwners names the final directory of every change still on record.
func (m *Manager) parkOwners() map[string]string {
	owners := map[string]string{}
	for id, j := range m.jobs {
		owners[id] = installDir(j.BundleID, j.ManifestSHA256)
	}
	for _, install := range m.installs {
		if install.Ownership == OwnershipManaged {
			owners[install.ID] = installDir(install.BundleID, install.ManifestSHA256)
		}
	}
	return owners
}

func (m *Manager) reconcileJobs() error {
	for id, j := range m.jobs {
		if j.State == StateInstalled || (j.State == StatePublishing && m.jobCommitted(j)) {
			_ = m.store.discardStaging(j.StagingID)
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
