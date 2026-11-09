//go:build e2e

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"penguin.local/app/internal/catalog"
	"penguin.local/app/internal/models"
)

const (
	e2eRevision  = "0123456789abcdef0123456789abcdef01234567"
	e2eFile      = "model.bin"
	e2eChunk     = 8 << 10
	e2eChunkWait = 150 * time.Millisecond
	e2eETag      = `"e2e-fixture"`
)

var (
	e2ePayload  = bytes.Repeat([]byte("penguin-e2e-synthetic-bytes!"), 16<<10/28*16)
	e2eBrowsers = []string{"chromium", "webkit"}
	e2eJourneys = []string{"pause", "retry", "cancel", "reload", "reference", "copy", "relocate", "mismatch", "popup"}
)

// modelConfig isolates E2E runs: a throwaway data root, a synthetic catalog,
// and a local TLS origin. The native folder picker is replaced because server
// mode has no dialogs; everything behind it is the production service.
func modelConfig() models.Config {
	root := os.Getenv("PENGUIN_E2E_ROOT")
	if root == "" {
		log.Fatal("PENGUIN_E2E_ROOT is required")
	}
	if err := os.RemoveAll(root); err != nil {
		log.Fatal(err)
	}
	origin := newE2EOrigin()
	descriptors := []catalog.Descriptor{}
	for _, browser := range e2eBrowsers {
		for _, journey := range e2eJourneys {
			id := "e2e-" + browser + "-" + journey
			descriptors = append(descriptors, e2eDescriptor(origin.URL, id, "Synthetic "+browser+" "+journey, true))
			folder := filepath.Join(root, "external", id)
			if err := os.MkdirAll(folder, 0o755); err != nil {
				log.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(folder, e2eFile), e2ePayload, 0o644); err != nil {
				log.Fatal(err)
			}
			if journey == "mismatch" {
				corrupt := bytes.Clone(e2ePayload)
				corrupt[0] ^= 1
				if err := os.WriteFile(filepath.Join(folder, e2eFile), corrupt, 0o644); err != nil {
					log.Fatal(err)
				}
			}
		}
	}
	descriptors = append(descriptors, e2eDescriptor(origin.URL, "e2e-restricted", "Synthetic restricted", false))
	loaded := &catalog.Catalog{}
	for _, descriptor := range descriptors {
		bundle, err := catalog.NewBundle(descriptor)
		if err != nil {
			log.Fatal(err)
		}
		loaded.Bundles = append(loaded.Bundles, bundle)
	}
	return models.Config{
		Root:      filepath.Join(root, "data"),
		Catalog:   loaded,
		Transport: origin.Client().Transport,
		ChooseFolder: func(_, bundleID string) (string, error) {
			external := filepath.Join(root, "external", bundleID)
			if _, err := os.Stat(external); err == nil {
				return external, nil
			}
			return filepath.Join(root, "moved", bundleID), nil
		},
		RevealDirectory: func(string) error { return nil },
	}
}

func e2eDescriptor(origin, id, name string, approved bool) catalog.Descriptor {
	size := int64(len(e2ePayload))
	sum := sha256.Sum256(e2ePayload)
	license := "Apache-2.0"
	review := catalog.ReviewApproved
	if !approved {
		review = catalog.ReviewUnreviewed
	}
	return catalog.Descriptor{
		SchemaVersion:  catalog.SchemaVersion,
		CatalogVersion: "e2e",
		ModelID:        "synthetic",
		VariantID:      "fixture",
		BundleID:       id,
		VoiceIDs:       []string{},
		DisplayName:    name,
		Publisher:      "Penguin tests",
		Description:    "Synthetic bytes for end-to-end tests.",
		SourceURL:      origin + "/source",
		ModelCardURL:   origin + "/card",
		ReleaseLabel:   "fixture",
		Languages:      []string{"en-US"},
		Format:         "bin",
		Precision:      "none",
		Engine:         catalog.Engine{Status: catalog.EngineUnknown},
		Artifacts: []catalog.Artifact{{
			ArtifactID:     "model",
			Repository:     "synthetic/" + id,
			Revision:       e2eRevision,
			Path:           e2eFile,
			URL:            origin + "/" + id + "/resolve/" + e2eRevision + "/" + e2eFile,
			SizeBytes:      &size,
			SHA256:         hex.EncodeToString(sum[:]),
			HashSource:     "e2e-fixture",
			HashObservedAt: "2026-11-09",
		}},
		Licensing: []catalog.LicenseEvidence{{
			Kind: "model", Subject: name, SourceURL: origin + "/license", SourceRevision: e2eRevision,
			LicenseID: &license, DistributionReview: review,
		}},
		Space: catalog.Space{RequiredBytes: size, InstalledBytes: size},
	}
}

// newE2EOrigin serves the payload slowly enough for tests to act mid-transfer.
// The first three requests for a "retry" bundle fail, exhausting its budget.
func newE2EOrigin() *httptest.Server {
	var mu sync.Mutex
	failures := map[string]int{}
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if strings.HasSuffix(id, "-retry") {
			mu.Lock()
			failures[id]++
			failing := failures[id] <= 3
			mu.Unlock()
			if failing {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		start := 0
		w.Header().Set("ETag", e2eETag)
		status := http.StatusOK
		if header := r.Header.Get("Range"); header != "" && r.Header.Get("If-Range") == e2eETag {
			parsed, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(header, "bytes="), "-"))
			if err == nil && parsed < len(e2ePayload) {
				start = parsed
				status = http.StatusPartialContent
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(e2ePayload)-1, len(e2ePayload)))
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(e2ePayload)-start))
		w.WriteHeader(status)
		for offset := start; offset < len(e2ePayload); offset += e2eChunk {
			if _, err := w.Write(e2ePayload[offset:min(offset+e2eChunk, len(e2ePayload))]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(e2eChunkWait):
			}
		}
	}))
}
