package models

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"penguin.local/app/internal/catalog"
)

const (
	journalSchema  = 1
	registrySchema = 1
	sealedSchema   = 1

	jobsDir      = "state/jobs"
	registryFile = "state/registry.json"
	lockFile     = "state/writer.lock"
	modelsDir    = "models"
	stagingRoot  = "models/.downloads"
	replacedRoot = "models/.replaced"
	installsRoot = "models/installs"
	sealedName   = "install.json"
	filesDir     = "files"

	maxStateBytes = 16 << 20
)

var (
	errUnsafeFile      = errors.New("not a regular file reachable without links")
	errBaseUnavailable = errors.New("location unavailable")
	errLocked          = errors.New("storage is locked by another process")

	jobIDPattern = regexp.MustCompile(`^job-[0-9a-f]{16}$`)
)

// fsHooks are the durability primitives, replaceable so tests can inject
// faults and crashes at exact boundaries.
type fsHooks struct {
	write    func(*os.File, []byte) (int, error)
	sync     func(*os.File) error
	close    func(*os.File) error
	rename   func(*os.Root, string, string) error
	boundary func(string)
}

func defaultHooks() fsHooks {
	return fsHooks{
		write:    func(file *os.File, data []byte) (int, error) { return file.Write(data) },
		sync:     func(file *os.File) error { return file.Sync() },
		close:    func(file *os.File) error { return file.Close() },
		rename:   func(root *os.Root, from, to string) error { return root.Rename(from, to) },
		boundary: func(string) {},
	}
}

// store owns every path below the application data directory.
type store struct {
	base      string
	directory *os.File
	root      *os.Root
	lock      io.Closer
	hooks     fsHooks
}

func openStore(base string, hooks fsHooks) (*store, error) {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, storageError(CodeStorageUnavailable, "", err)
	}
	directory, err := os.Open(base)
	if err != nil {
		return nil, storageError(CodeStorageUnavailable, "", err)
	}
	lock, err := openAt(directory, lockFile, os.O_RDWR|os.O_CREATE, 0o600, true)
	if err != nil {
		_ = directory.Close()
		return nil, storageError(CodeStorageUnavailable, "", err)
	}
	if err := lockExclusive(lock); err != nil {
		_ = lock.Close()
		_ = directory.Close()
		if errors.Is(err, errLocked) {
			return nil, newError(CodeStorageLocked, "", err)
		}
		return nil, storageError(CodeStorageUnavailable, "", err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		_ = lock.Close()
		_ = directory.Close()
		return nil, storageError(CodeStorageUnavailable, "", err)
	}
	opened := &store{base: base, directory: directory, root: root, lock: lock, hooks: hooks}
	if err := opened.prepare(); err != nil {
		opened.close()
		return nil, storageError(CodeStorageUnavailable, "", err)
	}
	return opened, nil
}

