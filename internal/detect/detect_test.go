package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfileDetectsNodePackage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"scripts":{"test":"vitest"}}`)

	if got := Profile(dir); got != NodeProfile {
		t.Fatalf("Profile() = %q, want %q", got, NodeProfile)
	}
}

func TestProfileDetectsGoModule(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n")

	if got := Profile(dir); got != GoProfile {
		t.Fatalf("Profile() = %q, want %q", got, GoProfile)
	}
}

func TestProfileUsesNearestMarker(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/app\n")
	app := filepath.Join(root, "web", "src")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "web", "package.json"), "{}")

	if got := Profile(app); got != NodeProfile {
		t.Fatalf("Profile() = %q, want %q", got, NodeProfile)
	}
}

func TestProfileUsesGoForSameDirectoryTie(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n")
	writeFile(t, filepath.Join(dir, "package.json"), "{}")

	if got := Profile(dir); got != GoProfile {
		t.Fatalf("Profile() = %q, want %q", got, GoProfile)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
