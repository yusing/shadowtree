//go:build linux

package runner

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"

	"golang.org/x/sys/unix"
)

func TestCreateOverlayWorkspaceHidesSkippedFiles(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "HEAD"), []byte("ref: main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".shadowtree.toml"), []byte("profile = \"go\""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(source, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	workspace := filepath.Join(workDir, "workspace")

	sandbox, err := createOverlayWorkspace(t.Context(), source, workDir, workspace)
	if err != nil {
		t.Skipf("overlayfs unavailable: %v", err)
	}
	defer func() {
		if err := sandbox.Close(); err != nil {
			t.Fatalf("close overlay workspace: %v", err)
		}
	}()
	err = sandbox.runNamespaceCommand(
		t.Context(),
		os.Environ(),
		sandbox.target,
		[]string{"sh", "-c", "test -f file.txt && test ! -e .git && test ! -e .shadowtree.toml && test ! -e pipe"},
		nil,
		io.Discard,
		io.Discard,
	)
	if err != nil {
		t.Fatalf("namespace overlay view: %v", err)
	}
}

func TestNamespaceCommandUsesStableSourceCWD(t *testing.T) {
	source := t.TempDir()
	workDir := t.TempDir()
	sandbox, err := createOverlayWorkspace(t.Context(), source, workDir, filepath.Join(workDir, "workspace"))
	if err != nil {
		t.Skipf("overlayfs unavailable: %v", err)
	}
	defer func() {
		if err := sandbox.Close(); err != nil {
			t.Fatalf("close overlay workspace: %v", err)
		}
	}()

	var stdout bytes.Buffer
	err = sandbox.runNamespaceCommand(
		t.Context(),
		os.Environ(),
		sandbox.target,
		[]string{"pwd"},
		nil,
		&stdout,
		io.Discard,
	)
	if err != nil {
		t.Fatalf("namespace pwd: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != source {
		t.Fatalf("pwd = %q, want %q", got, source)
	}
}

func TestNamespaceOverlayPreservesGoTestCache(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not available")
	}
	source := t.TempDir()
	cache := filepath.Join(t.TempDir(), "go-build")
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module cache.example\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "cache_test.go"), []byte("package cache\n\nimport \"testing\"\n\nfunc TestCache(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("test", recipe.Recipe{
		Cmd: recipe.Command{"go", "test", "{pkg}", "{@}"},
		Arguments: map[string]recipe.Argument{
			"pkg": {Type: "rel_path", Default: "./..."},
		},
		Env: map[string]string{"GOCACHE": cache},
	}, nil, nil, nil, "", recipe.GoProfile)
	if err != nil {
		t.Fatal(err)
	}

	var firstOut, firstErr bytes.Buffer
	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source, Stdout: &firstOut, Stderr: &firstErr}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(firstErr.String(), "overlayfs unavailable") {
		t.Skipf("overlayfs unavailable: %s", firstErr.String())
	}
	var secondOut, secondErr bytes.Buffer
	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source, Stdout: &secondOut, Stderr: &secondErr}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(secondErr.String(), "overlayfs unavailable") {
		t.Skipf("overlayfs unavailable: %s", secondErr.String())
	}
	if !strings.Contains(secondOut.String(), "(cached)") {
		t.Fatalf("second go test output missing cache marker:\nstdout:\n%s\nstderr:\n%s\nfirst stdout:\n%s\nfirst stderr:\n%s", secondOut.String(), secondErr.String(), firstOut.String(), firstErr.String())
	}
}