// prepare confirms both handles name the same directory, then durably creates
// the layout so later commits cannot outlive their parent directories.
func (s *store) prepare() error {
	anchored, err := s.directory.Stat()
	if err != nil {
		return err
	}
	rooted, err := s.root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(anchored, rooted) {
		return errUnsafeFile
	}
	for _, dir := range []string{jobsDir, stagingRoot, replacedRoot, installsRoot} {
		if err := s.root.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	for _, dir := range []string{"state", modelsDir, "."} {
		if err := s.syncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) close() {
	_ = s.root.Close()
	_ = s.lock.Close()
	_ = s.directory.Close()
}

func (s *store) open(rel string, flag int, perm os.FileMode, createParents bool) (*os.File, error) {
	return openAt(s.directory, rel, flag, perm, createParents)
}

func (s *store) writeAll(file *os.File, data []byte) error {
	written, err := s.hooks.write(file, data)
	if err == nil && written < len(data) {
		return io.ErrShortWrite
	}
	return err
}

func (s *store) syncDir(rel string) error {
	dir, err := s.root.Open(rel)
	if err != nil {
		return err
	}
	if err := s.hooks.sync(dir); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}

// writeAtomic durably replaces rel with data through a temporary sibling.
func (s *store) writeAtomic(rel string, data []byte) (err error) {
	dir, name := path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	temp := path.Join(dir, "."+name+"."+randomHex(6)+".tmp")
	defer func() {
		if err != nil {
			_ = s.root.Remove(temp)
		}
	}()
	file, err := s.open(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600, true)
	if err != nil {
		return err
	}
	if err := s.writeAll(file, data); err != nil {
		_ = file.Close()
		return err
	}
	if err := s.hooks.sync(file); err != nil {
		_ = file.Close()
		return err
	}
	if err := s.hooks.close(file); err != nil {
		return err
	}
	if err := s.hooks.rename(s.root, temp, rel); err != nil {
		return err
	}
	return s.syncDir(dir)
}

func (s *store) readBounded(rel string) ([]byte, error) {
	file, err := s.open(rel, os.O_RDONLY, 0, false)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxStateBytes {
		return nil, errors.New("state file is too large")
	}
	return data, nil
}

// removeTemporaries deletes atomic-write leftovers from an earlier crash.
func (s *store) removeTemporaries(dir string) {
	entries, err := fs.ReadDir(s.root.FS(), dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			_ = s.root.Remove(path.Join(dir, entry.Name()))
		}
	}
}

func randomHex(size int) string {
	buffer := make([]byte, size)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

func newJobID() string {
	return "job-" + randomHex(8)
}

func newInstallationID() string {
	return "ins-" + randomHex(8)
}

func journalPath(jobID string) string {
	return path.Join(jobsDir, jobID+".json")
}

func stagingDir(stagingID string) string {
	return path.Join(stagingRoot, stagingID)
}

func stagingFile(stagingID, artifactPath string) string {
	return path.Join(stagingRoot, stagingID, filesDir, artifactPath)
}

func installDir(bundleID, manifest string) string {
	return path.Join(installsRoot, bundleID, manifest)
}

// JobKind names how a job acquires a bundle.
type JobKind string

const (
	KindDownload  JobKind = "download"
	KindCopy      JobKind = "copy"
	KindReference JobKind = "reference"
)

// State is a persisted job lifecycle state.
type State string

const (
	StateQueued      State = "queued"
	StateDownloading State = "downloading"
	StateCopying     State = "copying"
	StateVerifying   State = "verifying"
	StatePublishing  State = "publishing"
	StateInstalled   State = "installed"
	StatePaused      State = "paused"
	StateInterrupted State = "interrupted"
	StateFailed      State = "failed"
)

func validState(state State) bool {
	switch state {
	case StateQueued, StateDownloading, StateCopying, StateVerifying, StatePublishing,
		StateInstalled, StatePaused, StateInterrupted, StateFailed:
		return true
	}
	return false
}

type journalArtifact struct {
	ArtifactID string `json:"artifactId"`
	Path       string `json:"path"`
	ETag       string `json:"etag,omitempty"`
}

// journal is the durable record of one job.
type journal struct {
	SchemaVersion  int               `json:"schemaVersion"`
	JobID          string            `json:"jobId"`
	Kind           JobKind           `json:"kind"`
	BundleID       string            `json:"bundleId"`
	ManifestSHA256 string            `json:"manifestSha256"`
	State          State             `json:"state"`
	StagingID      string            `json:"stagingId,omitempty"`
	SourcePath     string            `json:"sourcePath,omitempty"`
	InstallationID string            `json:"installationId,omitempty"`
	ErrorCode      Code              `json:"errorCode,omitempty"`
	ErrorArtifact  string            `json:"errorArtifact,omitempty"`
	Artifacts      []journalArtifact `json:"artifacts"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

// validate checks recovered identities before any path is built from them.
func (j *journal) validate(expectedID string) error {
	switch {
	case j.SchemaVersion != journalSchema:
		return fmt.Errorf("unsupported journal schema %d", j.SchemaVersion)
	case j.JobID != expectedID || !jobIDPattern.MatchString(j.JobID):
		return errors.New("journal identity does not match its file")
	case !catalog.ValidID(j.BundleID) || !catalog.ValidDigest(j.ManifestSHA256):
		return errors.New("invalid bundle identity")
	case !validState(j.State):
		return errors.New("invalid state")
	}
	switch j.Kind {
	case KindDownload:
		if j.StagingID != j.JobID {
			return errors.New("download does not own its staging")
		}
	case KindCopy:
		if j.StagingID != j.JobID || !filepath.IsAbs(j.SourcePath) {
			return errors.New("copy does not own its staging or lacks a source")
		}
	case KindReference:
		if j.StagingID != "" || !filepath.IsAbs(j.SourcePath) {
			return errors.New("reference has staging or lacks a source")
		}
	default:
		return errors.New("invalid kind")
	}
	for _, artifact := range j.Artifacts {
		if err := catalog.ValidatePath(artifact.Path); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) saveJournal(entry *journal) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return s.writeAtomic(journalPath(entry.JobID), data)
}

func (s *store) removeJournal(jobID string) error {
	if err := s.root.Remove(journalPath(jobID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return s.syncDir(jobsDir)
}

// loadJournals returns valid journals and the IDs of unreadable ones.
func (s *store) loadJournals() (entries []*journal, corrupt []string, err error) {
	files, err := fs.ReadDir(s.root.FS(), jobsDir)
	if err != nil {
		return nil, nil, err
	}
	for _, file := range files {
		id, isJournal := strings.CutSuffix(file.Name(), ".json")
		if !isJournal || !jobIDPattern.MatchString(id) {
			continue
		}
		var entry journal
		data, readErr := s.readBounded(journalPath(id))
		if readErr == nil {
			readErr = json.Unmarshal(data, &entry)
		}
		if readErr == nil {
			readErr = entry.validate(id)
		}
		if readErr != nil {
			corrupt = append(corrupt, id)
			continue
		}
		entries = append(entries, &entry)
	}
	return entries, corrupt, nil
}

// Ownership states who controls an installation's bytes.
type Ownership string

const (
	OwnershipManaged  Ownership = "managed"
	OwnershipExternal Ownership = "external"
)

// InstallStatus is the last known state of an installation's files.
type InstallStatus string

const (
	StatusVerified    InstallStatus = "verified_files"
	StatusUnavailable InstallStatus = "unavailable"
	StatusChanged     InstallStatus = "changed"
)

type sealedArtifact struct {
	ArtifactID string `json:"artifactId"`
	Path       string `json:"path"`
	SizeBytes  int64  `json:"sizeBytes"`
	SHA256     string `json:"sha256"`
}

// observation is a change hint for external files, never proof of content.
type observation struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"modTime"`
	Identity uint64    `json:"identity"`
}

type installation struct {
	ID             string           `json:"id"`
	Ownership      Ownership        `json:"ownership"`
	BundleID       string           `json:"bundleId"`
	ManifestSHA256 string           `json:"manifestSha256"`
	Status         InstallStatus    `json:"status"`
	Location       string           `json:"location,omitempty"`
	SizeBytes      int64            `json:"sizeBytes"`
	LastVerifiedAt time.Time        `json:"lastVerifiedAt"`
	Artifacts      []sealedArtifact `json:"artifacts"`
	Observations   []observation    `json:"observations,omitempty"`
	ErrorCode      Code             `json:"errorCode,omitempty"`
	ErrorArtifact  string           `json:"errorArtifact,omitempty"`
}

func (i *installation) validate() error {
	switch {
	case !catalog.ValidID(i.ID), !catalog.ValidID(i.BundleID), !catalog.ValidDigest(i.ManifestSHA256):
		return errors.New("invalid installation identity")
	case i.Status != StatusVerified && i.Status != StatusUnavailable && i.Status != StatusChanged:
		return errors.New("invalid installation status")
	case i.Ownership == OwnershipExternal && !filepath.IsAbs(i.Location):
		return errors.New("external installation without location")
	case i.Ownership != OwnershipExternal && i.Ownership != OwnershipManaged:
		return errors.New("invalid ownership")
	}
	return validateSealedArtifacts(i.Artifacts)
}

func validateSealedArtifacts(artifacts []sealedArtifact) error {
	if len(artifacts) == 0 || len(artifacts) > catalog.MaxArtifacts {
		return errors.New("artifact count out of bounds")
	}
	for _, artifact := range artifacts {
		if err := catalog.ValidatePath(artifact.Path); err != nil {
			return err
		}
		if artifact.SizeBytes < 0 || !catalog.ValidDigest(artifact.SHA256) {
			return errors.New("invalid sealed artifact")
		}
	}
	return nil
}

type registryDocument struct {
	SchemaVersion int             `json:"schemaVersion"`
	Installations []*installation `json:"installations"`
}

// loadRegistry never discards a damaged registry: it is preserved beside the
// original and managed installs are later re-adopted from their sealed manifests.
func (s *store) loadRegistry() (installs []*installation, recovered bool, err error) {
	data, err := s.readBounded(registryFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	var document registryDocument
	if err == nil {
		err = json.Unmarshal(data, &document)
	}
	if err == nil && document.SchemaVersion != registrySchema {
		err = fmt.Errorf("unsupported registry schema %d", document.SchemaVersion)
	}
	if err == nil {
		for _, entry := range document.Installations {
			if entry == nil {
				err = errors.New("empty registry entry")
				break
			}
			if err = entry.validate(); err != nil {
				break
			}
		}
	}
	if err == nil {
		return document.Installations, false, nil
	}
	aside := fmt.Sprintf("state/registry.corrupt-%d.json", time.Now().UnixNano())
	if renameErr := s.hooks.rename(s.root, registryFile, aside); renameErr != nil {
		return nil, false, renameErr
	}
	return nil, true, nil
}

func (s *store) saveRegistry(installs []*installation) error {
	data, err := json.Marshal(registryDocument{SchemaVersion: registrySchema, Installations: installs})
	if err != nil {
		return err
	}
	return s.writeAtomic(registryFile, data)
}

// sealedManifest is written inside every managed install before publication.
type sealedManifest struct {
	SchemaVersion  int              `json:"schemaVersion"`
	BundleID       string           `json:"bundleId"`
	ManifestSHA256 string           `json:"manifestSha256"`
	PublishedAt    time.Time        `json:"publishedAt"`
	Artifacts      []sealedArtifact `json:"artifacts"`
}

func (s *store) readSealed(dir string) (*sealedManifest, error) {
	data, err := s.readBounded(path.Join(dir, sealedName))
	if err != nil {
		return nil, err
	}
	var sealed sealedManifest
	if err := json.Unmarshal(data, &sealed); err != nil {
		return nil, err
	}
	if sealed.SchemaVersion != sealedSchema || !catalog.ValidID(sealed.BundleID) || !catalog.ValidDigest(sealed.ManifestSHA256) {
		return nil, errors.New("invalid sealed manifest")
	}
	if err := validateSealedArtifacts(sealed.Artifacts); err != nil {
		return nil, err
	}
	return &sealed, nil
}
