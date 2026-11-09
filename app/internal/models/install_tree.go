package models

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"

	"penguin.local/app/internal/catalog"
)

// treeChange is one uncommitted move of an install directory. The caller
// commits the registry, then ends the change with finish or rollback.
type treeChange struct {
	store   *store
	staging string
	final   string
	parked  string
}

// install durably moves sealed staging into its final directory, parking any
// damaged copy already there under changeID.
func (s *store) install(changeID, stagingID, bundleID, manifest string) (*treeChange, error) {
	change := &treeChange{
		store:   s,
		staging: stagingDir(stagingID),
		final:   installDir(bundleID, manifest),
		parked:  path.Join(replacedRoot, changeID),
	}
	if err := s.root.MkdirAll(path.Dir(change.final), 0o700); err != nil {
		return nil, publishError("", err)
	}
	info, err := s.root.Lstat(change.staging)
	if err != nil {
		return nil, publishError("", err)
	}
	if !info.IsDir() {
		return nil, newError(CodeUnsafePath, "", nil)
	}
	if _, err := s.root.Lstat(change.final); err == nil {
		// Only a damaged copy of the same manifest can already be here; a
		// verified one would have satisfied the request without a job.
		if err := s.hooks.rename(s.root, change.final, change.parked); err != nil {
			return nil, publishError("", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, publishError("", err)
	}
	if err := s.hooks.rename(s.root, change.staging, change.final); err != nil {
		return nil, errors.Join(publishError("", err), change.restoreParked())
	}
	for _, dir := range []string{path.Dir(change.final), installsRoot, stagingRoot, replacedRoot} {
		if err := s.syncDir(dir); err != nil {
			return nil, errors.Join(publishError("", err), change.rollback())
		}
	}
	s.hooks.boundary("renamed")
	return change, nil
}

// uninstall durably parks an install directory under changeID. A directory
// that is already missing is not an error, so damaged installs stay removable.
func (s *store) uninstall(changeID, bundleID, manifest string) (*treeChange, error) {
	change := &treeChange{
		store:  s,
		final:  installDir(bundleID, manifest),
		parked: path.Join(replacedRoot, changeID),
	}
	if err := s.hooks.rename(s.root, change.final, change.parked); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return change, nil
		}
		return nil, storageError(CodeWriteFailed, "", err)
	}
	for _, dir := range []string{path.Dir(change.final), replacedRoot} {
		if err := s.syncDir(dir); err != nil {
			return nil, errors.Join(storageError(CodeWriteFailed, "", err), change.rollback())
		}
	}
	return change, nil
}

// finish deletes what the committed change replaced.
func (c *treeChange) finish() error {
	c.store.hooks.boundary("registered")
	if err := c.store.root.RemoveAll(c.parked); err != nil {
		return storageError(CodeWriteFailed, "", err)
	}
	return nil
}

// rollback undoes the move after a failed registry commit.
func (c *treeChange) rollback() error {
	if c.staging != "" {
		if err := c.store.hooks.rename(c.store.root, c.final, c.staging); err != nil {
			return publishError("", err)
		}
	}
	return c.restoreParked()
}

func (c *treeChange) restoreParked() error {
	if _, err := c.store.root.Lstat(c.parked); err != nil {
		return nil
	}
	if err := c.store.hooks.rename(c.store.root, c.parked, c.final); err != nil {
		return publishError("", err)
	}
	return nil
}

// recoverParked ends changes a crash interrupted. owners maps each change ID
// still on record to its final directory: if that directory is empty the change
// never committed, so its parked files go back. Everything else is garbage.
func (s *store) recoverParked(owners map[string]string) {
	entries, err := fs.ReadDir(s.root.FS(), replacedRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		parked := path.Join(replacedRoot, entry.Name())
		if final, ok := owners[entry.Name()]; ok {
			if _, err := s.root.Lstat(final); errors.Is(err, fs.ErrNotExist) {
				_ = s.hooks.rename(s.root, parked, final)
				continue
			}
		}
		_ = s.root.RemoveAll(parked)
	}
}

