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

func TestRepoRootFindsNearestGitMarker(t *testing.T) {
	tests := []struct {
		name       string
		makeMarker func(*testing.T, string)
	}{
		{
			name: "directory marker",
			makeMarker: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "opaque file marker",
			makeMarker: func(t *testing.T, path string) {
				t.Helper()
				writeFile(t, path, "unknown future or malformed Git metadata\n")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.makeMarker(t, filepath.Join(root, ".git"))
			cwd := filepath.Join(root, "nested", "dir")
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				t.Fatal(err)
			}

			if got := RepoRoot(cwd); got != root {
				t.Fatalf("RepoRoot() = %q, want %q", got, root)
			}
		})
	}
}

func TestRepoRootReturnsEmptyWithoutGitMarker(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	if exists(filepath.Join(root, ".git")) {
		t.Skipf("filesystem root %q contains a .git marker", root)
	}

	if got := RepoRoot(root); got != "" {
		t.Fatalf("RepoRoot() = %q, want no repository", got)
	}
}

func TestRepoRootIgnoresUnrelatedGitNames(t *testing.T) {
	root := t.TempDir()
	want := RepoRoot(root)
	cwd := filepath.Join(root, "nested")
	for _, name := range []string{".github", ".gitignore", ".gitmodules"} {
		writeFile(t, filepath.Join(cwd, name), "unrelated\n")
	}

	if got := RepoRoot(cwd); got != want {
		t.Fatalf("RepoRoot() = %q, want unchanged root %q", got, want)
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
