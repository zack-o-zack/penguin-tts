package models

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type transferFixture struct {
	name, mode, etag string
	prefix           int
	corruptPrefix    bool
	wantInstalled    bool
	wantCode         Code
	wantRetained     int64 // -1 means the staged file must be gone.
	wantGets         int
}

func fixtureHandler(f transferFixture) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/expired") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if f.mode == "expired" {
			http.Redirect(w, r, r.URL.Path+"/expired", http.StatusFound)
			return
		}
		w.Header().Set("ETag", savedETag)
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			if f.mode == "head405" {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			return
		}
		if f.mode == "416" || f.mode == "416-no-total" {
			if f.mode == "416" {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(payload)))
			}
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		body := bytes.Clone(payload)
		if f.mode == "corrupt" {
			body[0] ^= 1
		}
		if f.mode == "encoding" {
			w.Header().Set("Content-Encoding", "gzip")
		}
		status := http.StatusOK
		if rangeHeader := r.Header.Get("Range"); rangeHeader != "" && f.mode != "ignored" {
			start, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rangeHeader, "bytes="), "-"))
			total := len(payload)
			body = body[start:]
			shownStart := start
			if f.mode == "wrong-start" {
				shownStart++
			}
			if f.mode == "wrong-total" {
				total++
			}
			if f.mode != "missing-range" {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", shownStart, len(payload)-1, total))
			}
			if f.mode == "changed-etag" {
				w.Header().Set("ETag", `"fixture-v2"`)
			}
			if f.mode == "weak-etag" {
				w.Header().Set("ETag", `W/"fixture-v1"`)
			}
			status = http.StatusPartialContent
		}
		switch f.mode {
		case "chunked", "oversized":
			w.WriteHeader(status)
			w.(http.Flusher).Flush()
			if f.mode == "oversized" {
				body = append(body, 'x')
			}
		case "truncated":
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			body = body[:len(body)/2]
			w.WriteHeader(status)
		default:
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(status)
		}
		_, _ = w.Write(body)
	}
}

