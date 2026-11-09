// Package models owns model downloads, verification, and installed files.
package models

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
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

func (m *Manager) saveAndEmitLocked(j *job) error {
	err := m.persistLocked(j)
	m.emitJobLocked(j)
	return err
}

func (m *Manager) setState(j *job, state State) error {
	return m.withLock(func() error {
		j.State = state
		j.ErrorCode, j.ErrorArtifact = "", ""
		return m.saveAndEmitLocked(j)
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

// unresumable reports a failure that retrying the same job cannot fix.
func (j *job) unresumable() bool {
	return j.ErrorCode == CodeInvalidManifest || j.ErrorCode == CodeInvalidJournal
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

func (m *Manager) findInstallLocked(bundleID, manifest string, ownership Ownership) *installation {
	for _, install := range m.installs {
		if install.BundleID == bundleID && install.ManifestSHA256 == manifest && install.Ownership == ownership {
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
		if install := m.findInstallLocked(bundle.BundleID, bundle.ManifestSHA256, OwnershipManaged); install != nil && install.Status == StatusVerified {
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
			return m.saveAndEmitLocked(j)
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
		if j.unresumable() {
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
		err = m.saveAndEmitLocked(j)
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
	if err := m.store.discardStaging(j.StagingID); err != nil {
		return storageError(CodeWriteFailed, "", err)
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
		return m.saveAndEmitLocked(j)
	})
}

func (m *Manager) stagedBytesLocked(j *job) int64 {
	return sumSizes(m.store.stagedSizes(j.StagingID, j.Artifacts))
}

func sumSizes(sizes map[string]int64) (total int64) {
	for _, size := range sizes {
		total += size
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

func modelsPath(base string) string {
	return filepath.Join(base, modelsDir)
}
