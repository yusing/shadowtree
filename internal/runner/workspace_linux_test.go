//go:build linux

package runner

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

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

	sandbox, err := createOverlayWorkspace(source, workDir, workspace)
	if err != nil {
		t.Skipf("overlayfs unavailable: %v", err)
	}
	defer func() {
		if err := sandbox.Close(false); err != nil {
			t.Fatalf("close overlay workspace: %v", err)
		}
	}()
	err = sandbox.runNamespaceCommand(
		t.Context(),
		os.Environ(),
		[]string{"sh", "-c", "test -f file.txt && test ! -e .git && test ! -e .shadowtree.toml && test ! -e pipe"},
		nil,
		io.Discard,
		io.Discard,
	)
	if err != nil {
		t.Fatalf("namespace overlay view: %v", err)
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
	sandbox, err := createOverlayWorkspace(source, workDir, filepath.Join(workDir, "workspace"))
	if err != nil {
		t.Skipf("overlayfs unavailable: %v", err)
	}
	defer func() {
		if err := sandbox.Close(false); err != nil {
			t.Fatalf("close overlay workspace: %v", err)
		}
	}()

	err = sandbox.runNamespaceCommand(
		t.Context(),
		append(os.Environ(), "SHADOWTREE_OVERLAY_DEBUG=1"),
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
