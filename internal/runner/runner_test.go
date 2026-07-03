package runner

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
)

func TestRunDoesNotMutateSourceWithoutSyncOut(t *testing.T) {
	if os.Getenv("SHELL") == "" {
		t.Setenv("SHELL", "/bin/sh")
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("run", recipe.Recipe{Cmd: recipe.Command{"sh", "-c", "printf shadow > file.txt"}}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "host" {
		t.Fatalf("source mutated: %q", data)
	}
}

func TestRunUnsandboxedMutatesSourceWithoutSyncOut(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve(
		"run",
		recipe.Recipe{Cmd: recipe.Command{"sh", "-c", "printf shadow > file.txt"}, Sandboxed: new(false)},
		nil,
		nil,
		nil,
		"",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shadow" {
		t.Fatalf("source data = %q", data)
	}
}

func TestRunSyncsExplicitPath(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve("run", recipe.Recipe{Cmd: recipe.Command{"sh", "-c", "printf shadow > out.txt"}}, nil, []string{"out.txt"}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shadow" {
		t.Fatalf("synced data = %q", data)
	}
}

func TestPrintPlanHidesUnsandboxedSyncOut(t *testing.T) {
	resolved, err := recipe.Resolve(
		"tidy",
		recipe.Recipe{
			Cmd:       recipe.Command{"go", "mod", "tidy"},
			Sandboxed: new(false),
			SyncOut:   []string{"go.mod", "go.sum"},
		},
		nil,
		nil,
		nil,
		"",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	printPlan(&out, resolved)
	text := out.String()
	if !strings.Contains(text, "sandboxed: false") {
		t.Fatalf("plan output missing sandboxed marker:\n%s", text)
	}
	if strings.Contains(text, "sync_out:") {
		t.Fatalf("plan output shows ignored sync_out:\n%s", text)
	}
}
