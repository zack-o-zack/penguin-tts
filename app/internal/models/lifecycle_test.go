package models

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"penguin.local/app/internal/catalog"
)

// gatedServer streams a prefix, then blocks until released or disconnected.
func gatedServer(prefixSize int) (http.HandlerFunc, chan struct{}, chan struct{}) {
	started := make(chan struct{}, 16)
	release := make(chan struct{})
	return func(w http.ResponseWriter, r *http.Request) {
		start := 0
		status := http.StatusOK
		if header := r.Header.Get("Range"); header != "" {
			_, _ = fmt.Sscanf(header, "bytes=%d-", &start)
			status = http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
		}
		w.Header().Set("ETag", savedETag)
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)-start))
		w.WriteHeader(status)
		if start < prefixSize {
			_, _ = w.Write(payload[start:prefixSize])
			start = prefixSize
		}
		w.(http.Flusher).Flush()
		started <- struct{}{}
		select {
		case <-release:
			_, _ = w.Write(payload[start:])
		case <-r.Context().Done():
		}
	}, started, release
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for signal")
	}
}

func (h *harness) waitBytes(id string, size int64) {
	h.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if got := stagedSize(h.t, h.stagingPath(id, fixtureFile)); got >= size {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("staging never reached %d bytes", size)
}

func TestPauseResumeKeepsBytesAndCompletes(t *testing.T) {
	handler, started, release := gatedServer(1024)
	h := newHarness(t, single(payload), handler)
	h.open()
	id := h.download()
	waitSignal(t, started)
	h.waitBytes(id, 1024)
	if err := h.manager.Pause(id); err != nil {
		t.Fatal(err)
	}
	if snapshot := h.waitState(id, StatePaused); snapshot.RetainedBytes != 1024 {
		t.Fatalf("job = %+v", snapshot)
	}
	close(release)
	h.resume(id)
	h.waitState(id, StateInstalled)
	h.assertInstalled(payload)
	if got := h.log.entries[1].Header.Get("Range"); got != "bytes=1024-" {
		t.Fatalf("resume range = %q", got)
	}
	if _, err := os.Stat(h.stagingPath(id, fixtureFile)); !os.IsNotExist(err) {
		t.Fatalf("staging remains: %v", err)
	}
}

func TestCancelDeletesPartialsWhileRunning(t *testing.T) {
	handler, started, release := gatedServer(1024)
	defer close(release)
	h := newHarness(t, single(payload), handler)
	h.open()
	id := h.download()
	waitSignal(t, started)
	h.waitBytes(id, 1024)
	if err := h.manager.Cancel(id); err != nil {
		t.Fatal(err)
	}
	h.waitGone(id)
	if _, err := os.Stat(filepath.Join(h.root, filepath.FromSlash(stagingDir(id)))); !os.IsNotExist(err) {
		t.Fatalf("staging remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.root, filepath.FromSlash(journalPath(id)))); !os.IsNotExist(err) {
		t.Fatalf("journal remains: %v", err)
	}
	if list := h.manager.ListInstallations(); list.RetainedBytes != 0 {
		t.Fatalf("retained = %d", list.RetainedBytes)
	}
}

func TestCancelDeletesPartialsAfterPause(t *testing.T) {
	handler, started, release := gatedServer(1024)
	defer close(release)
	h := newHarness(t, single(payload), handler)
	h.open()
	id := h.download()
	waitSignal(t, started)
	h.waitBytes(id, 1024)
	if err := h.manager.Pause(id); err != nil {
		t.Fatal(err)
	}
	h.waitState(id, StatePaused)
	if got := stagedSize(t, h.stagingPath(id, fixtureFile)); got != 1024 {
		t.Fatalf("pause did not keep progress: %d", got)
	}
	if err := h.manager.Cancel(id); err != nil {
		t.Fatal(err)
	}
	h.waitGone(id)
	if got := stagedSize(t, h.stagingPath(id, fixtureFile)); got != -1 {
		t.Fatalf("cancel kept progress: %d", got)
	}
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	h.open()
	if jobs := h.manager.ListJobs().Jobs; len(jobs) != 0 {
		t.Fatalf("canceled job returned after relaunch: %+v", jobs)
	}
}

func TestDownloadAfterCancelStartsFromZero(t *testing.T) {
	handler, started, release := gatedServer(1024)
	h := newHarness(t, single(payload), handler)
	h.open()
	id := h.download()
	waitSignal(t, started)
	h.waitBytes(id, 1024)
	if err := h.manager.Cancel(id); err != nil {
		t.Fatal(err)
	}
	h.waitGone(id)
	close(release)
	h.waitState(h.download(), StateInstalled)
	h.assertInstalled(payload)
	if got := h.log.entries[1].Header.Get("Range"); got != "" {
		t.Fatalf("new download reused canceled bytes: %q", got)
	}
}

func TestCancelFailureKeepsJobVisible(t *testing.T) {
	handler, started, release := gatedServer(1024)
	defer close(release)
	h := newHarness(t, single(payload), handler)
	h.open()
	id := h.download()
	waitSignal(t, started)
	h.waitBytes(id, 1024)
	directory := filepath.Join(h.root, filepath.FromSlash(stagingDir(id)), filesDir)
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	if err := h.manager.Cancel(id); err != nil {
		t.Fatal(err)
	}
	snapshot := h.waitState(id, StateFailed)
	if snapshot.ErrorCode != string(CodeAccessDenied) {
		t.Fatalf("job = %+v", snapshot)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.manager.Cancel(id); err != nil {
		t.Fatal(err)
	}
	h.waitGone(id)
}

func TestDuplicateRequestsJoinAndInstalledIsReturned(t *testing.T) {
	handler, started, release := gatedServer(1024)
	h := newHarness(t, single(payload), handler)
	h.open()
	first := h.download()
	waitSignal(t, started)
	if second := h.download(); second != first {
		t.Fatalf("duplicate job %s != %s", second, first)
	}
	close(release)
	h.waitState(first, StateInstalled)
	result, err := h.manager.EnqueueDownload(fixtureBundle)
	if err != nil {
		t.Fatal(err)
	}
	if result.JobID != "" || result.InstallationID == "" {
		t.Fatalf("result = %+v", result)
	}
	if h.log.count() != 1 {
		t.Fatalf("requests = %d", h.log.count())
	}
}

func TestQueueLimit(t *testing.T) {
	handler, started, release := gatedServer(1024)
	defer close(release)
	h := newHarness(t, single(payload), handler)
	descriptors := []catalog.Descriptor{}
	for i := range maxPendingJobs + 1 {
		descriptors = append(descriptors, fixtureDescriptor(h.server.URL, fmt.Sprintf("bundle-%d", i), "1", single(payload)))
	}
	h.catalog = fixtureCatalog(t, descriptors...)
	h.open()
	for i := range maxPendingJobs {
		if _, err := h.manager.EnqueueDownload(fmt.Sprintf("bundle-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	waitSignal(t, started)
	_, err := h.manager.EnqueueDownload(fmt.Sprintf("bundle-%d", maxPendingJobs))
	if asError(err).Code != CodeQueueFull {
		t.Fatalf("err = %v", err)
	}
	if h.log.count() != 1 {
		t.Fatalf("more than one transfer ran: %d", h.log.count())
	}
}

func TestIneligibleBundleCannotBeQueued(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	descriptor := fixtureDescriptor(h.server.URL, fixtureBundle, "1", single(payload))
	descriptor.Licensing[0].DistributionReview = catalog.ReviewUnreviewed
	h.catalog = fixtureCatalog(t, descriptor)
	h.open()
	if _, err := h.manager.EnqueueDownload(fixtureBundle); asError(err).Code != CodeNotEligible {
		t.Fatalf("err = %v", err)
	}
	if h.log.count() != 0 {
		t.Fatal("ineligible bundle reached the network")
	}
}

func TestCancelDuringBackoffStopsPromptly(t *testing.T) {
	h := newHarness(t, single(payload), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	waiting := make(chan struct{}, 1)
	h.controls.delay = func(int, time.Duration) time.Duration {
		waiting <- struct{}{}
		return time.Hour
	}
	h.open()
	id := h.download()
	waitSignal(t, waiting)
	if err := h.manager.Cancel(id); err != nil {
		t.Fatal(err)
	}
	h.waitGone(id)
	if h.log.count() != 1 {
		t.Fatalf("requests = %d", h.log.count())
	}
}

func TestCancelDuringHashStopsBeforePublication(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	hashing := make(chan struct{})
	proceed := make(chan struct{})
	h.controls.hashing = func(string) {
		close(hashing)
		<-proceed
	}
	h.open()
	id := h.download()
	waitSignal(t, hashing)
	if err := h.manager.Cancel(id); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	h.waitGone(id)
	h.assertNotInstalled()
}

func TestCancelDuringPublicationLosesToCommit(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	hooks := defaultHooks()
	reached := make(chan struct{})
	proceed := make(chan struct{})
	hooks.boundary = func(name string) {
		if name == "files-synced" {
			close(reached)
			<-proceed
		}
	}
	h.controls.hooks = &hooks
	h.open()
	id := h.download()
	waitSignal(t, reached)
	if err := h.manager.Cancel(id); asError(err).Code != CodeInvalidState {
		t.Fatalf("cancel during publication = %v", err)
	}
	close(proceed)
	h.waitState(id, StateInstalled)
	h.assertInstalled(payload)
}

func TestPreflightRequiresRemainingBytesAndHeadroom(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	descriptor := fixtureDescriptor(h.server.URL, fixtureBundle, "1", single(payload))
	descriptor.Space.StagingHeadroomBytes = 100
	h.catalog = fixtureCatalog(t, descriptor)
	h.bundle = h.catalog.Bundles[0]
	var free atomic.Uint64
	free.Store(uint64(len(payload)) - 28 + 99)
	h.controls.freeSpace = func(string) (uint64, error) { return free.Load(), nil }
	id := h.seedInterrupted(payload[:28], savedETag)
	h.open()
	h.waitState(id, StateInterrupted)
	h.resume(id)
	if snapshot := h.waitState(id, StateFailed); snapshot.ErrorCode != string(CodeNoSpace) {
		t.Fatalf("job = %+v", snapshot)
	}
	if h.log.count() != 0 {
		t.Fatal("preflight failure reached the network")
	}
	free.Store(free.Load() + 1)
	h.resume(id)
	h.waitState(id, StateInstalled, StateFailed)
	if h.log.count() != 1 {
		t.Fatalf("requests = %d", h.log.count())
	}
}

type faultCase struct {
	name   string
	inject func(*fsHooks)
	code   Code
}

func failNamedRename(target string, err error) func(*fsHooks) {
	return func(hooks *fsHooks) {
		real := hooks.rename
		hooks.rename = func(root *os.Root, from, to string) error {
			if strings.Contains(to, target) {
				return err
			}
			return real(root, from, to)
		}
	}
}

func failStagingWrite(result func([]byte) (int, error)) func(*fsHooks) {
	return func(hooks *fsHooks) {
		real := hooks.write
		hooks.write = func(file *os.File, data []byte) (int, error) {
			if file.Name() == fixtureFile {
				return result(data)
			}
			return real(file, data)
		}
	}
}

func TestFilesystemFaultsNeverReportInstalled(t *testing.T) {
	syncCalls := func(hooks *fsHooks, match string, err error) {
		real := hooks.sync
		hooks.sync = func(file *os.File) error {
			if filepath.Base(file.Name()) == match {
				return err
			}
			return real(file)
		}
	}
	cases := []faultCase{
		{"disk full", failStagingWrite(func([]byte) (int, error) { return 0, syscall.ENOSPC }), CodeNoSpace},
		{"quota", failStagingWrite(func([]byte) (int, error) { return 0, syscall.EDQUOT }), CodeNoSpace},
		{"access denied", failStagingWrite(func([]byte) (int, error) { return 0, syscall.EACCES }), CodeAccessDenied},
		{"read-only", failStagingWrite(func([]byte) (int, error) { return 0, syscall.EROFS }), CodeAccessDenied},
		{"short write", failStagingWrite(func(data []byte) (int, error) { return len(data) / 2, nil }), CodeWriteFailed},
		{"io error", failStagingWrite(func([]byte) (int, error) { return 0, syscall.EIO }), CodeWriteFailed},
		{"close failure", func(hooks *fsHooks) {
			real := hooks.close
			hooks.close = func(file *os.File) error {
				if file.Name() == fixtureFile {
					_ = file.Close()
					return syscall.EIO
				}
				return real(file)
			}
		}, CodeWriteFailed},
		{"file sync failure", func(hooks *fsHooks) { syncCalls(hooks, fixtureFile, syscall.EIO) }, CodePublishFailed},
		{"directory sync failure", func(hooks *fsHooks) { syncCalls(hooks, filesDir, syscall.EIO) }, CodePublishFailed},
		{"sealed sync failure", func(hooks *fsHooks) { syncCalls(hooks, sealedName, syscall.EIO) }, CodePublishFailed},
		{"final rename failure", failNamedRename(installsRoot, syscall.EIO), CodePublishFailed},
		{"registry rename failure", failNamedRename(registryFile, syscall.EIO), CodePublishFailed},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, single(payload), serveFile(payload))
			hooks := defaultHooks()
			test.inject(&hooks)
			h.controls.hooks = &hooks
			h.open()
			id := h.download()
			snapshot := h.waitState(id, StateFailed, StateInstalled)
			if snapshot.State != string(StateFailed) || snapshot.ErrorCode != string(test.code) {
				t.Fatalf("job = %+v", snapshot)
			}
			h.assertNotInstalled()
			if _, err := os.Stat(filepath.Join(h.root, filepath.FromSlash(stagingDir(id)))); err != nil {
				t.Fatalf("staging was not preserved: %v", err)
			}
			h.controls.hooks = nil
			h.reopen()
			h.assertNotInstalled()
			h.waitState(id, StateFailed)
			h.resume(id)
			h.waitState(id, StateInstalled)
			h.assertInstalled(payload)
		})
	}
}

func TestUpdateFailureKeepsPriorInstall(t *testing.T) {
	updated := append([]byte("v2"), payload...)
	var current atomic.Pointer[[]byte]
	current.Store(&payload)
	h := newHarness(t, single(payload), func(w http.ResponseWriter, r *http.Request) { serveFile(*current.Load())(w, r) })
	h.open()
	h.waitState(h.download(), StateInstalled)
	oldPath := h.installedPath(fixtureFile)

	current.Store(&updated)
	h.catalog = fixtureCatalog(t, fixtureDescriptor(h.server.URL, fixtureBundle, "2", single(updated)))
	hooks := defaultHooks()
	failNamedRename(installsRoot, syscall.EIO)(&hooks)
	h.controls.hooks = &hooks
	h.reopen()
	id := h.download()
	if snapshot := h.waitState(id, StateFailed); snapshot.ErrorCode != string(CodePublishFailed) {
		t.Fatalf("job = %+v", snapshot)
	}
	data, err := os.ReadFile(oldPath)
	if err != nil || string(data) != string(payload) {
		t.Fatalf("prior install damaged: %v", err)
	}

	h.controls.hooks = nil
	h.reopen()
	h.resume(id)
	h.waitState(id, StateInstalled)
	list := h.manager.ListInstallations()
	if len(list.Installations) != 2 {
		t.Fatalf("installations = %+v", list.Installations)
	}
	if data, err := os.ReadFile(oldPath); err != nil || string(data) != string(payload) {
		t.Fatalf("prior install removed by update: %v", err)
	}
}

func TestCorruptJournalIsDiscardableAndHarmless(t *testing.T) {
	cases := map[string]string{
		"not json":       `{`,
		"wrong identity": `{"schemaVersion":1,"jobId":"job-ffffffffffffffff","kind":"download"}`,
		"escaping staging": fmt.Sprintf(`{"schemaVersion":1,"jobId":"job-00000000000000b1","kind":"download","bundleId":"fixture-bundle","manifestSha256":"%s","state":"paused","stagingId":"../../..","artifacts":[]}`,
			strings.Repeat("a", 64)),
		"foreign staging": fmt.Sprintf(`{"schemaVersion":1,"jobId":"job-00000000000000b1","kind":"download","bundleId":"fixture-bundle","manifestSha256":"%s","state":"paused","stagingId":"job-00000000000000b2","artifacts":[]}`,
			strings.Repeat("a", 64)),
		"unsafe artifact": fmt.Sprintf(`{"schemaVersion":1,"jobId":"job-00000000000000b1","kind":"download","bundleId":"fixture-bundle","manifestSha256":"%s","state":"paused","stagingId":"job-00000000000000b1","artifacts":[{"artifactId":"a","path":"../../x"}]}`,
			strings.Repeat("a", 64)),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, single(payload), serveFile(payload))
			h.open()
			h.waitState(h.download(), StateInstalled)
			if err := h.manager.Close(); err != nil {
				t.Fatal(err)
			}
			id := "job-00000000000000b1"
			if err := os.WriteFile(filepath.Join(h.root, filepath.FromSlash(journalPath(id))), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(filepath.Dir(h.root), "outside.txt")
			if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			h.open()
			snapshot := h.waitState(id, StateFailed)
			if snapshot.ErrorCode != string(CodeInvalidJournal) || strings.Join(snapshot.Actions, ",") != "cancel" {
				t.Fatalf("job = %+v", snapshot)
			}
			h.assertInstalled(payload)
			if err := h.manager.Cancel(id); err != nil {
				t.Fatal(err)
			}
			h.assertInstalled(payload)
			if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
				t.Fatalf("outside file changed: %v", err)
			}
		})
	}
}

func TestJournalMustListCatalogArtifacts(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	id := h.seedInterrupted(payload[:128], savedETag)
	path := filepath.Join(h.root, filepath.FromSlash(journalPath(id)))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entry journal
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	entry.Artifacts = []journalArtifact{}
	if data, err = json.Marshal(entry); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	h.open()
	if snapshot := h.waitState(id, StateFailed); snapshot.ErrorCode != string(CodeInvalidJournal) {
		t.Fatalf("job = %+v", snapshot)
	}
	if err := h.manager.Resume(id); asError(err).Code != CodeInvalidJournal {
		t.Fatalf("resume = %v", err)
	}
}

func TestCancelCannotDeleteAnotherJobsStaging(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	victim := h.seedInterrupted(payload[:128], savedETag)
	attacker := "job-00000000000000b1"
	content := fmt.Sprintf(`{"schemaVersion":1,"jobId":"%s","kind":"download","bundleId":"%s","manifestSha256":"%s","state":"paused","stagingId":"%s","artifacts":[]}`,
		attacker, h.bundle.BundleID, h.bundle.ManifestSHA256, victim)
	if err := os.WriteFile(filepath.Join(h.root, filepath.FromSlash(journalPath(attacker))), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	h.open()
	h.waitState(attacker, StateFailed)
	if err := h.manager.Cancel(attacker); err != nil {
		t.Fatal(err)
	}
	if got := stagedSize(t, h.stagingPath(victim, fixtureFile)); got != 128 {
		t.Fatalf("victim staging = %d", got)
	}
}

func TestJournalBoundToCatalogManifest(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	id := h.seedInterrupted(payload[:128], savedETag)
	h.catalog = fixtureCatalog(t, fixtureDescriptor(h.server.URL, fixtureBundle, "changed", single(payload)))
	h.open()
	snapshot := h.waitState(id, StateFailed)
	if snapshot.ErrorCode != string(CodeInvalidManifest) {
		t.Fatalf("job = %+v", snapshot)
	}
	if err := h.manager.Resume(id); asError(err).Code != CodeInvalidManifest {
		t.Fatalf("resume = %v", err)
	}
	if h.log.count() != 0 {
		t.Fatal("stale validator was used")
	}
}

func TestCorruptRegistryPreservedAndManagedInstallsRecovered(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	h.open()
	h.waitState(h.download(), StateInstalled)
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(h.root, filepath.FromSlash(registryFile))
	if err := os.WriteFile(registry, []byte("{damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.open()
	h.assertInstalled(payload)
	list := h.manager.ListInstallations()
	if list.Notice == "" {
		t.Fatal("recovery was not disclosed")
	}
	matches, err := filepath.Glob(filepath.Join(h.root, "state", "registry.corrupt-*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("damaged registry not preserved: %v %v", matches, err)
	}
}

func TestInstalledFilesCheckedOnStartup(t *testing.T) {
	cases := map[string]struct {
		damage func(string) error
		status InstallStatus
		code   Code
	}{
		"missing":   {os.Remove, StatusUnavailable, CodeMissingArtifact},
		"truncated": {func(path string) error { return os.Truncate(path, 10) }, StatusChanged, CodeSizeMismatch},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, single(payload), serveFile(payload))
			h.open()
			h.waitState(h.download(), StateInstalled)
			if err := h.manager.Close(); err != nil {
				t.Fatal(err)
			}
			requests := h.log.count()
			if err := test.damage(h.installedPath(fixtureFile)); err != nil {
				t.Fatal(err)
			}
			h.open()
			install := h.manager.ListInstallations().Installations[0]
			if install.Status != string(test.status) || install.ErrorCode != string(test.code) || install.ErrorArtifact != fixtureFile {
				t.Fatalf("install = %+v", install)
			}
			if h.log.count() != requests {
				t.Fatal("startup used the network")
			}
		})
	}
}

func TestRepairReplacesDamagedInstall(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	h.open()
	h.waitState(h.download(), StateInstalled)
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.installedPath(fixtureFile), payload[:10], 0o600); err != nil {
		t.Fatal(err)
	}
	h.open()
	h.waitState(h.download(), StateInstalled)
	h.assertInstalled(payload)
}

func TestRemovalIsRejectedDuringPublication(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	h.open()
	h.waitState(h.download(), StateInstalled)
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	must(t, os.Truncate(h.installedPath(fixtureFile), 10))
	reached := make(chan struct{})
	proceed := make(chan struct{})
	hooks := defaultHooks()
	hooks.boundary = func(name string) {
		if name == "renamed" {
			close(reached)
			<-proceed
		}
	}
	h.controls.hooks = &hooks
	h.open()
	id := h.download()
	waitSignal(t, reached)
	install := h.manager.ListInstallations().Installations[0]
	if err := h.manager.RemoveInstallation(install.ID); asError(err).Code != CodeInvalidState {
		t.Fatalf("removal during publication = %v", err)
	}
	close(proceed)
	h.waitState(id, StateInstalled)
	h.assertInstalled(payload)
}

func onceFailingRename(armed *atomic.Bool, target string) func(*os.Root, string, string) error {
	real := defaultHooks().rename
	return func(root *os.Root, from, to string) error {
		if strings.Contains(to, target) && armed.CompareAndSwap(true, false) {
			return syscall.EIO
		}
		return real(root, from, to)
	}
}

func TestFailedRepairRestoresPreviousDirectory(t *testing.T) {
	for name, target := range map[string]string{"final rename": installsRoot, "registry commit": registryFile} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, single(payload), serveFile(payload))
			h.open()
			h.waitState(h.download(), StateInstalled)
			if err := h.manager.Close(); err != nil {
				t.Fatal(err)
			}
			damaged := payload[:10]
			must(t, os.WriteFile(h.installedPath(fixtureFile), damaged, 0o600))
			hooks := defaultHooks()
			var armed atomic.Bool
			hooks.rename = onceFailingRename(&armed, target)
			h.controls.hooks = &hooks
			h.open()
			armed.Store(true)
			id := h.download()
			h.waitState(id, StateFailed)
			data, err := os.ReadFile(h.installedPath(fixtureFile))
			if err != nil || string(data) != string(damaged) {
				t.Fatalf("previous directory was not restored: %v", err)
			}
			if got := stagedSize(t, h.stagingPath(id, fixtureFile)); got != int64(len(payload)) {
				t.Fatalf("staging = %d", got)
			}
			h.controls.hooks = nil
			h.reopen()
			if data, err := os.ReadFile(h.installedPath(fixtureFile)); err != nil || string(data) != string(damaged) {
				t.Fatalf("restart deleted the previous directory: %v", err)
			}
		})
	}
}

