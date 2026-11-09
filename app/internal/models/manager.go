// Package models owns model downloads, verification, and installed files.
package models

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"math"
	"net/http"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"penguin.local/app/internal/catalog"
)

const (
	maxPendingJobs   = 20
	progressInterval = 250 * time.Millisecond
	rateSmoothing    = 0.3
	shutdownWait     = 5 * time.Second
)

var (
	errShutdown = errors.New("shutting down")
	errStopped  = errors.New("stopped by request")
)

type stopRequest string

const (
	stopNone   stopRequest = ""
	stopPause  stopRequest = "pause"
	stopCancel stopRequest = "cancel"
)

// Options configures a Manager.
type Options struct {
	Root            string
	Catalog         *catalog.Catalog
	Transport       http.RoundTripper
	OnJob           func(JobSnapshot)
	OnInstallations func(uint64)
}

// testControls replaces timing and filesystem behavior in tests.
type testControls struct {
	hooks     *fsHooks
	idle      time.Duration
	delay     func(int, time.Duration) time.Duration
	freeSpace func(string) (uint64, error)
	hashing   func(string)
	shutdown  time.Duration
}

type job struct {
	journal
	running     bool
	stop        stopRequest
	cancel      context.CancelCauseFunc
	fileBytes   map[string]int64
	verified    map[string]int64
	retained    int64
	current     string
	sequence    uint64
	lastEmitted time.Time
	rate        float64
	rateAt      time.Time
	rateMark    int64
}

// Manager is the single writer for model storage in this process.
type Manager struct {
	options Options
	store   *store
	catalog *catalog.Catalog
	tr      *transferer
	hooks   fsHooks
	free    func(string) (uint64, error)
	hashing func(string)
	waitFor time.Duration

	mu       sync.Mutex
	jobs     map[string]*job
	installs []*installation
	notice   string
	sequence uint64
	outbox   []func()

	ctx       context.Context
	cancelAll context.CancelCauseFunc
	wake      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// Open acquires the writer lock, reconciles saved state without network
// traffic, and starts the worker.
func Open(ctx context.Context, options Options) (*Manager, error) {
	return open(ctx, options, testControls{})
}

func open(ctx context.Context, options Options, controls testControls) (*Manager, error) {
	hooks := defaultHooks()
	if controls.hooks != nil {
		hooks = *controls.hooks
	}
	opened, err := openStore(options.Root, hooks)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		options: options,
		store:   opened,
		catalog: options.Catalog,
		hooks:   hooks,
		free:    freeBytes,
		hashing: func(string) {},
		waitFor: shutdownWait,
		jobs:    map[string]*job{},
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	manager.tr = &transferer{client: newHTTPClient(options.Transport), store: opened, idle: defaultIdleTimeout, delay: retryDelay}
	if controls.idle > 0 {
		manager.tr.idle = controls.idle
	}
	if controls.delay != nil {
		manager.tr.delay = controls.delay
	}
	if controls.freeSpace != nil {
		manager.free = controls.freeSpace
	}
	if controls.hashing != nil {
		manager.hashing = controls.hashing
	}
	if controls.shutdown > 0 {
		manager.waitFor = controls.shutdown
	}
	if err := manager.reconcile(ctx); err != nil {
		opened.close()
		return nil, err
	}
	manager.ctx, manager.cancelAll = context.WithCancelCause(context.Background())
	go manager.work()
	return manager, nil
}

// Close stops work and waits a bounded time. If a worker cannot stop in time
// the writer lock stays held until the process exits.
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.cancelAll(errShutdown)
		timer := time.NewTimer(m.waitFor)
		defer timer.Stop()
		select {
		case <-m.done:
			m.store.close()
		case <-timer.C:
			m.closeErr = errors.New("model worker did not stop before the shutdown deadline")
		}
	})
	return m.closeErr
}

func (m *Manager) withLock(action func() error) error {
	m.mu.Lock()
	err := action()
	outbox := m.outbox
	m.outbox = nil
	m.mu.Unlock()
	for _, deliver := range outbox {
		deliver()
	}
	return err
}

func (m *Manager) emitJobLocked(j *job) {
	m.sequence++
	j.sequence = m.sequence
	j.lastEmitted = time.Now()
	if m.options.OnJob != nil {
		snapshot := m.snapshotLocked(j)
		m.outbox = append(m.outbox, func() { m.options.OnJob(snapshot) })
	}
}