type installKey struct {
	bundleID string
	manifest string
}

// installDirs lists every well-named install directory on disk.
func (s *store) installDirs() ([]installKey, error) {
	bundles, err := fs.ReadDir(s.root.FS(), installsRoot)
	if err != nil {
		return nil, err
	}
	var keys []installKey
	for _, bundle := range bundles {
		manifests, err := fs.ReadDir(s.root.FS(), path.Join(installsRoot, bundle.Name()))
		if err != nil {
			continue
		}
		for _, manifest := range manifests {
			if catalog.ValidID(bundle.Name()) && catalog.ValidDigest(manifest.Name()) {
				keys = append(keys, installKey{bundleID: bundle.Name(), manifest: manifest.Name()})
			}
		}
	}
	return keys, nil
}

// seal makes verified staging durable and writes its manifest, failing if any
// file changed since verification.
func (s *store) seal(stagingID string, sealed *sealedManifest, verified map[string]fileCheck) error {
	staging := stagingDir(stagingID)
	directories := map[string]bool{staging: true, path.Join(staging, filesDir): true}
	for _, artifact := range sealed.Artifacts {
		rel := stagingFile(stagingID, artifact.Path)
		if err := s.syncVerified(rel, artifact.Path, verified[artifact.Path]); err != nil {
			return err
		}
		for dir := path.Dir(rel); strings.HasPrefix(dir, staging); dir = path.Dir(dir) {
			directories[dir] = true
		}
	}
	s.hooks.boundary("files-synced")

	if err := s.writeSealed(staging, sealed); err != nil {
		return err
	}
	for dir := range directories {
		if err := s.syncDir(dir); err != nil {
			return publishError("", err)
		}
	}
	s.hooks.boundary("sealed")
	return nil
}

func (s *store) syncVerified(rel, artifact string, verified fileCheck) error {
	file, err := s.open(rel, os.O_RDWR, 0, false)
	if err != nil {
		return publishError(artifact, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return publishError(artifact, err)
	}
	if checkOf(info) != verified {
		_ = file.Close()
		return newError(CodeSourceChanged, artifact, nil)
	}
	if err := s.syncAndClose(file); err != nil {
		return publishError(artifact, err)
	}
	return nil
}

func (s *store) writeSealed(staging string, sealed *sealedManifest) error {
	data, err := json.Marshal(sealed)
	if err != nil {
		return newError(CodeInternal, "", err)
	}
	file, err := s.open(path.Join(staging, sealedName), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600, false)
	if err != nil {
		return publishError("", err)
	}
	if err := s.writeAll(file, data); err != nil {
		_ = file.Close()
		return publishError("", err)
	}
	if err := s.syncAndClose(file); err != nil {
		return publishError("", err)
	}
	return nil
}

// discardStaging deletes a job's staging; jobs without staging are a no-op.
func (s *store) discardStaging(stagingID string) error {
	if stagingID == "" {
		return nil
	}
	return s.root.RemoveAll(stagingDir(stagingID))
}

func (s *store) stagingGone(stagingID string) bool {
	_, err := s.root.Lstat(stagingDir(stagingID))
	return errors.Is(err, fs.ErrNotExist)
}

// stagedSizes reports each regular staged file by artifact path.
func (s *store) stagedSizes(stagingID string, artifacts []journalArtifact) map[string]int64 {
	sizes := map[string]int64{}
	if stagingID == "" {
		return sizes
	}
	for _, artifact := range artifacts {
		if info, err := s.root.Lstat(stagingFile(stagingID, artifact.Path)); err == nil && info.Mode().IsRegular() {
			sizes[artifact.Path] = info.Size()
		}
	}
	return sizes
}

func (s *store) removeStagedFile(stagingID, artifact string) error {
	err := s.root.Remove(stagingFile(stagingID, artifact))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
