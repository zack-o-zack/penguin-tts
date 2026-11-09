package models

import (
	"crypto/sha256"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func serviceOptions() application.ServiceOptions {
	return application.ServiceOptions{}
}

var importFiles = []fixtureArtifact{
	{path: "config.json", data: []byte(`{"sample_rate":24000}`)},
	{path: "onnx/model.onnx", data: []byte("synthetic model!")},
	{path: "voices/voice.bin", data: []byte("synthetic voice!")},
}

func newImportHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, importFiles, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h.open()
	return h
}

func writeBundleFolder(t *testing.T, dir string) string {
	t.Helper()
	for _, file := range importFiles {
		path := filepath.Join(dir, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func folderHashes(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	hashes := map[string][32]byte{}
	for _, file := range importFiles {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file.path)))
		if err != nil {
			t.Fatal(err)
		}
		hashes[file.path] = sha256.Sum256(data)
	}
	return hashes
}

func (h *harness) importFolder(folder string, mode ImportMode) string {
	h.t.Helper()
	result, err := h.manager.ImportFolder(fixtureBundle, folder, mode)
	if err != nil {
		h.t.Fatal(err)
	}
	if result.JobID == "" {
		h.t.Fatalf("no job: %+v", result)
	}
	return result.JobID
}

func TestPlainFolderContract(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*testing.T, string)
		wantCode Code
		artifact string
	}{
		{name: "complete", mutate: func(*testing.T, string) {}},
		{name: "missing companion", mutate: func(t *testing.T, dir string) { must(t, os.Remove(filepath.Join(dir, "config.json"))) }, wantCode: CodeMissingArtifact, artifact: "config.json"},
		{name: "same size corruption", mutate: func(t *testing.T, dir string) {
			must(t, os.WriteFile(filepath.Join(dir, "voices/voice.bin"), []byte("corrupted voice!"), 0o644))
		}, wantCode: CodeHashMismatch, artifact: "voices/voice.bin"},
		{name: "truncated", mutate: func(t *testing.T, dir string) { must(t, os.Truncate(filepath.Join(dir, "onnx/model.onnx"), 4)) }, wantCode: CodeSizeMismatch, artifact: "onnx/model.onnx"},
		{name: "extra metadata ignored", mutate: func(t *testing.T, dir string) {
			must(t, os.MkdirAll(filepath.Join(dir, ".cache/huggingface"), 0o755))
			must(t, os.WriteFile(filepath.Join(dir, ".cache/huggingface/meta"), []byte("x"), 0o644))
		}},
		{name: "directory instead of file", mutate: func(t *testing.T, dir string) {
			must(t, os.Remove(filepath.Join(dir, "config.json")))
			must(t, os.Mkdir(filepath.Join(dir, "config.json"), 0o755))
		}, wantCode: CodeUnsafePath, artifact: "config.json"},
		{name: "file symlink", mutate: func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "real")
			must(t, os.Rename(filepath.Join(dir, "config.json"), target))
			must(t, os.Symlink(target, filepath.Join(dir, "config.json")))
		}, wantCode: CodeUnsafePath, artifact: "config.json"},
		{name: "ancestor symlink", mutate: func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "real")
			must(t, os.Rename(filepath.Join(dir, "onnx"), target))
			must(t, os.Symlink(target, filepath.Join(dir, "onnx")))
		}, wantCode: CodeUnsafePath, artifact: "onnx/model.onnx"},
		{name: "fifo does not block", mutate: func(t *testing.T, dir string) {
			must(t, os.Remove(filepath.Join(dir, "config.json")))
			must(t, syscall.Mkfifo(filepath.Join(dir, "config.json"), 0o644))
		}, wantCode: CodeUnsafePath, artifact: "config.json"},
		{name: "hard links are ordinary files", mutate: func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "linked")
			must(t, os.Link(filepath.Join(dir, "config.json"), target))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			h := newImportHarness(t)
			folder := writeBundleFolder(t, filepath.Join(t.TempDir(), "My Models café"))
			test.mutate(t, folder)
			id := h.importFolder(folder, ImportReference)
			snapshot := h.waitState(id, StateInstalled, StateFailed)
			list := h.manager.ListInstallations()
			if test.wantCode == "" {
				if snapshot.State != string(StateInstalled) || len(list.Installations) != 1 {
					t.Fatalf("job = %+v installs = %+v", snapshot, list.Installations)
				}
				install := list.Installations[0]
				if install.Ownership != string(OwnershipExternal) || install.Status != string(StatusVerified) || install.Location != folder {
					t.Fatalf("install = %+v", install)
				}
				return
			}
			if snapshot.ErrorCode != string(test.wantCode) || snapshot.ErrorArtifact != test.artifact {
				t.Fatalf("job = %+v", snapshot)
			}
			if len(list.Installations) != 0 {
				t.Fatalf("unverified folder registered: %+v", list.Installations)
			}
			if h.log.count() != 0 {
				t.Fatal("import used the network")
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPermissionLossIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	h := newImportHarness(t)
	folder := writeBundleFolder(t, t.TempDir())
	config := filepath.Join(folder, "config.json")
	must(t, os.Chmod(config, 0))
	t.Cleanup(func() { _ = os.Chmod(config, 0o644) })
	snapshot := h.waitState(h.importFolder(folder, ImportReference), StateFailed)
	if snapshot.ErrorCode != string(CodeAccessDenied) {
		t.Fatalf("job = %+v", snapshot)
	}
}