func (m *Manager) emitRemovedLocked(j *job) {
	m.sequence++
	if m.options.OnJob != nil {
		snapshot := m.snapshotLocked(j)
		snapshot.Sequence = m.sequence
		snapshot.Removed = true
		m.outbox = append(m.outbox, func() { m.options.OnJob(snapshot) })
	}
}

func (m *Manager) emitInstallationsLocked() {
	m.sequence++
	if m.options.OnInstallations != nil {
		sequence := m.sequence
		m.outbox = append(m.outbox, func() { m.options.OnInstallations(sequence) })
	}
}

func (m *Manager) persistLocked(j *job) error {
	j.UpdatedAt = time.Now().UTC()
	if err := m.store.saveJournal(&j.journal); err != nil {
		return storageError(CodeWriteFailed, "", err)
	}
	return nil
}

func (m *Manager) setState(j *job, state State) error {
	return m.withLock(func() error {
		j.State = state
		j.ErrorCode, j.ErrorArtifact = "", ""
		err := m.persistLocked(j)
		m.emitJobLocked(j)
		return err
	})
}

func (m *Manager) setCurrent(j *job, artifactPath string) {
	_ = m.withLock(func() error {
		j.current = artifactPath
		return nil
	})
}

func (m *Manager) reportProgress(j *job, update func()) {
	_ = m.withLock(func() error {
		update()
		if time.Since(j.lastEmitted) >= progressInterval {
			j.sampleRate(time.Now())
			m.emitJobLocked(j)
		}
		return nil
	})
}

func (m *Manager) bundleFor(j *journal) (catalog.Bundle, error) {
	bundle, ok := m.catalog.Find(j.BundleID)
	if !ok || bundle.ManifestSHA256 != j.ManifestSHA256 {
		return catalog.Bundle{}, newError(CodeInvalidManifest, "", errors.New("journal does not match the catalog"))
	}
	return bundle, nil
}

func journalArtifacts(bundle catalog.Bundle) []journalArtifact {
	artifacts := make([]journalArtifact, 0, len(bundle.Artifacts))
	for _, artifact := range bundle.Artifacts {
		artifacts = append(artifacts, journalArtifact{ArtifactID: artifact.ArtifactID, Path: artifact.Path})
	}
	return artifacts
}

func (j *job) artifact(artifactPath string) *journalArtifact {
	for index := range j.Artifacts {
		if j.Artifacts[index].Path == artifactPath {
			return &j.Artifacts[index]
		}
	}
	return nil
}

func (j *job) settled() bool {
	return !j.running && (j.State == StatePaused || j.State == StateInterrupted || j.State == StateFailed)
}

func (j *job) pending() bool {
	return j.State != StateInstalled && j.State != StateFailed
}