// The fixtures run through the service: seeded staging, recovery, Resume, and
// a terminal state. Unlike the prototype, a hash failure gets a clean retry.
func TestTransferFixtures(t *testing.T) {
	size := int64(len(payload))
	fixtures := []transferFixture{
		{name: "fresh", mode: "full", wantInstalled: true, wantGets: 1},
		{name: "valid-resume", prefix: 128, etag: savedETag, wantInstalled: true, wantGets: 1},
		{name: "range-ignored-200", mode: "ignored", prefix: 128, etag: savedETag, wantInstalled: true, wantGets: 1},
		{name: "wrong-range-start", mode: "wrong-start", prefix: 128, etag: savedETag, wantCode: CodeInvalidResponse, wantRetained: 128, wantGets: 1},
		{name: "missing-content-range", mode: "missing-range", prefix: 128, etag: savedETag, wantCode: CodeInvalidResponse, wantRetained: 128, wantGets: 1},
		{name: "changed-response-etag", mode: "changed-etag", prefix: 128, etag: savedETag, wantCode: CodeInvalidResponse, wantRetained: 128, wantGets: 1},
		{name: "weak-response-etag", mode: "weak-etag", prefix: 128, etag: savedETag, wantCode: CodeInvalidResponse, wantRetained: 128, wantGets: 1},
		{name: "weak-saved-etag", prefix: 128, etag: `W/"fixture-v1"`, wantInstalled: true, wantGets: 1},
		{name: "changed-range-total", mode: "wrong-total", prefix: 128, etag: savedETag, wantCode: CodeInvalidResponse, wantRetained: 128, wantGets: 1},
		{name: "complete-prefix-416", mode: "416", prefix: len(payload), etag: savedETag, wantInstalled: true, wantGets: 1},
		{name: "corrupt-complete-prefix", mode: "416", prefix: len(payload), etag: savedETag, corruptPrefix: true, wantCode: CodeInvalidResponse, wantRetained: 0, wantGets: 2},
		{name: "incomplete-prefix-416", mode: "416", prefix: 128, etag: savedETag, wantCode: CodeInvalidResponse, wantRetained: 128, wantGets: 1},
		{name: "416-without-total", mode: "416-no-total", prefix: 128, etag: savedETag, wantCode: CodeInvalidResponse, wantRetained: 128, wantGets: 1},
		{name: "head-not-supported", mode: "head405", prefix: 128, etag: savedETag, wantInstalled: true, wantGets: 1},
		{name: "unknown-length", mode: "chunked", wantInstalled: true, wantGets: 1},
		{name: "truncated-stream", mode: "truncated", wantCode: CodeNetwork, wantRetained: size - size/8, wantGets: 3},
		{name: "oversized-unknown-length", mode: "oversized", wantCode: CodeInvalidResponse, wantRetained: size, wantGets: 1},
		{name: "checksum-mismatch", mode: "corrupt", wantCode: CodeHashMismatch, wantRetained: -1, wantGets: 3},
		{name: "corrupt-resumed-prefix", prefix: 128, etag: savedETag, corruptPrefix: true, wantInstalled: true, wantGets: 2},
		{name: "unexpected-content-encoding", mode: "encoding", wantCode: CodeInvalidResponse, wantRetained: 0, wantGets: 1},
		{name: "expired-redirect", mode: "expired", wantCode: CodeForbidden, wantRetained: 0, wantGets: 4},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			h := newHarness(t, single(payload), fixtureHandler(f))
			var id string
			if f.prefix > 0 {
				prefix := bytes.Clone(payload[:f.prefix])
				if f.corruptPrefix {
					prefix[0] ^= 1
				}
				id = h.seedInterrupted(prefix, f.etag)
				h.open()
				if snapshot := h.waitState(id, StateInterrupted); snapshot.RetainedBytes != int64(f.prefix) {
					t.Fatalf("retained = %d", snapshot.RetainedBytes)
				}
				if h.log.count() != 0 {
					t.Fatal("recovery used the network")
				}
				h.resume(id)
			} else {
				h.open()
				id = h.download()
			}
			snapshot := h.waitState(id, StateInstalled, StateFailed)
			if f.wantInstalled {
				if snapshot.State != string(StateInstalled) {
					t.Fatalf("job = %+v", snapshot)
				}
				h.assertInstalled(payload)
			} else {
				if snapshot.State != string(StateFailed) || snapshot.ErrorCode != string(f.wantCode) {
					t.Fatalf("job = %+v", snapshot)
				}
				h.assertNotInstalled()
				staged := stagedSize(t, h.stagingPath(id, fixtureFile))
				if staged != f.wantRetained {
					t.Fatalf("retained %d bytes, want %d", staged, f.wantRetained)
				}
			}
			for _, method := range h.log.methods() {
				if method != http.MethodGet {
					t.Fatalf("unexpected %s request", method)
				}
			}
			if gets := h.log.count(); gets != f.wantGets {
				t.Fatalf("requests = %d, want %d", gets, f.wantGets)
			}
		})
	}
}

func TestResponseValidationBoundaries(t *testing.T) {
	size := len(payload)
	prefix := 128
	cases := map[string]func(http.ResponseWriter){
		"416 wrong total": func(w http.ResponseWriter) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size+1))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		},
		"range overflow": func(w http.ResponseWriter) {
			w.Header().Set("ETag", savedETag)
			w.Header().Set("Content-Range", "bytes 99999999999999999999-1/2")
			w.WriteHeader(http.StatusPartialContent)
		},
		"multipart": func(w http.ResponseWriter) {
			w.Header().Set("ETag", savedETag)
			w.Header().Set("Content-Type", "multipart/byteranges; boundary=x")
			w.WriteHeader(http.StatusPartialContent)
		},
		"short end bound": func(w http.ResponseWriter) {
			w.Header().Set("ETag", savedETag)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", prefix, size-2, size))
			w.WriteHeader(http.StatusPartialContent)
		},
		"misleading 206 length": func(w http.ResponseWriter) {
			w.Header().Set("ETag", savedETag)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", prefix, size-1, size))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("x"))
		},
	}
	for name, respond := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, single(payload), func(w http.ResponseWriter, _ *http.Request) { respond(w) })
			id := h.seedInterrupted(payload[:prefix], savedETag)
			h.open()
			h.waitState(id, StateInterrupted)
			h.resume(id)
			snapshot := h.waitState(id, StateFailed)
			if snapshot.ErrorCode != string(CodeInvalidResponse) {
				t.Fatalf("job = %+v", snapshot)
			}
			if got := stagedSize(t, h.stagingPath(id, fixtureFile)); got != int64(prefix) {
				t.Fatalf("prefix changed: %d", got)
			}
			if h.log.count() != 1 {
				t.Fatalf("malformed range retried: %d", h.log.count())
			}
		})
	}
}

