package runner

import (
	"bytes"
	"context"
	"errors"
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

func TestRunWarnsAndFallsBackWhenOverlayUnavailable(t *testing.T) {
	original := newOverlayWorkspace
	newOverlayWorkspace = func(context.Context, string, string, string) (*sandboxWorkspace, error) {
		return nil, errors.New("forced overlay failure")
	}
	t.Cleanup(func() {
		newOverlayWorkspace = original
	})
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("run", recipe.Recipe{Cmd: recipe.Command{"sh", "-c", "printf shadow > file.txt"}}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source, Stderr: &stderr}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "host" {
		t.Fatalf("source mutated: %q", data)
	}
	if !strings.Contains(stderr.String(), "overlayfs unavailable (forced overlay failure); falling back to copied workspace") {
		t.Fatalf("stderr missing fallback warning:\n%s", stderr.String())
	}
}

func TestOverlaySyncRootMaterializesRequestedPaths(t *testing.T) {
	source := t.TempDir()
	upper := t.TempDir()
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upper, "file.txt"), []byte("shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	sandbox := &sandboxWorkspace{source: source, root: filepath.Join(workDir, "workspace"), upper: upper, workDir: workDir, overlay: true}

	syncRoot, cleanup, err := sandbox.SyncRoot([]string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(filepath.Join(syncRoot, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shadow" {
		t.Fatalf("materialized data = %q", data)
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

func TestRunInvokesRecipeReferenceDirectly(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.Command{"cat", "out.txt"},
			Pre:       []recipe.Command{{"@gen", "shadow"}},
			Sandboxed: new(false),
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

	var stdout bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   map[string]recipe.Recipe{"gen": {Cmd: recipe.Command{"sh", "-c", "printf %s \"$1\" > out.txt", "shadowtree"}}},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "shadow" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunInvokesStringRecipeReferenceWithBracketArguments(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.Command{"cat", "out.txt"},
			Pre:       []recipe.Command{recipe.ScriptCommand("@gen[value=shadow]")},
			Sandboxed: new(false),
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

	var stdout bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved: resolved,
		Recipes: map[string]recipe.Recipe{
			"gen": {
				Cmd:         recipe.Command{"sh", "-c", "printf %s \"$1\" > out.txt", "shadowtree"},
				DefaultArgs: []string{"{value}"},
				Arguments: map[string]recipe.Argument{
					"value": {Required: true},
				},
			},
		},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "shadow" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunInvokesLiteralScriptRecipeReferenceWithArguments(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("@gen value=shadow\ncat out.txt"),
			Sandboxed: new(false),
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

	var stdout bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved: resolved,
		Recipes: map[string]recipe.Recipe{
			"gen": {
				Cmd:         recipe.Command{"sh", "-c", "printf %s \"$1\" > out.txt", "shadowtree"},
				DefaultArgs: []string{"{value}"},
				Arguments: map[string]recipe.Argument{
					"value": {Required: true},
				},
			},
		},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "shadow" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunInvokesLiteralScriptRecipeReferenceInConditional(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("if @check; then printf ok; else printf fail; fi"),
			Sandboxed: new(false),
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

	var stdout bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   map[string]recipe.Recipe{"check": {Cmd: recipe.Command{"true"}}},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "ok" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunInvokesLiteralScriptRecipeReferenceWithBracketArguments(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("@gen[value=shadow]\ncat out.txt"),
			Sandboxed: new(false),
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

	var stdout bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved: resolved,
		Recipes: map[string]recipe.Recipe{
			"gen": {
				Cmd:         recipe.Command{"sh", "-c", "printf %s \"$1\" > out.txt", "shadowtree"},
				DefaultArgs: []string{"{value}"},
				Arguments: map[string]recipe.Argument{
					"value": {Required: true},
				},
			},
		},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "shadow" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunPreservesScriptArgsWithLiteralRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:         recipe.ScriptCommand("@noop\nprintf %s \"$1\""),
			DefaultArgs: []string{"shadow"},
			Sandboxed:   new(false),
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

	var stdout bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   map[string]recipe.Recipe{"noop": {Cmd: recipe.Command{"true"}}},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "shadow" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunPreservesDashPrefixedScriptArgsWithLiteralRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:         recipe.ScriptCommand("@noop\nprintf '%s:%s' \"$1\" \"$2\""),
			DefaultArgs: []string{"-n", "shadow"},
			Sandboxed:   new(false),
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

	var stdout bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   map[string]recipe.Recipe{"noop": {Cmd: recipe.Command{"true"}}},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "-n:shadow" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunDoesNotDispatchVariableExpandedScriptRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("FOO=@gen\n$FOO 2>/dev/null || printf no-dispatch"),
			Sandboxed: new(false),
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

	var stdout bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   map[string]recipe.Recipe{"gen": {Cmd: recipe.Command{"sh", "-c", "printf shadow > out.txt"}}},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "no-dispatch" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	_, err = os.Stat(filepath.Join(source, "out.txt"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("out.txt err = %v, want not exist", err)
	}
}

func TestRunInvokesCrossConfigRecipeReferenceFromTargetDir(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(source, "webui")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".shadowtree.toml"), []byte(`
[recipes.gen-schema]
cmd = ["sh", "-c", "printf '%s\n' \"$PWD\"; printf '%s' \"$1\" > out.txt", "shadowtree"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.Command{"@webui:gen-schema", "shadow"},
			Sandboxed: new(false),
		},
		nil,
		nil,
		nil,
		filepath.Join(source, ".shadowtree.toml"),
		"",
	)
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   map[string]recipe.Recipe{"test": {Cmd: recipe.Command{"@webui:gen-schema", "shadow"}}},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != target {
		t.Fatalf("stdout = %q, want target dir %q", stdout.String(), target)
	}
	data, err := os.ReadFile(filepath.Join(target, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shadow" {
		t.Fatalf("out.txt = %q", data)
	}
}

func TestRunCrossConfigRecipeReferenceUsesTopLevelSyncOut(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(source, "webui")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".shadowtree.toml"), []byte(`
[recipes.gen-schema]
cmd = ["sh", "-c", "printf shadow > out.txt"]
sync_out = ["out.txt"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{Cmd: recipe.Command{"@webui:gen-schema"}},
		nil,
		[]string{"webui/out.txt"},
		nil,
		filepath.Join(source, ".shadowtree.toml"),
		"",
	)
	if err != nil {
		t.Fatal(err)
	}

	err = Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   map[string]recipe.Recipe{"test": {Cmd: recipe.Command{"@webui:gen-schema"}}},
		SourceDir: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shadow" {
		t.Fatalf("out.txt = %q", data)
	}
}

func TestRunRejectsRecipeReferenceCycle(t *testing.T) {
	resolved, err := recipe.Resolve("a", recipe.Recipe{Cmd: recipe.Command{"@b"}, Sandboxed: new(false)}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}

	err = Run(t.Context(), Options{
		Resolved: resolved,
		Recipes: map[string]recipe.Recipe{
			"a": {Cmd: recipe.Command{"@b"}},
			"b": {Cmd: recipe.Command{"@a"}},
		},
		SourceDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "recipe reference cycle: a -> b -> a") {
		t.Fatalf("err = %v, want cycle", err)
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

func TestRunSyncsExplicitPathDeletion(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "out.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("run", recipe.Recipe{Cmd: recipe.Command{"rm", "out.txt"}}, nil, []string{"out.txt"}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source}); err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(filepath.Join(source, "out.txt"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("out.txt err = %v, want not exist", err)
	}
}

func TestSyncPathDeletesMissingWorkspacePath(t *testing.T) {
	workspace := t.TempDir()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "out.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SyncPath(workspace, source, "out.txt"); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(source, "out.txt"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("out.txt err = %v, want not exist", err)
	}
}

func TestSyncPathMissingWorkspacePathDoesNotCreateParent(t *testing.T) {
	workspace := t.TempDir()
	source := t.TempDir()

	if err := SyncPath(workspace, source, "missing/out.txt"); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(source, "missing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent err = %v, want not exist", err)
	}
}

func TestSyncPathReplacesLeafSymlink(t *testing.T) {
	workspace := t.TempDir()
	source := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(filepath.Join(workspace, "out.txt"), []byte("shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(source, "out.txt")); err != nil {
		t.Fatal(err)
	}

	if err := SyncPath(workspace, source, "out.txt"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shadow" {
		t.Fatalf("synced data = %q", data)
	}
	victimData, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(victimData) != "victim" {
		t.Fatalf("victim data = %q", victimData)
	}
}

func TestSyncPathDeletesThroughParentSymlinkWithoutMutatingTarget(t *testing.T) {
	workspace := t.TempDir()
	source := t.TempDir()
	real := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "file.txt"), []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(source, "out")); err != nil {
		t.Fatal(err)
	}

	if err := SyncPath(workspace, source, "out/file.txt"); err != nil {
		t.Fatal(err)
	}
	victimData, err := os.ReadFile(filepath.Join(real, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(victimData) != "victim" {
		t.Fatalf("victim data = %q", victimData)
	}
	info, err := os.Lstat(filepath.Join(source, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Type() == os.ModeSymlink {
		t.Fatal("source out is still a symlink")
	}
	_, err = os.Stat(filepath.Join(source, "out", "file.txt"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source out/file.txt err = %v, want not exist", err)
	}
}

func TestSyncPathReplacesParentSymlink(t *testing.T) {
	workspace := t.TempDir()
	source := t.TempDir()
	real := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(filepath.Join(workspace, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "out", "file.txt"), []byte("shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "file.txt"), []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(source, "out")); err != nil {
		t.Fatal(err)
	}

	if err := SyncPath(workspace, source, "out/file.txt"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source, "out", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shadow" {
		t.Fatalf("synced data = %q", data)
	}
	victimData, err := os.ReadFile(filepath.Join(real, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(victimData) != "victim" {
		t.Fatalf("victim data = %q", victimData)
	}
	info, err := os.Lstat(filepath.Join(source, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Type() == os.ModeSymlink {
		t.Fatal("source out is still a symlink")
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