func TestOverlayForEachFilesystemBuiltinRunsInNamespace(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/app\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "cmd", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "cmd", "app", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "dev-data", "certs"), 0o755); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(source, "dev-data", "certs", "debian-12.pve:8890.zip")
	if err := os.WriteFile(unreadable, []byte("fixture"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	sandbox, err := createOverlayWorkspace(t.Context(), source, workDir, filepath.Join(workDir, "workspace"))
	if err != nil {
		t.Skipf("overlayfs unavailable: %v", err)
	}
	defer func() {
		if err := sandbox.Close(); err != nil {
			t.Fatalf("close overlay workspace: %v", err)
		}
	}()
	resolved, err := recipe.Resolve("deadcode", recipe.Recipe{
		ForEach: recipe.ScriptCommand("@go-main-packages"),
		Workdir: "{item}",
		Cmd:     recipe.Command{"true"},
	}, nil, nil, nil, "", recipe.GoProfile)
	if err != nil {
		t.Fatal(err)
	}

	values, err := forEachItems(t.Context(), sandbox, sandbox.root, os.Environ(), Options{Resolved: resolved, SourceDir: source}, io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, value := range values {
		if value.Value == "./cmd/app" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("values = %#v, want ./cmd/app", values)
	}
}

func TestApplyOverlayUpperAppliesWhiteoutAndOpaqueDir(t *testing.T) {
	upper := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "deleted.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dst, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dst, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "dir", "hidden.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	whiteout := filepath.Join(upper, "deleted.txt")
	if err := os.WriteFile(whiteout, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setOverlayXattr(t, whiteout, "whiteout")
	upperDir := filepath.Join(upper, "dir")
	if err := os.Mkdir(upperDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setOverlayXattr(t, upperDir, "opaque")
	if err := os.WriteFile(filepath.Join(upperDir, "new.txt"), []byte("shadow"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := applyOverlayUpper(upper, dst, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "deleted.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted.txt err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "dir", "hidden.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hidden.txt err = %v, want not exist", err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "dir", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shadow" {
		t.Fatalf("new.txt = %q", data)
	}
	info, err := os.Stat(filepath.Join(dst, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("dir mode = %v, want 0755", got)
	}
}

func TestApplyOverlayUpperExcludesSeededWhiteouts(t *testing.T) {
	upper := t.TempDir()
	dst := t.TempDir()
	if err := os.Mkdir(filepath.Join(upper, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upper, ".git", "config"), []byte("shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dst, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, ".git", "config"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := applyOverlayUpper(upper, dst, map[string]struct{}{".git": {}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dst, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "host" {
		t.Fatalf(".git/config = %q", data)
	}
}

func TestApplyOverlayUpperSkipsUnsupportedFileType(t *testing.T) {
	upper := t.TempDir()
	dst := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(upper, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "pipe"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := applyOverlayUpper(upper, dst, nil); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dst, "pipe"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pipe err = %v, want not exist", err)
	}
}

func TestOverlaySyncRootMaterializesRequestedWhiteout(t *testing.T) {
	source := t.TempDir()
	upper := t.TempDir()
	lower := filepath.Join(source, "deleted.txt")
	if err := os.WriteFile(lower, []byte("host"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lower, 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "kept.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	whiteout := filepath.Join(upper, "deleted.txt")
	if err := os.WriteFile(whiteout, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setOverlayXattr(t, whiteout, "whiteout")
	sandbox := newOverlaySandboxForTest(t, source, upper)

	syncRoot, cleanup, err := sandbox.SyncRoot([]string{"deleted.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(syncRoot, "deleted.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted.txt err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(syncRoot, "kept.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kept.txt err = %v, want not exist", err)
	}
}

func TestOverlaySyncRootMaterializesRequestedOpaqueAncestor(t *testing.T) {
	source := t.TempDir()
	upper := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(source, "dir", "hidden.txt")
	if err := os.WriteFile(hidden, []byte("host"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hidden, 0o000); err != nil {
		t.Fatal(err)
	}
	upperDir := filepath.Join(upper, "dir")
	if err := os.Mkdir(upperDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setOverlayXattr(t, upperDir, "opaque")
	sandbox := newOverlaySandboxForTest(t, source, upper)

	syncRoot, cleanup, err := sandbox.SyncRoot([]string{"dir/hidden.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(syncRoot, "dir", "hidden.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dir/hidden.txt err = %v, want not exist", err)
	}
}

func TestSeededWhiteoutPreservesParentMode(t *testing.T) {
	source := t.TempDir()
	upper := t.TempDir()
	dst := t.TempDir()
	sourceDir := filepath.Join(source, "dir")
	dstDir := filepath.Join(dst, "dir")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(sourceDir, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if _, err := createSkipWhiteouts(source, upper); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dstDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dstDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := applyOverlayUpper(upper, dst, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", got)
	}
}

func TestCopyTreeSkipsUnsupportedFileType(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	copyDir := filepath.Join(dst, "copy")
	if err := CopyTree(src, copyDir); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(copyDir, "pipe"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pipe err = %v, want not exist", err)
	}
}

func TestNamespaceCommandPreservesEnvironment(t *testing.T) {
	source := t.TempDir()
	workDir := t.TempDir()
	sandbox, err := createOverlayWorkspace(t.Context(), source, workDir, filepath.Join(workDir, "workspace"))
	if err != nil {
		t.Skipf("overlayfs unavailable: %v", err)
	}
	defer func() {
		if err := sandbox.Close(); err != nil {
			t.Fatalf("close overlay workspace: %v", err)
		}
	}()

	err = sandbox.runNamespaceCommand(
		t.Context(),
		append(os.Environ(), "SHADOWTREE_OVERLAY_DEBUG=1"),
		sandbox.target,
		[]string{"sh", "-c", `test "$SHADOWTREE_OVERLAY_DEBUG" = 1`},
		nil,
		io.Discard,
		io.Discard,
	)
	if err != nil {
		t.Fatalf("namespace command env: %v", err)
	}
}

func setOverlayXattr(t *testing.T, path, name string) {
	t.Helper()
	if err := unix.Setxattr(path, "user.overlay."+name, []byte("y"), 0); err != nil {
		t.Skipf("overlay xattr unavailable: %v", err)
	}
}