func TestMisleadingFullLengthLeavesPrefix(t *testing.T) {
	h := newHarness(t, single(payload), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		_, _ = w.Write(payload[:10])
	})
	id := h.seedInterrupted(payload[:128], savedETag)
	h.open()
	h.waitState(id, StateInterrupted)
	h.resume(id)
	if snapshot := h.waitState(id, StateFailed); snapshot.ErrorCode != string(CodeInvalidResponse) {
		t.Fatalf("job = %+v", snapshot)
	}
	if got := stagedSize(t, h.stagingPath(id, fixtureFile)); got != 128 {
		t.Fatalf("prefix replaced before validation: %d", got)
	}
}

func TestNoValidatorRestartsWithoutRangeHeaders(t *testing.T) {
	var sawRange atomic.Bool
	h := newHarness(t, single(payload), func(w http.ResponseWriter, r *http.Request) {
		sawRange.Store(sawRange.Load() || r.Header.Get("Range") != "" || r.Header.Get("If-Range") != "")
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	})
	id := h.seedInterrupted(payload[:128], "")
	h.open()
	h.waitState(id, StateInterrupted)
	h.resume(id)
	h.waitState(id, StateInstalled)
	if sawRange.Load() {
		t.Fatal("range sent without a strong validator")
	}
	h.assertInstalled(payload)
}

func TestRequestsCarryIdentityEncodingAndNoCredentials(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	h.open()
	h.waitState(h.download(), StateInstalled)
	request := h.log.entries[0]
	if request.Header.Get("Accept-Encoding") != "identity" {
		t.Fatalf("encoding = %q", request.Header.Get("Accept-Encoding"))
	}
	if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
		t.Fatal("credentials were sent")
	}
}

func TestRedirectPolicy(t *testing.T) {
	t.Run("follows five redirects", func(t *testing.T) {
		var hops atomic.Int32
		h := newHarness(t, single(payload), func(w http.ResponseWriter, r *http.Request) {
			if hops.Add(1) <= maxRedirects {
				http.Redirect(w, r, r.URL.Path, http.StatusFound)
				return
			}
			serveFile(payload)(w, r)
		})
		h.open()
		h.waitState(h.download(), StateInstalled)
	})
	t.Run("refuses a sixth", func(t *testing.T) {
		h := newHarness(t, single(payload), func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, r.URL.Path, http.StatusFound)
		})
		h.open()
		snapshot := h.waitState(h.download(), StateFailed)
		if snapshot.ErrorCode != string(CodeInvalidResponse) || h.log.count() != maxRedirects+1 {
			t.Fatalf("job = %+v requests = %d", snapshot, h.log.count())
		}
	})
	t.Run("refuses HTTPS downgrade", func(t *testing.T) {
		var plainHits atomic.Int32
		plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { plainHits.Add(1) }))
		defer plain.Close()
		h := newHarness(t, single(payload), func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+"/signed?token=secret", http.StatusFound)
		})
		h.open()
		snapshot := h.waitState(h.download(), StateFailed)
		if snapshot.ErrorCode != string(CodeInvalidResponse) || plainHits.Load() != 0 {
			t.Fatalf("job = %+v plain hits = %d", snapshot, plainHits.Load())
		}
		if strings.Contains(snapshot.ErrorMessage, "secret") {
			t.Fatal("signed URL leaked into the error")
		}
	})
}