func TestOpensStayInOriginalRootAfterPathSwap(t *testing.T) {
	handler, started, release := gatedServer(1024)
	h := newHarness(t, single(payload), handler)
	h.open()
	waitJob := h.download()
	waitSignal(t, started)
	h.waitBytes(waitJob, 1024)
	if err := h.manager.Pause(waitJob); err != nil {
		t.Fatal(err)
	}
	h.waitState(waitJob, StatePaused)

	moved := h.root + "-moved"
	evil := filepath.Join(t.TempDir(), "evil")
	must(t, os.Mkdir(evil, 0o700))
	must(t, os.Rename(h.root, moved))
	must(t, os.Symlink(evil, h.root))
	t.Cleanup(func() {
		_ = os.Remove(h.root)
		_ = os.Rename(moved, h.root)
	})

	close(release)
	h.resume(waitJob)
	h.waitState(waitJob, StateInstalled)
	entries, err := os.ReadDir(evil)
	if err != nil || len(entries) != 0 {
		t.Fatalf("writes followed the swapped path: %v %v", entries, err)
	}
}

func TestSymlinkedStateDirectoryIsRejected(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	outside := t.TempDir()
	must(t, os.MkdirAll(h.root, 0o700))
	must(t, os.Symlink(outside, filepath.Join(h.root, "state")))
	_, err := open(t.Context(), h.options(), h.controls)
	if asError(err).Code != CodeUnsafePath {
		t.Fatalf("open = %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("lock followed the link: %v", entries)
	}
}

func TestSymlinkSubstitutionIsRejected(t *testing.T) {
	outsideDir := t.TempDir()
	victim := filepath.Join(outsideDir, fixtureFile)
	if err := os.WriteFile(victim, []byte("victim"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("staging file", func(t *testing.T) {
		h := newHarness(t, single(payload), serveFile(payload))
		id := h.seedInterrupted(payload[:10], savedETag)
		path := h.stagingPath(id, fixtureFile)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, path); err != nil {
			t.Fatal(err)
		}
		h.open()
		h.waitState(id, StateInterrupted)
		h.resume(id)
		if snapshot := h.waitState(id, StateFailed); snapshot.ErrorCode != string(CodeUnsafePath) {
			t.Fatalf("job = %+v", snapshot)
		}
		if data, _ := os.ReadFile(victim); string(data) != "victim" {
			t.Fatal("symlink target was overwritten")
		}
	})
	t.Run("staging directory", func(t *testing.T) {
		h := newHarness(t, single(payload), serveFile(payload))
		id := h.seedInterrupted(payload[:10], savedETag)
		directory := filepath.Join(h.root, filepath.FromSlash(stagingDir(id)))
		if err := os.RemoveAll(directory); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideDir, directory); err != nil {
			t.Fatal(err)
		}
		h.open()
		h.waitState(id, StateInterrupted)
		h.resume(id)
		if snapshot := h.waitState(id, StateFailed); snapshot.ErrorCode != string(CodeUnsafePath) {
			t.Fatalf("job = %+v", snapshot)
		}
		if err := h.manager.Cancel(id); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(victim); err != nil || string(data) != "victim" {
			t.Fatalf("discard escaped staging: %v", err)
		}
	})
}

func TestShutdownInterruptsAndRecoversWithoutNetwork(t *testing.T) {
	handler, started, release := gatedServer(1024)
	h := newHarness(t, single(payload), handler)
	h.open()
	id := h.download()
	waitSignal(t, started)
	h.waitBytes(id, 1024)
	begin := time.Now()
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(begin) > shutdownWait {
		t.Fatal("shutdown exceeded its bound")
	}
	requests := h.log.count()
	h.open()
	if snapshot := h.waitState(id, StateInterrupted); snapshot.RetainedBytes != 1024 {
		t.Fatalf("job = %+v", snapshot)
	}
	time.Sleep(50 * time.Millisecond)
	if h.log.count() != requests {
		t.Fatal("relaunch used the network without a user action")
	}
	close(release)
	h.resume(id)
	h.waitState(id, StateInstalled)
	h.assertInstalled(payload)
}

func TestShutdownIsBounded(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	block := make(chan struct{})
	reached := make(chan struct{})
	var once sync.Once
	hooks := defaultHooks()
	hooks.boundary = func(name string) {
		if name == "files-synced" {
			once.Do(func() { close(reached) })
			<-block
		}
	}
	h.controls.hooks = &hooks
	h.controls.shutdown = 50 * time.Millisecond
	h.open()
	h.download()
	waitSignal(t, reached)
	begin := time.Now()
	if err := h.manager.Close(); err == nil {
		t.Fatal("expected a shutdown deadline error")
	}
	if time.Since(begin) > time.Second {
		t.Fatal("close did not honor its deadline")
	}
	close(block)
	waitSignal(t, h.manager.done)
}

func TestSecondWriterIsRejected(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	h.open()
	_, err := open(context.Background(), h.options(), h.controls)
	if asError(err).Code != CodeStorageLocked {
		t.Fatalf("second writer = %v", err)
	}
}

func TestEventsAreOrderedAndThrottled(t *testing.T) {
	large := make([]byte, 8<<20)
	h := newHarness(t, single(large), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(large)))
		for offset := 0; offset < len(large); offset += 256 << 10 {
			_, _ = w.Write(large[offset:min(offset+256<<10, len(large))])
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
	})
	h.open()
	id := h.download()
	final := h.waitState(id, StateInstalled)
	time.Sleep(20 * time.Millisecond)
	h.eventsMu.Lock()
	defer h.eventsMu.Unlock()
	progress := 0
	var maxSequence uint64
	for _, event := range h.events {
		maxSequence = max(maxSequence, event.Sequence)
		if event.State == string(StateDownloading) && event.TransferredBytes > 0 {
			progress++
		}
	}
	if progress < 1 || progress > 10 {
		t.Fatalf("progress events = %d", progress)
	}
	last := h.events[len(h.events)-1]
	if last.State != string(StateInstalled) && final.Sequence > maxSequence {
		t.Fatalf("terminal state was not delivered: %+v", last)
	}
	if strings.Contains(fmt.Sprintf("%+v", h.events), "https://") {
		t.Fatal("source URLs reached events")
	}
}

