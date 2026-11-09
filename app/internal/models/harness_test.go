package models

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"penguin.local/app/internal/catalog"
)

const (
	fixtureRevision = "0123456789abcdef0123456789abcdef01234567"
	fixtureBundle   = "fixture-bundle"
	fixtureFile     = "model.bin"
	savedETag       = `"fixture-v1"`
	waitTimeout     = 10 * time.Second
)

var payload = bytes.Repeat([]byte("Penguin download fixture.\n"), 256)

type fixtureArtifact struct {
	path string
	data []byte
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fixtureDescriptor(baseURL, bundleID, release string, files []fixtureArtifact) catalog.Descriptor {
	license := "Apache-2.0"
	descriptor := catalog.Descriptor{
		SchemaVersion:  catalog.SchemaVersion,
		CatalogVersion: release,
		ModelID:        "fixture",
		VariantID:      "variant",
		BundleID:       bundleID,
		VoiceIDs:       []string{},
		DisplayName:    "Fixture " + bundleID,
		Publisher:      "Penguin tests",
		Description:    "Synthetic bytes.",
		SourceURL:      "https://example.test/source",
		ModelCardURL:   "https://example.test/card",
		ReleaseLabel:   release,
		Languages:      []string{"en-US"},
		Format:         "bin",
		Precision:      "none",
		Engine:         catalog.Engine{Status: catalog.EngineUnknown},
		Licensing: []catalog.LicenseEvidence{{
			Kind: "model", Subject: "Fixture", SourceURL: "https://example.test/license",
			SourceRevision: fixtureRevision, LicenseID: &license, DistributionReview: catalog.ReviewApproved,
		}},
	}
	for index, file := range files {
		size := int64(len(file.data))
		descriptor.Artifacts = append(descriptor.Artifacts, catalog.Artifact{
			ArtifactID:     fmt.Sprintf("artifact-%d", index),
			Repository:     "owner/repo",
			Revision:       fixtureRevision,
			Path:           file.path,
			URL:            baseURL + "/owner/repo/resolve/" + fixtureRevision + "/" + file.path,
			SizeBytes:      &size,
			SHA256:         digestOf(file.data),
			HashSource:     "fixture",
			HashObservedAt: "2026-11-09",
		})
		descriptor.Space.RequiredBytes += size
	}
	descriptor.Space.InstalledBytes = descriptor.Space.RequiredBytes
	return descriptor
}

func fixtureCatalog(t testing.TB, descriptors ...catalog.Descriptor) *catalog.Catalog {
	t.Helper()
	loaded := &catalog.Catalog{}
	for _, descriptor := range descriptors {
		bundle, err := catalog.NewBundle(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		loaded.Bundles = append(loaded.Bundles, bundle)
	}
	return loaded
}

// requestLog counts server traffic for assertions about network behavior.
type requestLog struct {
	mu      sync.Mutex
	entries []*http.Request
}

func (l *requestLog) add(request *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, request.Clone(context.Background()))
}

func (l *requestLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func (l *requestLog) methods() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	methods := make([]string, 0, len(l.entries))
	for _, entry := range l.entries {
		methods = append(methods, entry.Method)
	}
	return methods
}

type harness struct {
	t        *testing.T
	root     string
	server   *httptest.Server
	log      *requestLog
	catalog  *catalog.Catalog
	bundle   catalog.Bundle
	controls testControls
	manager  *Manager

	eventsMu sync.Mutex
	events   []JobSnapshot
}

func noDelay(int, time.Duration) time.Duration { return 0 }

func plentyOfSpace(string) (uint64, error) { return 1 << 40, nil }

func newHarness(t *testing.T, files []fixtureArtifact, handler http.HandlerFunc) *harness {
	t.Helper()
	h := &harness{
		t:        t,
		root:     filepath.Join(t.TempDir(), "app data é"),
		log:      &requestLog{},
		controls: testControls{delay: noDelay, freeSpace: plentyOfSpace, idle: 2 * time.Second},
	}
	h.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.log.add(r)
		handler(w, r)
	}))
	t.Cleanup(h.server.Close)
	h.catalog = fixtureCatalog(t, fixtureDescriptor(h.server.URL, fixtureBundle, "1", files))
	h.bundle = h.catalog.Bundles[0]
	return h
}