func (m *Manager) enqueueLocked(kind JobKind, bundle catalog.Bundle, source, installationID string) (EnqueueResult, error) {
	if m.ctx.Err() != nil {
		return EnqueueResult{}, newError(CodeInvalidState, "", m.ctx.Err())
	}
	pending := 0
	for _, existing := range m.jobs {
		if existing.pending() {
			pending++
		}
	}
	if pending >= maxPendingJobs {
		return EnqueueResult{}, newError(CodeQueueFull, "", nil)
	}
	id := newJobID()
	now := time.Now().UTC()
	entry := journal{
		SchemaVersion:  journalSchema,
		JobID:          id,
		Kind:           kind,
		BundleID:       bundle.BundleID,
		ManifestSHA256: bundle.ManifestSHA256,
		State:          StateQueued,
		SourcePath:     source,
		InstallationID: installationID,
		Artifacts:      journalArtifacts(bundle),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if kind != KindReference {
		entry.StagingID = id
	}
	if err := m.store.saveJournal(&entry); err != nil {
		return EnqueueResult{}, storageError(CodeWriteFailed, "", err)
	}
	created := &job{journal: entry}
	m.jobs[id] = created
	m.emitJobLocked(created)
	m.signal()
	return EnqueueResult{JobID: id}, nil
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) findJoinableLocked(bundle catalog.Bundle, kinds ...JobKind) *job {
	for _, existing := range m.jobs {
		if existing.BundleID == bundle.BundleID && existing.ManifestSHA256 == bundle.ManifestSHA256 &&
			existing.State != StateInstalled && slices.Contains(kinds, existing.Kind) {
			return existing
		}
	}
	return nil
}

func (m *Manager) findInstallLocked(bundle catalog.Bundle, ownership Ownership) *installation {
	for _, install := range m.installs {
		if install.BundleID == bundle.BundleID && install.ManifestSHA256 == bundle.ManifestSHA256 && install.Ownership == ownership {
			return install
		}
	}
	return nil
}

// EnqueueDownload joins a matching job, returns a verified install, or queues
// a new transfer.
func (m *Manager) EnqueueDownload(bundleID string) (result EnqueueResult, err error) {
	err = m.withLock(func() error {
		bundle, ok := m.catalog.Find(bundleID)
		if !ok {
			return newError(CodeUnknownBundle, "", nil)
		}
		if !bundle.DownloadEligibility().Eligible {
			return newError(CodeNotEligible, "", nil)
		}
		if existing := m.findJoinableLocked(bundle, KindDownload, KindCopy); existing != nil {
			result = EnqueueResult{JobID: existing.JobID}
			return nil
		}
		if install := m.findInstallLocked(bundle, OwnershipManaged); install != nil && install.Status == StatusVerified {
			result = EnqueueResult{InstallationID: install.ID}
			return nil
		}
		var enqueueErr error
		result, enqueueErr = m.enqueueLocked(KindDownload, bundle, "", "")
		return enqueueErr
	})
	return result, err
}

func (m *Manager) lookup(jobID string) (*job, error) {
	j, ok := m.jobs[jobID]
	if !ok {
		return nil, newError(CodeUnknownJob, "", nil)
	}
	return j, nil
}

// Pause stops a job and keeps its recoverable staging.
func (m *Manager) Pause(jobID string) error {
	return m.withLock(func() error {
		j, err := m.lookup(jobID)
		if err != nil {
			return err
		}
		switch {
		case j.State == StateQueued && !j.running:
			j.State = StatePaused
			err := m.persistLocked(j)
			m.emitJobLocked(j)
			return err
		case j.running && j.State != StatePublishing && j.stop == stopNone:
			m.requestStopLocked(j, stopPause)
			return nil
		}
		return newError(CodeInvalidState, "", nil)
	})
}

func (m *Manager) requestStopLocked(j *job, stop stopRequest) {
	j.stop = stop
	j.cancel(errStopped)
	m.emitJobLocked(j)
}

// Resume requeues paused, interrupted, or failed work.
func (m *Manager) Resume(jobID string) error {
	return m.withLock(func() error {
		j, err := m.lookup(jobID)
		if err != nil {
			return err
		}
		if !j.settled() {
			return newError(CodeInvalidState, "", nil)
		}
		if j.ErrorCode == CodeInvalidManifest || j.ErrorCode == CodeInvalidJournal {
			return newError(j.ErrorCode, "", nil)
		}
		bundle, err := m.bundleFor(&j.journal)
		if err != nil {
			return err
		}
		if j.Kind == KindDownload && !bundle.DownloadEligibility().Eligible {
			return newError(CodeNotEligible, "", nil)
		}
		j.State = StateQueued
		j.ErrorCode, j.ErrorArtifact = "", ""
		err = m.persistLocked(j)
		m.emitJobLocked(j)
		m.signal()
		return err
	})
}

// Cancel stops a job and deletes the partial files it owns. Publication is the
// serialization point: once a job is publishing, publication wins.
func (m *Manager) Cancel(jobID string) error {
	return m.withLock(func() error {
		j, err := m.lookup(jobID)
		if err != nil {
			return err
		}
		switch {
		case j.State == StatePublishing || j.State == StateInstalled:
			return newError(CodeInvalidState, "", nil)
		case j.running:
			if j.stop != stopCancel {
				m.requestStopLocked(j, stopCancel)
			}
			return nil
		}
		return m.discardLocked(j)
	})
}

// discardLocked runs only once writers have stopped, and removes nothing but
// the job's own staging and journal.
func (m *Manager) discardLocked(j *job) error {
	if j.StagingID != "" {
		if err := m.store.root.RemoveAll(stagingDir(j.StagingID)); err != nil {
			return storageError(CodeWriteFailed, "", err)
		}
	}
	if err := m.store.removeJournal(j.JobID); err != nil {
		return storageError(CodeWriteFailed, "", err)
	}
	delete(m.jobs, j.JobID)
	m.emitRemovedLocked(j)
	m.emitInstallationsLocked()
	return nil
}

func (m *Manager) work() {
	defer close(m.done)
	for {
		j, jobCtx := m.claimNext()
		if j == nil {
			select {
			case <-m.wake:
				continue
			case <-m.ctx.Done():
				return
			}
		}
		m.finish(j, m.run(jobCtx, j))
	}
}

func (m *Manager) claimNext() (claimed *job, ctx context.Context) {
	_ = m.withLock(func() error {
		if m.ctx.Err() != nil {
			return nil
		}
		for _, candidate := range m.jobs {
			if candidate.State != StateQueued {
				continue
			}
			if claimed == nil || candidate.CreatedAt.Before(claimed.CreatedAt) ||
				(candidate.CreatedAt.Equal(claimed.CreatedAt) && candidate.JobID < claimed.JobID) {
				claimed = candidate
			}
		}
		if claimed != nil {
			claimed.running = true
			claimed.stop = stopNone
			claimed.fileBytes = map[string]int64{}
			claimed.verified = map[string]int64{}
			claimed.rate, claimed.rateAt, claimed.rateMark = 0, time.Time{}, 0
			ctx, claimed.cancel = context.WithCancelCause(m.ctx)
		}
		return nil
	})
	return claimed, ctx
}

func (m *Manager) finish(j *job, err error) {
	_ = m.withLock(func() error {
		defer j.cancel(nil)
		j.running = false
		j.current = ""
		if j.stop == stopCancel {
			if err = m.discardLocked(j); err == nil {
				return nil
			}
			j.stop = stopNone
		}
		switch {
		case j.stop == stopPause:
			j.State = StatePaused
		case err == nil:
		case errors.Is(err, errShutdown) || errors.Is(err, context.Canceled):
			j.State = StateInterrupted
		default:
			failure := asError(err)
			j.State = StateFailed
			j.ErrorCode, j.ErrorArtifact = failure.Code, failure.Artifact
		}
		j.stop = stopNone
		if err == nil && j.State == StateInstalled {
			j.retained = 0
		} else {
			j.retained = m.stagedBytesLocked(j)
		}
		persistErr := m.persistLocked(j)
		m.emitJobLocked(j)
		return persistErr
	})
}

func (m *Manager) stagedBytesLocked(j *job) int64 {
	if j.StagingID == "" {
		return 0
	}
	var total int64
	for _, artifact := range j.Artifacts {
		if info, err := m.store.root.Lstat(stagingFile(j.StagingID, artifact.Path)); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}

func (m *Manager) run(ctx context.Context, j *job) error {
	bundle, err := m.bundleFor(&j.journal)
	if err != nil {
		return err
	}
	switch j.Kind {
	case KindDownload:
		return m.runDownload(ctx, j, bundle)
	case KindCopy:
		return m.runCopy(ctx, j, bundle)
	default:
		return m.runReference(ctx, j, bundle)
	}
}

func (m *Manager) preflight(j *job, bundle catalog.Bundle, headroom int64) error {
	var retained int64
	_ = m.withLock(func() error {
		retained = m.stagedBytesLocked(j)
		for _, artifact := range j.Artifacts {
			if info, err := m.store.root.Lstat(stagingFile(j.StagingID, artifact.Path)); err == nil && info.Mode().IsRegular() {
				j.fileBytes[artifact.Path] = info.Size()
			}
		}
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
	budgets := map[string]int{}
	for _, artifact := range bundle.Artifacts {
		budgets[artifact.Path] = maxTransferAttempts
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
			budget := budgets[artifact.Path]
			err := m.transferArtifact(ctx, j, artifact, &budget)
			budgets[artifact.Path] = budget
			if err != nil {
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
				if budgets[artifact.Path] <= 0 {
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
		err := m.store.root.Remove(stagingFile(j.StagingID, artifact.Path))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
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
	return hashFile(ctx, m.store.reader(), stagingFile(j.StagingID, artifact.Path), artifact.Path, artifact.Size(), artifact.SHA256, func(done int64) {
		m.reportProgress(j, func() { j.verified[artifact.Path] = done })
	})
}

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

// sampleRate smooths transfer speed over this run only, so a resumed prefix
// never counts as speed. A restarted file begins a new measurement.
func (j *job) sampleRate(now time.Time) {
	transferred, _ := j.snapshotBytes()
	if j.rateAt.IsZero() || transferred < j.rateMark {
		j.rate, j.rateAt, j.rateMark = 0, now, transferred
		return
	}
	current := float64(transferred-j.rateMark) / now.Sub(j.rateAt).Seconds()
	if j.rate > 0 {
		current = rateSmoothing*current + (1-rateSmoothing)*j.rate
	}
	j.rate, j.rateAt, j.rateMark = current, now, transferred
}

func (j *job) snapshotBytes() (transferred, verified int64) {
	for _, size := range j.fileBytes {
		transferred += size
	}
	for _, size := range j.verified {
		verified += size
	}
	return transferred, verified
}

func (m *Manager) snapshotLocked(j *job) JobSnapshot {
	transferred, verified := j.snapshotBytes()
	snapshot := JobSnapshot{
		JobID:            j.JobID,
		Kind:             string(j.Kind),
		BundleID:         j.BundleID,
		State:            string(j.State),
		Sequence:         j.sequence,
		TransferredBytes: transferred,
		VerifiedBytes:    verified,
		RetainedBytes:    j.retained,
		CurrentArtifact:  j.current,
		ErrorCode:        string(j.ErrorCode),
		ErrorArtifact:    j.ErrorArtifact,
		Actions:          j.actions(),
		CreatedAt:        j.CreatedAt,
		DisplayName:      cmp.Or(j.BundleID, "Unknown download"),
	}
	if j.ErrorCode != "" {
		snapshot.ErrorMessage = newError(j.ErrorCode, j.ErrorArtifact, nil).Error()
	}
	if bundle, ok := m.catalog.Find(j.BundleID); ok {
		snapshot.DisplayName = bundle.DisplayName
		if bundle.ManifestSHA256 == j.ManifestSHA256 {
			snapshot.TotalBytes = bundle.Space.RequiredBytes
		}
	}
	if j.running {
		snapshot.RetainedBytes = transferred
	}
	if j.running && j.stop == stopNone && j.rate > 0 && (j.State == StateDownloading || j.State == StateCopying) {
		snapshot.BytesPerSecond = int64(j.rate)
		if snapshot.TotalBytes > transferred {
			snapshot.SecondsRemaining = int64(math.Ceil(float64(snapshot.TotalBytes-transferred) / j.rate))
		}
	}
	return snapshot
}

func (j *job) actions() []string {
	actions := []string{}
	if j.running && j.stop != stopNone {
		return actions
	}
	switch j.State {
	case StateQueued, StateDownloading, StateCopying, StateVerifying:
		return []string{"pause", "cancel"}
	case StatePaused, StateInterrupted:
		return []string{"resume", "cancel"}
	case StateFailed:
		if j.ErrorCode == CodeInvalidManifest || j.ErrorCode == CodeInvalidJournal {
			return []string{"cancel"}
		}
		return []string{"resume", "cancel"}
	}
	return actions
}

// ListJobs returns an authoritative snapshot.
func (m *Manager) ListJobs() JobList {
	var list JobList
	_ = m.withLock(func() error {
		list.Sequence = m.sequence
		list.Jobs = make([]JobSnapshot, 0, len(m.jobs))
		for _, j := range m.jobs {
			list.Jobs = append(list.Jobs, m.snapshotLocked(j))
		}
		return nil
	})
	slices.SortFunc(list.Jobs, func(a, b JobSnapshot) int {
		if order := a.CreatedAt.Compare(b.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.JobID, b.JobID)
	})
	return list
}

// ListCatalog describes bundles without exposing download URLs.
func (m *Manager) ListCatalog() []CatalogEntry {
	entries := make([]CatalogEntry, 0, len(m.catalog.Bundles))
	for _, bundle := range m.catalog.Bundles {
		entries = append(entries, newCatalogEntry(bundle))
	}
	return entries
}

func modelsPath(base string) string {
	return filepath.Join(base, modelsDir)
}