func TestUntrustedCertificateIsNotRetried(t *testing.T) {
	h := newHarness(t, single(payload), serveFile(payload))
	options := h.options()
	options.Transport = &http.Transport{}
	manager, err := open(t.Context(), options, h.controls)
	if err != nil {
		t.Fatal(err)
	}
	h.manager = manager
	t.Cleanup(func() { _ = manager.Close() })
	snapshot := h.waitState(h.download(), StateFailed)
	if snapshot.ErrorCode != string(CodeTLS) {
		t.Fatalf("job = %+v", snapshot)
	}
	if h.log.count() != 0 {
		t.Fatalf("requests = %d", h.log.count())
	}
}

func TestStalledBodyTimesOutAndRetainsPrefix(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	h := newHarness(t, single(payload), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", savedETag)
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload[:64])
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	h.controls.idle = 40 * time.Millisecond
	h.open()
	id := h.download()
	snapshot := h.waitState(id, StateFailed)
	if snapshot.ErrorCode != string(CodeNetwork) || h.log.count() != maxTransferAttempts {
		t.Fatalf("job = %+v requests = %d", snapshot, h.log.count())
	}
	if got := stagedSize(t, h.stagingPath(id, fixtureFile)); got <= 0 {
		t.Fatalf("prefix lost: %d", got)
	}
}

func TestStatusClassification(t *testing.T) {
	cases := []struct {
		status int
		code   Code
		gets   int
	}{
		{http.StatusRequestTimeout, CodeServer, 3},
		{http.StatusTooManyRequests, CodeServer, 3},
		{http.StatusInternalServerError, CodeServer, 3},
		{http.StatusBadGateway, CodeServer, 3},
		{http.StatusServiceUnavailable, CodeServer, 3},
		{http.StatusGatewayTimeout, CodeServer, 3},
		{http.StatusUnauthorized, CodeAuthRequired, 1},
		{http.StatusForbidden, CodeForbidden, 1},
		{http.StatusNotFound, CodeNotFound, 1},
		{http.StatusGone, CodeNotFound, 1},
		{http.StatusTeapot, CodeServer, 1},
	}
	for _, test := range cases {
		t.Run(strconv.Itoa(test.status), func(t *testing.T) {
			h := newHarness(t, single(payload), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(test.status) })
			h.open()
			snapshot := h.waitState(h.download(), StateFailed)
			if snapshot.ErrorCode != string(test.code) || h.log.count() != test.gets {
				t.Fatalf("job = %+v requests = %d", snapshot, h.log.count())
			}
		})
	}
}

func TestTransientFailureRecoversWithinBudget(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, single(payload), func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		serveFile(payload)(w, r)
	})
	h.open()
	h.waitState(h.download(), StateInstalled)
	h.assertInstalled(payload)
}

func TestRetryDelayPolicy(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	for attempt, base := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second} {
		delay := retryDelay(attempt, 0)
		if delay < base || delay >= base+maxRetryJitter {
			t.Fatalf("attempt %d delay = %v", attempt, delay)
		}
	}
	if got := retryDelay(1, 2*time.Minute); got != maxRetryAfter {
		t.Fatalf("Retry-After not capped: %v", got)
	}
	cases := map[string]time.Duration{
		"5":    5 * time.Second,
		"3600": maxRetryAfter,
		"0":    0,
		"-1":   0,
		"soon": 0,
		now.Add(10 * time.Second).Format(http.TimeFormat): 10 * time.Second,
		now.Add(time.Hour).Format(http.TimeFormat):        maxRetryAfter,
		now.Add(-time.Hour).Format(http.TimeFormat):       0,
	}
	for value, want := range cases {
		if got := parseRetryAfter(value, now); got != want {
			t.Fatalf("Retry-After %q = %v, want %v", value, got, want)
		}
	}
}

func TestErrorsDoNotExposeCauses(t *testing.T) {
	wrapped := newError(CodeNetwork, "model.bin", errors.New("https://cdn.test/file?signature=secret"))
	if strings.Contains(wrapped.Error(), "secret") {
		t.Fatal("cause leaked")
	}
	if _, err := os.Stat("/definitely/missing"); !errors.Is(storageError(CodeWriteFailed, "", err), os.ErrNotExist) {
		t.Fatal("cause is not retained for diagnostics")
	}
}