func TestSpeedAndTimeLeftAreReportedOnlyWhileTransferring(t *testing.T) {
	large := make([]byte, 16<<20)
	const chunk = 256 << 10
	h := newHarness(t, single(large), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(large)))
		for offset := 0; offset < len(large); offset += chunk {
			_, _ = w.Write(large[offset:min(offset+chunk, len(large))])
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
	})
	h.open()
	h.waitState(h.download(), StateInstalled)
	h.eventsMu.Lock()
	defer h.eventsMu.Unlock()
	estimates := 0
	for _, event := range h.events {
		transferring := event.State == string(StateDownloading)
		if !transferring && (event.BytesPerSecond != 0 || event.SecondsRemaining != 0) {
			t.Fatalf("estimate outside transfer: %+v", event)
		}
		if transferring && event.BytesPerSecond > 0 && event.TransferredBytes < event.TotalBytes {
			estimates++
			// 256 KiB every 20 ms is at most about 13 MB/s.
			if event.BytesPerSecond > 14<<20 || event.SecondsRemaining <= 0 {
				t.Fatalf("implausible estimate: %+v", event)
			}
		}
	}
	if estimates == 0 {
		t.Fatal("no speed estimate was reported")
	}
}

func TestResumedBytesDoNotCountAsSpeed(t *testing.T) {
	large := make([]byte, 8<<20)
	h := newHarness(t, single(large), func(w http.ResponseWriter, r *http.Request) {
		start := len(large) - 1<<20
		w.Header().Set("ETag", savedETag)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(large)-1, len(large)))
		w.Header().Set("Content-Length", fmt.Sprint(len(large)-start))
		w.WriteHeader(http.StatusPartialContent)
		for offset := start; offset < len(large); offset += 64 << 10 {
			_, _ = w.Write(large[offset:min(offset+64<<10, len(large))])
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	})
	id := h.seedInterrupted(large[:len(large)-1<<20], savedETag)
	h.open()
	h.waitState(id, StateInterrupted)
	h.resume(id)
	h.waitState(id, StateInstalled)
	h.eventsMu.Lock()
	defer h.eventsMu.Unlock()
	for _, event := range h.events {
		// 64 KiB every 50 ms is about 1.3 MB/s; the 7 MB prefix must not register.
		if event.BytesPerSecond > 3<<20 {
			t.Fatalf("resumed prefix counted as speed: %+v", event)
		}
	}
}

func TestListCatalogHasNoURLsAndDistinguishesEngine(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	h.open()
	entries := h.manager.ListCatalog()
	if len(entries) != 1 || !entries[0].DownloadEligible || entries[0].EngineStatus != catalog.EngineUnknown {
		t.Fatalf("entries = %+v", entries)
	}
	if strings.Contains(fmt.Sprintf("%+v", entries[0].Artifacts), "https://") {
		t.Fatal("download URL exposed")
	}
}