func TestMutationDuringVerificationIsRejected(t *testing.T) {
	h := newHarness(t, importFiles, func(http.ResponseWriter, *http.Request) {})
	folder := writeBundleFolder(t, t.TempDir())
	h.controls.hashing = func(artifact string) {
		if artifact == "onnx/model.onnx" {
			must(t, os.Chtimes(filepath.Join(folder, "voices/voice.bin"), time.Now().Add(time.Hour), time.Now().Add(time.Hour)))
		}
	}
	h.open()
	id := h.importFolder(folder, ImportReference)
	h.waitState(id, StateInstalled)
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	must(t, os.Chtimes(filepath.Join(folder, "config.json"), time.Now().Add(2*time.Hour), time.Now().Add(2*time.Hour)))
	h.controls.hashing = nil
	h.open()
	install := h.manager.ListInstallations().Installations[0]
	if install.Status != string(StatusChanged) || install.ErrorCode != string(CodeSourceChanged) {
		t.Fatalf("metadata change was not an invalidation hint: %+v", install)
	}
	if err := h.manager.Revalidate(t.Context(), install.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.manager.ListInstallations().Installations[0]; got.Status != string(StatusVerified) {
		t.Fatalf("full verification did not restore status: %+v", got)
	}
}

func TestMovedReferenceLocateAgain(t *testing.T) {
	h := newImportHarness(t)
	base := t.TempDir()
	original := writeBundleFolder(t, filepath.Join(base, "original"))
	h.waitState(h.importFolder(original, ImportReference), StateInstalled)
	install := h.manager.ListInstallations().Installations[0]

	moved := filepath.Join(base, "moved")
	must(t, os.Rename(original, moved))
	h.reopen()
	got := h.manager.ListInstallations().Installations[0]
	if got.Status != string(StatusUnavailable) || got.ErrorCode != string(CodeLocationUnavailable) || got.ID != install.ID {
		t.Fatalf("install = %+v", got)
	}

	wrong := writeBundleFolder(t, filepath.Join(base, "wrong"))
	must(t, os.WriteFile(filepath.Join(wrong, "config.json"), []byte("{}"), 0o644))
	result, err := h.manager.LocateAgain(install.ID, wrong)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := h.waitState(result.JobID, StateFailed); snapshot.ErrorCode != string(CodeSizeMismatch) {
		t.Fatalf("job = %+v", snapshot)
	}
	if got := h.manager.ListInstallations().Installations[0]; got.Location != original {
		t.Fatalf("failed relocation changed the record: %+v", got)
	}

	retry, err := h.manager.LocateAgain(install.ID, moved)
	if err != nil {
		t.Fatal(err)
	}
	if retry.JobID != result.JobID {
		t.Fatalf("stopped job was not retargeted: %s", retry.JobID)
	}
	h.waitState(retry.JobID, StateInstalled)
	got = h.manager.ListInstallations().Installations[0]
	if got.Location != moved || got.Status != string(StatusVerified) || got.ErrorCode != "" || got.ID != install.ID {
		t.Fatalf("install = %+v", got)
	}
}

func TestStaleRevalidationCannotOverwriteRelocation(t *testing.T) {
	h := newHarness(t, importFiles, func(http.ResponseWriter, *http.Request) {})
	base := t.TempDir()
	original := writeBundleFolder(t, filepath.Join(base, "original"))
	replacement := writeBundleFolder(t, filepath.Join(base, "replacement"))
	reached := make(chan struct{})
	proceed := make(chan struct{})
	var armed atomic.Bool
	h.controls.hashing = func(string) {
		if armed.CompareAndSwap(true, false) {
			close(reached)
			<-proceed
		}
	}
	h.open()
	h.waitState(h.importFolder(original, ImportReference), StateInstalled)
	install := h.manager.ListInstallations().Installations[0]

	armed.Store(true)
	checked := make(chan error, 1)
	go func() { checked <- h.manager.Revalidate(t.Context(), install.ID) }()
	waitSignal(t, reached)
	result, err := h.manager.LocateAgain(install.ID, replacement)
	must(t, err)
	h.waitState(result.JobID, StateInstalled)
	must(t, os.RemoveAll(original))
	close(proceed)
	must(t, <-checked)

	got := h.manager.ListInstallations().Installations[0]
	if got.Location != replacement || got.Status != string(StatusVerified) {
		t.Fatalf("stale check overwrote relocation: %+v", got)
	}
}

func TestRemovingExternalKeepsSourceBytes(t *testing.T) {
	h := newImportHarness(t)
	folder := writeBundleFolder(t, t.TempDir())
	before := folderHashes(t, folder)
	h.waitState(h.importFolder(folder, ImportReference), StateInstalled)
	install := h.manager.ListInstallations().Installations[0]
	if err := h.manager.RemoveInstallation(install.ID); err != nil {
		t.Fatal(err)
	}
	if list := h.manager.ListInstallations(); len(list.Installations) != 0 {
		t.Fatalf("installs = %+v", list.Installations)
	}
	after := folderHashes(t, folder)
	for artifact, hash := range before {
		if after[artifact] != hash {
			t.Fatalf("%s changed", artifact)
		}
	}
}

func TestManagedCopyIsIndependentAndRemovalConfined(t *testing.T) {
	h := newImportHarness(t)
	source := writeBundleFolder(t, t.TempDir())
	before := folderHashes(t, source)
	must(t, os.WriteFile(filepath.Join(source, "script.py"), []byte("raise SystemExit"), 0o644))
	h.waitState(h.importFolder(source, ImportCopy), StateInstalled)
	install := h.manager.ListInstallations().Installations[0]
	if install.Ownership != string(OwnershipManaged) {
		t.Fatalf("install = %+v", install)
	}
	for artifact, hash := range before {
		if folderHashes(t, source)[artifact] != hash {
			t.Fatalf("source %s changed", artifact)
		}
	}
	if _, err := os.Stat(filepath.Join(install.Location, filesDir, "script.py")); !os.IsNotExist(err) {
		t.Fatalf("undeclared file copied: %v", err)
	}
	must(t, os.RemoveAll(source))
	if err := h.manager.Revalidate(t.Context(), install.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.manager.ListInstallations().Installations[0]; got.Status != string(StatusVerified) {
		t.Fatalf("copy depended on its source: %+v", got)
	}

	sibling := filepath.Join(h.root, "models", "installs", "keep.txt")
	must(t, os.WriteFile(sibling, []byte("keep"), 0o600))
	if err := h.manager.RemoveInstallation(install.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(install.Location); !os.IsNotExist(err) {
		t.Fatalf("managed files remain: %v", err)
	}
	if data, err := os.ReadFile(sibling); err != nil || string(data) != "keep" {
		t.Fatalf("removal escaped the install: %v", err)
	}
}

func TestFailedCopyLeavesSourceAndRegistersNothing(t *testing.T) {
	h := newImportHarness(t)
	source := writeBundleFolder(t, t.TempDir())
	must(t, os.Truncate(filepath.Join(source, "voices/voice.bin"), 3))
	id := h.importFolder(source, ImportCopy)
	if snapshot := h.waitState(id, StateFailed); snapshot.ErrorCode != string(CodeSizeMismatch) {
		t.Fatalf("job = %+v", snapshot)
	}
	if list := h.manager.ListInstallations(); len(list.Installations) != 0 {
		t.Fatalf("installs = %+v", list.Installations)
	}
	if info, err := os.Stat(filepath.Join(source, "voices/voice.bin")); err != nil || info.Size() != 3 {
		t.Fatalf("source changed: %v", err)
	}
}

func TestCopyRejectsSourceChangedAfterCopy(t *testing.T) {
	h := newHarness(t, importFiles, func(http.ResponseWriter, *http.Request) {})
	source := writeBundleFolder(t, t.TempDir())
	h.open()
	// Copying corrupt bytes must fail at the destination hash.
	must(t, os.WriteFile(filepath.Join(source, "onnx/model.onnx"), []byte("corrupted model!"), 0o644))
	snapshot := h.waitState(h.importFolder(source, ImportCopy), StateFailed)
	if snapshot.ErrorCode != string(CodeHashMismatch) || snapshot.ErrorArtifact != "onnx/model.onnx" {
		t.Fatalf("job = %+v", snapshot)
	}
}

func TestCancelDuringImport(t *testing.T) {
	h := newHarness(t, importFiles, func(http.ResponseWriter, *http.Request) {})
	reached := make(chan struct{})
	proceed := make(chan struct{})
	h.controls.hashing = func(string) {
		select {
		case <-reached:
		default:
			close(reached)
			<-proceed
		}
	}
	h.open()
	folder := writeBundleFolder(t, t.TempDir())
	id := h.importFolder(folder, ImportReference)
	waitSignal(t, reached)
	if err := h.manager.Cancel(id); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	h.waitGone(id)
	if list := h.manager.ListInstallations(); len(list.Installations) != 0 {
		t.Fatalf("canceled import registered: %+v", list.Installations)
	}
}

func TestDuplicateImportReturnsExistingInstall(t *testing.T) {
	h := newImportHarness(t)
	folder := writeBundleFolder(t, t.TempDir())
	h.waitState(h.importFolder(folder, ImportReference), StateInstalled)
	result, err := h.manager.ImportFolder(fixtureBundle, folder, ImportReference)
	if err != nil {
		t.Fatal(err)
	}
	if result.InstallationID == "" || result.JobID != "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRejectsFolderInsideManagedStorage(t *testing.T) {
	h := newImportHarness(t)
	_, err := h.manager.ImportFolder(fixtureBundle, filepath.Join(h.root, "models"), ImportReference)
	if asError(err).Code != CodeUnsafePath {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorsNameKnownArtifactsOnly(t *testing.T) {
	h := newImportHarness(t)
	folder := writeBundleFolder(t, t.TempDir())
	must(t, os.WriteFile(filepath.Join(folder, "private-notes.txt"), []byte("x"), 0o644))
	must(t, os.Remove(filepath.Join(folder, "config.json")))
	snapshot := h.waitState(h.importFolder(folder, ImportReference), StateFailed)
	if strings.Contains(snapshot.ErrorMessage, "private-notes") || !strings.Contains(snapshot.ErrorMessage, "config.json") {
		t.Fatalf("message = %q", snapshot.ErrorMessage)
	}
}

func TestServiceReportsLockedStorage(t *testing.T) {
	h := newImportHarness(t)
	service := NewService(Config{Root: h.root, Catalog: h.catalog})
	if err := service.ServiceStartup(t.Context(), serviceOptions()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListJobs(); asError(err).Code != CodeStorageLocked {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceFolderDismissalIsNotAnError(t *testing.T) {
	h := newHarness(t, importFiles, func(http.ResponseWriter, *http.Request) {})
	folder := ""
	service := NewService(Config{
		Root:         h.root,
		Catalog:      h.catalog,
		ChooseFolder: func(string, string) (string, error) { return folder, nil },
	})
	must(t, service.ServiceStartup(t.Context(), serviceOptions()))
	t.Cleanup(func() { _ = service.ServiceShutdown() })
	result, err := service.ImportFolder(fixtureBundle, string(ImportReference))
	if err != nil || result != (EnqueueResult{}) {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	folder = writeBundleFolder(t, t.TempDir())
	result, err = service.ImportFolder(fixtureBundle, string(ImportReference))
	if err != nil || result.JobID == "" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}