func single(data []byte) []fixtureArtifact {
	return []fixtureArtifact{{path: fixtureFile, data: data}}
}

func serveFile(data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", savedETag)
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data)
	}
}

func serveFiles(files []fixtureArtifact) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, file := range files {
			if filepath.Base(r.URL.Path) == filepath.Base(file.path) {
				serveFile(file.data)(w, r)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h *harness) options() Options {
	return Options{
		Root:      h.root,
		Catalog:   h.catalog,
		Transport: h.server.Client().Transport,
		OnJob: func(snapshot JobSnapshot) {
			h.eventsMu.Lock()
			defer h.eventsMu.Unlock()
			h.events = append(h.events, snapshot)
		},
	}
}

func (h *harness) open() *Manager {
	h.t.Helper()
	manager, err := open(context.Background(), h.options(), h.controls)
	if err != nil {
		h.t.Fatal(err)
	}
	h.manager = manager
	h.t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func (h *harness) reopen() *Manager {
	h.t.Helper()
	if err := h.manager.Close(); err != nil {
		h.t.Fatal(err)
	}
	return h.open()
}

func (h *harness) job(id string) (JobSnapshot, bool) {
	for _, snapshot := range h.manager.ListJobs().Jobs {
		if snapshot.JobID == id {
			return snapshot, true
		}
	}
	return JobSnapshot{}, false
}

func (h *harness) waitState(id string, states ...State) JobSnapshot {
	h.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	var last JobSnapshot
	for time.Now().Before(deadline) {
		snapshot, ok := h.job(id)
		if ok {
			last = snapshot
			if slices.Contains(states, State(snapshot.State)) {
				return snapshot
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("job never reached %v: %+v", states, last)
	return last
}

func (h *harness) waitGone(id string) {
	h.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if _, ok := h.job(id); !ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("job %s was not removed", id)
}

func (h *harness) download() string {
	h.t.Helper()
	result, err := h.manager.EnqueueDownload(fixtureBundle)
	if err != nil {
		h.t.Fatal(err)
	}
	if result.JobID == "" {
		h.t.Fatalf("no job created: %+v", result)
	}
	return result.JobID
}

func (h *harness) stagingPath(stagingID, artifact string) string {
	return filepath.Join(h.root, filepath.FromSlash(stagingFile(stagingID, artifact)))
}

func (h *harness) installedPath(artifact string) string {
	return filepath.Join(h.root, filepath.FromSlash(installDir(h.bundle.BundleID, h.bundle.ManifestSHA256)), filesDir, artifact)
}

// seedInterrupted writes the staging and journal a previous run would leave.
func (h *harness) seedInterrupted(prefix []byte, etag string) string {
	h.t.Helper()
	id := "job-00000000000000a1"
	path := h.stagingPath(id, fixtureFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, prefix, 0o600); err != nil {
		h.t.Fatal(err)
	}
	entry := journal{
		SchemaVersion: journalSchema, JobID: id, Kind: KindDownload, BundleID: h.bundle.BundleID,
		ManifestSHA256: h.bundle.ManifestSHA256, State: StateDownloading, StagingID: id,
		Artifacts: []journalArtifact{{ArtifactID: h.bundle.Artifacts[0].ArtifactID, Path: fixtureFile, ETag: etag}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.root, "state", "jobs"), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, filepath.FromSlash(journalPath(id))), data, 0o600); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) resume(id string) {
	h.t.Helper()
	if err := h.manager.Resume(id); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) assertInstalled(data []byte) {
	h.t.Helper()
	got, err := os.ReadFile(h.installedPath(fixtureFile))
	if err != nil {
		h.t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		h.t.Fatal("installed bytes differ")
	}
	list := h.manager.ListInstallations()
	if len(list.Installations) != 1 || list.Installations[0].Status != string(StatusVerified) {
		h.t.Fatalf("installations = %+v", list.Installations)
	}
}

func (h *harness) assertNotInstalled() {
	h.t.Helper()
	if list := h.manager.ListInstallations(); len(list.Installations) != 0 {
		h.t.Fatalf("unexpected installation: %+v", list.Installations)
	}
	if _, err := os.Stat(h.installedPath(fixtureFile)); !os.IsNotExist(err) {
		h.t.Fatalf("final file exists: %v", err)
	}
}

func stagedSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return -1
	}
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
