package models

import (
	"context"
	"maps"
	"path"

	"penguin.local/app/internal/catalog"
)

func (m *Manager) preflight(j *job, bundle catalog.Bundle, headroom int64) error {
	var retained int64
	_ = m.withLock(func() error {
		sizes := m.store.stagedSizes(j.StagingID, j.Artifacts)
		retained = sumSizes(sizes)
		maps.Copy(j.fileBytes, sizes)
		return nil
	})
	free, err := m.free(path.Join(m.store.base, modelsDir))
	if err != nil {
		return nil // Unknown free space still fails safely on the first failed write.
	}
	if int64(free) < max(bundle.Space.RequiredBytes-retained, 0)+headroom {
		return newError(CodeNoSpace, "", nil)
	}
	return nil
}

func (m *Manager) runDownload(ctx context.Context, j *job, bundle catalog.Bundle) error {
	if err := m.preflight(j, bundle, bundle.Space.StagingHeadroomBytes); err != nil {
		return err
	}
	budgets := map[string]*int{}
	for _, artifact := range bundle.Artifacts {
		attempts := maxTransferAttempts
		budgets[artifact.Path] = &attempts
	}
	verified := map[string]fileCheck{}
	for {
		if err := m.setState(j, StateDownloading); err != nil {
			return err
		}
		for _, artifact := range bundle.Artifacts {
			if _, done := verified[artifact.Path]; done {
				continue
			}
			if err := m.transferArtifact(ctx, j, artifact, budgets[artifact.Path]); err != nil {
				return err
			}
		}
		if err := m.setState(j, StateVerifying); err != nil {
			return err
		}
		retry := false
		for _, artifact := range bundle.Artifacts {
			if _, done := verified[artifact.Path]; done {
				continue
			}
			check, err := m.verifyStaged(ctx, j, artifact)
			switch {
			case err == nil:
				verified[artifact.Path] = check
			case isContentMismatch(err):
				if clearErr := m.clearStaged(j, artifact); clearErr != nil {
					return clearErr
				}
				if *budgets[artifact.Path] <= 0 {
					return err
				}
				retry = true
			default:
				return err
			}
		}
		if !retry {
			return m.publish(ctx, j, bundle, verified)
		}
	}
}

func isContentMismatch(err error) bool {
	failure := asError(err)
	return failure.Code == CodeHashMismatch || failure.Code == CodeSizeMismatch
}

func (m *Manager) transferArtifact(ctx context.Context, j *job, artifact catalog.Artifact, budget *int) error {
	m.setCurrent(j, artifact.Path)
	return m.tr.transfer(ctx, transferSpec{
		artifact: artifact,
		rel:      stagingFile(j.StagingID, artifact.Path),
		budget:   budget,
		etag: func() (etag string) {
			_ = m.withLock(func() error {
				etag = j.artifact(artifact.Path).ETag
				return nil
			})
			return etag
		},
		saveETag: func(etag string) error {
			return m.withLock(func() error {
				j.artifact(artifact.Path).ETag = etag
				return m.persistLocked(j)
			})
		},
		progress: func(size int64) {
			m.reportProgress(j, func() { j.fileBytes[artifact.Path] = size })
		},
	})
}

// clearStaged removes bytes that failed verification so retries start clean.
func (m *Manager) clearStaged(j *job, artifact catalog.Artifact) error {
	return m.withLock(func() error {
		if err := m.store.removeStagedFile(j.StagingID, artifact.Path); err != nil {
			return storageError(CodeWriteFailed, artifact.Path, err)
		}
		j.artifact(artifact.Path).ETag = ""
		delete(j.fileBytes, artifact.Path)
		delete(j.verified, artifact.Path)
		return m.persistLocked(j)
	})
}

func (m *Manager) verifyStaged(ctx context.Context, j *job, artifact catalog.Artifact) (fileCheck, error) {
	m.setCurrent(j, artifact.Path)
	m.hashing(artifact.Path)
	return hashFile(ctx, m.store.reader(), stagingFile(j.StagingID, artifact.Path), sealedFrom(artifact), func(done int64) {
		m.reportProgress(j, func() { j.verified[artifact.Path] = done })
	})
}
