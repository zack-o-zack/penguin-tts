package models

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

const (
	crashEnvRoot     = "PENGUIN_CRASH_ROOT"
	crashEnvURL      = "PENGUIN_CRASH_URL"
	crashEnvPoint    = "PENGUIN_CRASH_POINT"
	lockedExitCode   = 3
	crashInsecureTLS = true
)

func killSelf() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	time.Sleep(time.Minute)
}

// TestCrashHelper is re-executed as a child process and dies by SIGKILL at the
// requested point, so recovery is exercised against real process death.
func TestCrashHelper(t *testing.T) {
	root := os.Getenv(crashEnvRoot)
	if root == "" {
		t.Skip("helper process only")
	}
	point := os.Getenv(crashEnvPoint)
	loaded := fixtureCatalog(t, fixtureDescriptor(os.Getenv(crashEnvURL), fixtureBundle, "1", single(payload)))
	hooks := defaultHooks()
	hooks.boundary = func(name string) {
		if name == point {
			killSelf()
		}
	}
	if point == "during-write" {
		write := hooks.write
		hooks.write = func(file *os.File, data []byte) (int, error) {
			if file.Name() == fixtureFile {
				_, _ = write(file, data[:len(data)/2])
				killSelf()
			}
			return write(file, data)
		}
	}
	manager, err := open(context.Background(), Options{
		Root:      root,
		Catalog:   loaded,
		Transport: insecureTransport(),
	}, testControls{hooks: &hooks, delay: noDelay, freeSpace: plentyOfSpace})
	if point == "second-writer" {
		var failure *Error
		if errors.As(err, &failure) && failure.Code == CodeStorageLocked {
			os.Exit(lockedExitCode)
		}
		os.Exit(1)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnqueueDownload(fixtureBundle); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Minute)
	t.Fatal("helper was not killed")
}

func insecureTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig.InsecureSkipVerify = crashInsecureTLS
	return transport
}

func runHelper(t *testing.T, h *harness, point string) *exec.ExitError {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashHelper$")
	command.Env = append(os.Environ(), crashEnvRoot+"="+h.root, crashEnvURL+"="+h.server.URL, crashEnvPoint+"="+point)
	err := command.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("helper ended unexpectedly: %v", err)
	}
	return exit
}

func TestProcessKillRecovery(t *testing.T) {
	cases := []struct {
		point     string
		installed bool
	}{
		{"during-write", false},
		{"files-synced", false},
		{"sealed", false},
		{"renamed", true},
		{"registered", true},
	}
	for _, test := range cases {
		t.Run(test.point, func(t *testing.T) {
			h := newHarness(t, single(payload), rangeAwareFile(payload))
			exit := runHelper(t, h, test.point)
			if status, ok := exit.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGKILL {
				t.Fatalf("helper did not die by SIGKILL: %v", exit)
			}
			requests := h.log.count()
			h.open()
			time.Sleep(20 * time.Millisecond)
			if h.log.count() != requests {
				t.Fatal("recovery used the network")
			}
			jobs := h.manager.ListJobs().Jobs
			if test.installed {
				h.assertInstalled(payload)
				if len(jobs) != 0 {
					t.Fatalf("job left behind: %+v", jobs)
				}
				return
			}
			h.assertNotInstalled()
			if len(jobs) != 1 || jobs[0].State != string(StateInterrupted) {
				t.Fatalf("jobs = %+v", jobs)
			}
			if !contains(jobs[0].Actions, "resume") || !contains(jobs[0].Actions, "cancel") {
				t.Fatalf("actions = %v", jobs[0].Actions)
			}
			h.resume(jobs[0].JobID)
			h.waitState(jobs[0].JobID, StateInstalled)
			h.assertInstalled(payload)
		})
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func rangeAwareFile(data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", savedETag)
		if header := r.Header.Get("Range"); header != "" {
			var start int
			_, _ = fmt.Sscanf(header, "bytes=%d-", &start)
			if start >= len(data) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(data)))
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
			w.Header().Set("Content-Length", fmt.Sprint(len(data)-start))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[start:])
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data)
	}
}

func TestRepairKilledAfterRenameIsAdopted(t *testing.T) {
	h := newHarness(t, single(payload), rangeAwareFile(payload))
	h.open()
	h.waitState(h.download(), StateInstalled)
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	must(t, os.Truncate(h.installedPath(fixtureFile), 10))
	runHelper(t, h, "renamed")
	requests := h.log.count()
	h.open()
	h.assertInstalled(payload)
	if jobs := h.manager.ListJobs().Jobs; len(jobs) != 0 {
		t.Fatalf("job left behind: %+v", jobs)
	}
	if h.log.count() != requests {
		t.Fatal("recovery used the network")
	}
}

func TestSecondProcessCannotWrite(t *testing.T) {
	h := newHarness(t, single(payload), rangeAwareFile(payload))
	h.open()
	exit := runHelper(t, h, "second-writer")
	if exit.ExitCode() != lockedExitCode {
		t.Fatalf("exit = %d", exit.ExitCode())
	}
}
