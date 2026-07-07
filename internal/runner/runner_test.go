package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
)

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

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

func newOverlaySandboxForTest(t *testing.T, source, upper string) *sandboxWorkspace {
	t.Helper()
	workDir := t.TempDir()
	return &sandboxWorkspace{
		source:  source,
		root:    filepath.Join(workDir, "workspace"),
		upper:   upper,
		workDir: workDir,
		overlay: true,
	}
}

func TestOverlaySyncRootMaterializesRequestedPaths(t *testing.T) {
	source := t.TempDir()
	upper := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "other.txt"), []byte("host other"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upper, "file.txt"), []byte("shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upper, "other.txt"), []byte("shadow other"), 0o644); err != nil {
		t.Fatal(err)
	}
	sandbox := newOverlaySandboxForTest(t, source, upper)

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
	if _, err := os.Stat(filepath.Join(syncRoot, "other.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("other.txt err = %v, want not exist", err)
	}
}

func TestOverlaySyncRootMaterializesRequestedDirectory(t *testing.T) {
	source := t.TempDir()
	upper := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "dir", "host.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(upper, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upper, "dir", "shadow.txt"), []byte("shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upper, "outside.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	sandbox := newOverlaySandboxForTest(t, source, upper)

	syncRoot, cleanup, err := sandbox.SyncRoot([]string{"dir"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, name := range []string{"host.txt", "shadow.txt"} {
		if _, err := os.Stat(filepath.Join(syncRoot, "dir", name)); err != nil {
			t.Fatalf("dir/%s err = %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(syncRoot, "outside.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside.txt err = %v, want not exist", err)
	}
}

func TestOverlaySyncRootMaterializesRequestedSymlink(t *testing.T) {
	source := t.TempDir()
	upper := t.TempDir()
	if err := os.Symlink("target.txt", filepath.Join(upper, "link.txt")); err != nil {
		t.Fatal(err)
	}
	sandbox := newOverlaySandboxForTest(t, source, upper)

	syncRoot, cleanup, err := sandbox.SyncRoot([]string{"link.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	target, err := os.Readlink(filepath.Join(syncRoot, "link.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "target.txt" {
		t.Fatalf("link target = %q, want target.txt", target)
	}
}

func TestOverlaySyncRootSkipsUnreadableLowerFileReplacedByUpper(t *testing.T) {
	source := t.TempDir()
	upper := t.TempDir()
	lower := filepath.Join(source, "file.txt")
	if err := os.WriteFile(lower, []byte("host"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lower, 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upper, "file.txt"), []byte("shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	sandbox := newOverlaySandboxForTest(t, source, upper)

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
		t.Fatalf("file.txt = %q, want shadow", data)
	}
}

func TestOverlaySyncRootSkipsUnrelatedUnreadableUpperDirectory(t *testing.T) {
	source := t.TempDir()
	upper := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(upper, "unrelated")
	if err := os.Mkdir(unrelated, 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unrelated, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(unrelated, 0o700)
	})
	sandbox := newOverlaySandboxForTest(t, source, upper)

	syncRoot, cleanup, err := sandbox.SyncRoot([]string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(filepath.Join(syncRoot, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "host" {
		t.Fatalf("file.txt = %q, want host", data)
	}
}

func TestOverlaySyncRootRejectsSourceParentSymlinkEscape(t *testing.T) {
	source := t.TempDir()
	upper := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "file.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	sandbox := newOverlaySandboxForTest(t, source, upper)

	syncRoot, cleanup, err := sandbox.SyncRoot([]string{"link/file.txt"})
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		t.Fatalf("SyncRoot succeeded with %q, want symlink escape error", syncRoot)
	}
}

func TestCopiedWorkspaceSyncRootUsesWorkspace(t *testing.T) {
	workspace := t.TempDir()
	source := t.TempDir()
	sandbox := &sandboxWorkspace{root: workspace, source: source}

	syncRoot, cleanup, err := sandbox.SyncRoot([]string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if syncRoot != workspace {
		t.Fatalf("sync root = %q, want workspace %q", syncRoot, workspace)
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

func TestStageBoundarySummarizesCommands(t *testing.T) {
	longArg := strings.Repeat("a", stageBoundaryCommandMax+1)
	tests := []struct {
		name       string
		phase      string
		index      int
		hasForEach bool
		command    recipe.Command
		want       string
	}{
		{
			name:    "multiline pre script",
			phase:   phasePre,
			command: recipe.ScriptCommand("setup\nrun"),
			want:    "== pre[0]: <script> ==",
		},
		{
			name:    "exact recipe ref",
			phase:   phaseMain,
			command: recipe.Command{"@build"},
			want:    "== cmd: @build ==",
		},
		{
			name:       "for_each cmd item",
			phase:      phaseMain,
			index:      2,
			hasForEach: true,
			command:    recipe.Command{"go", "test"},
			want:       "== cmd[2]: go test ==",
		},
		{
			name:    "long command",
			phase:   phaseMain,
			command: recipe.Command{longArg},
			want:    "== cmd: " + strings.Repeat("a", stageBoundaryCommandMax-3) + "... ==",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stageBoundary(tt.phase, tt.index, tt.hasForEach, tt.command); got != tt.want {
				t.Fatalf("stageBoundary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunVerbosePrintsStageBoundaries(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Pre:       []recipe.Command{recipe.ScriptCommand("printf 'pre\n'\nprintf 'again\n'")},
			Cmd:       recipe.Command{"@child"},
			Post:      []recipe.Command{recipe.ScriptCommand("printf 'post\n'")},
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

	var stdout, stderr bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   map[string]recipe.Recipe{"child": {Cmd: recipe.Command{"true"}}},
		SourceDir: source,
		Stdout:    &stdout,
		Stderr:    &stderr,
		Verbose:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := stderr.String()
	for _, want := range []string{
		"== pre[0]: <script> ==\n",
		"== cmd: @child ==\n",
		"== post[0]: <script> ==\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stderr = %q, want boundary %q", got, want)
		}
	}
	if strings.Contains(got, "printf 'again") {
		t.Fatalf("stderr leaked multiline script body:\n%s", got)
	}
}

func TestRunLogsAllStagesByDefault(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.ResolveWithOptions(
		"test",
		recipe.Recipe{
			Pre:       []recipe.Command{recipe.ScriptCommand("printf 'pre\n'")},
			Cmd:       recipe.ScriptCommand("printf 'cmd\n'"),
			Post:      []recipe.Command{recipe.ScriptCommand("printf 'post\n'")},
			Log:       "logs/{run_id}.log",
			Sandboxed: new(false),
		},
		nil,
		nil,
		nil,
		filepath.Join(source, ".shadowtree.toml"),
		"",
		recipe.ResolveOptions{RunID: "abcdef0123456789abcdef0123456789"},
	)
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source, Stdout: &stdout}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "pre\ncmd\npost\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	assertFileContent(t, filepath.Join(source, "logs", "abcdef0123456789abcdef0123456789.log"), `== pre[0]: <script> ==
pre
== cmd: <script> ==
cmd
== post[0]: <script> ==
post
`)
}

func TestRunLogsStdoutAndStderr(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("printf 'out\n'; printf 'err\n' >&2"),
			Log:       "run.log",
			LogTee:    new(false),
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

	var stdout, stderr bytes.Buffer
	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source, Stdout: &stdout, Stderr: &stderr}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q, want selected output suppressed", stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(source, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "== cmd: <script> ==\n") ||
		!strings.Contains(string(data), "out\n") ||
		!strings.Contains(string(data), "err\n") {
		t.Fatalf("log = %q, want stdout and stderr", data)
	}
}

func TestRunLogsCmdStageForEachItemsOnly(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			ForEach:   recipe.ScriptCommand("printf 'one\ntwo\n'"),
			Cmd:       recipe.ScriptCommand("printf 'cmd:%s\n' '{item}'"),
			Log:       "run.log",
			LogStages: []string{recipe.LogStageCmd},
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

	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source, Stdout: io.Discard}); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(source, "run.log"), `== cmd[0]: <script> ==
cmd:one
== cmd[1]: <script> ==
cmd:two
`)
}

func TestRunLogTeeFalseSuppressesSelectedTerminalOutput(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Pre:       []recipe.Command{recipe.ScriptCommand("printf 'pre\n'")},
			Cmd:       recipe.ScriptCommand("printf 'cmd\n'"),
			Post:      []recipe.Command{recipe.ScriptCommand("printf 'post\n'")},
			Log:       "run.log",
			LogStages: []string{recipe.LogStageCmd},
			LogTee:    new(false),
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
	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source, Stdout: &stdout}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "pre\npost\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	assertFileContent(t, filepath.Join(source, "run.log"), `== cmd: <script> ==
cmd
`)
}

func TestRunRejectsEscapingLogPath(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.Command{"true"},
			Log:       "../outside.log",
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

	err = Run(t.Context(), Options{Resolved: resolved, SourceDir: source})
	if err == nil || !strings.Contains(err.Error(), "log path must be relative to config directory") {
		t.Fatalf("Run() error = %v, want log path error", err)
	}
}

func TestRunCopiedWorkspaceSyncPreservesHostLog(t *testing.T) {
	original := newOverlayWorkspace
	newOverlayWorkspace = func(context.Context, string, string, string) (*sandboxWorkspace, error) {
		return nil, errors.New("forced overlay failure")
	}
	t.Cleanup(func() {
		newOverlayWorkspace = original
	})
	for _, syncOutAll := range []bool{false, true} {
		name := "sync_out"
		if syncOutAll {
			name = "sync_out_all"
		}
		t.Run(name, func(t *testing.T) {
			source := t.TempDir()
			resolved, err := recipe.Resolve(
				"test",
				recipe.Recipe{
					Cmd:       recipe.ScriptCommand("printf 'logged\n'"),
					Log:       "logs/run.log",
					SyncOut:   []string{"logs"},
					Sandboxed: new(true),
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
			if err := Run(t.Context(), Options{
				Resolved:   resolved,
				SourceDir:  source,
				Stdout:     io.Discard,
				Stderr:     io.Discard,
				SyncOutAll: syncOutAll,
			}); err != nil {
				t.Fatal(err)
			}
			assertFileContent(t, filepath.Join(source, "logs", "run.log"), `== cmd: <script> ==
logged
`)
		})
	}
}

func TestRunLogsPostAfterMainFailure(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("printf 'cmd\n'; exit 7"),
			Post:      []recipe.Command{recipe.ScriptCommand("printf 'post\n'")},
			Log:       "run.log",
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

	err = Run(t.Context(), Options{Resolved: resolved, SourceDir: source, Stdout: io.Discard})
	if err == nil {
		t.Fatal("Run succeeded, want command failure")
	}
	assertFileContent(t, filepath.Join(source, "run.log"), `== cmd: <script> ==
cmd
== post[0]: <script> ==
post
`)
}

func TestRunLogsNestedRecipeOutputThroughParentStage(t *testing.T) {
	source := t.TempDir()
	child := recipe.Recipe{
		Pre:  []recipe.Command{recipe.ScriptCommand("printf 'child-pre\n'")},
		Cmd:  recipe.ScriptCommand("printf 'child-cmd\n'"),
		Post: []recipe.Command{recipe.ScriptCommand("printf 'child-post\n'")},
		Log:  "ignored.log",
	}
	resolved, err := recipe.Resolve(
		"parent",
		recipe.Recipe{
			Cmd:       recipe.Command{"@child"},
			Log:       "parent.log",
			LogStages: []string{recipe.LogStageCmd},
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

	if err := Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   map[string]recipe.Recipe{"child": child},
		SourceDir: source,
		Stdout:    io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(source, "parent.log"), `== cmd: @child ==
child-pre
child-cmd
child-post
`)
	if _, err := os.Stat(filepath.Join(source, "ignored.log")); !os.IsNotExist(err) {
		t.Fatalf("nested log stat error = %v, want not exist", err)
	}
}

func TestRunInvokesRecipeReferenceDirectly(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.Command{"cat", "out.txt"},
			Pre:       []recipe.Command{{"@gen", "value=shadow"}},
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
				Cmd: recipe.Command{"sh", "-c", "printf %s \"$1\" > out.txt", "shadowtree", "{value}"},
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
				Cmd: recipe.Command{"sh", "-c", "printf %s \"$1\" > out.txt", "shadowtree", "{value}"},
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
				Cmd: recipe.Command{"sh", "-c", "printf %s \"$1\" > out.txt", "shadowtree", "{value}"},
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

func TestRunExpandsScriptVariablesInRecipeReferenceArguments(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/runtime\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(source, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(source, "go.args")
	fakeGo := filepath.Join(bin, "go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GO_CAPTURE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	recipes := recipe.Builtins(recipe.GoProfile, recipe.BuiltinOptions{Dir: source})
	recipes["bench"] = recipe.Recipe{
		Cmd:       recipe.ScriptCommand("run_bench() {\n\tpkg=./internal/runner\n\tbench=BenchmarkRun\n\t@test \"$pkg\" -run '^$' -bench \"$bench\" -benchtime=1x -count=1 {@}\n}\nrun_bench"),
		Env:       map[string]string{"GO_CAPTURE": capture, "PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH")},
		Sandboxed: new(false),
	}
	resolved, err := recipe.Resolve(
		"bench",
		recipes["bench"],
		[]string{"-cpu", "1"},
		nil,
		nil,
		"",
		recipe.GoProfile,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   recipes,
		SourceDir: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := "test\n./internal/runner\n-run\n^$\n-bench\nBenchmarkRun\n-benchtime=1x\n-count=1\n-cpu\n1\n"
	if string(got) != want {
		t.Fatalf("go args = %q, want %q", string(got), want)
	}
}

func TestRunScriptRecipeReferenceUsesRelativePathFromRecipeDir(t *testing.T) {
	source := t.TempDir()
	bin := filepath.Join(source, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "shadow-echo"), []byte("#!/bin/sh\nprintf shadow\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("@echo"),
			Env:       map[string]string{"PATH": "bin"},
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
		Recipes:   map[string]recipe.Recipe{"echo": {Cmd: recipe.Command{"shadow-echo"}}},
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

func TestRunScriptRecipeReferenceRespectsUnsetPath(t *testing.T) {
	source := t.TempDir()
	bin := filepath.Join(source, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "shadow-echo"), []byte("#!/bin/sh\nprintf shadow\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("unset PATH\n@echo"),
			Env:       map[string]string{"PATH": bin},
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

	err = Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   map[string]recipe.Recipe{"echo": {Cmd: recipe.Command{"shadow-echo"}}},
		SourceDir: source,
	})
	if err == nil || !strings.Contains(err.Error(), "executable file not found") {
		t.Fatalf("Run error = %v, want command not found", err)
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
				Cmd: recipe.Command{"sh", "-c", "printf %s \"$1\" > out.txt", "shadowtree", "{value}"},
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

func TestRunForEachRunsMainPerItem(t *testing.T) {
	source := t.TempDir()
	for _, dir := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(source, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := recipe.Resolve(
		"lint",
		recipe.Recipe{
			ForEach:   recipe.ScriptCommand("@enum a='alpha item' b='beta item'"),
			Workdir:   "{item}",
			Pre:       []recipe.Command{recipe.ScriptCommand("printf 'pre\n' > out.txt")},
			Cmd:       recipe.ScriptCommand(`printf '%s:%s:%s:%s\n' "{item_index}" "{item}" "{item_help}" "$(basename "$PWD")" >> ../out.txt`),
			Post:      []recipe.Command{recipe.ScriptCommand("printf 'post\n' >> out.txt")},
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

	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := "pre\n0:a:alpha item:a\n1:b:beta item:b\npost\n"
	if string(data) != want {
		t.Fatalf("out.txt = %q, want %q", data, want)
	}
}

func TestRunUsesWorkdirWithoutForEach(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "module"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve(
		"pwd",
		recipe.Recipe{
			Workdir:   "module",
			Cmd:       recipe.ScriptCommand(`printf '%s' "$(basename "$PWD")"`),
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
	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source, Stdout: &stdout}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "module" {
		t.Fatalf("stdout = %q, want module", stdout.String())
	}
}

func TestRunForEachStopsOnFirstFailureAndRunsPost(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"check",
		recipe.Recipe{
			ForEach:   recipe.ScriptCommand("@enum a b c"),
			Cmd:       recipe.ScriptCommand(`printf '%s\n' "{item}" >> out.txt; test "{item}" != b`),
			Post:      []recipe.Command{recipe.ScriptCommand("printf 'post\n' >> out.txt")},
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

	err = Run(t.Context(), Options{Resolved: resolved, SourceDir: source})
	if err == nil {
		t.Fatal("Run succeeded, want failure")
	}
	data, readErr := os.ReadFile(filepath.Join(source, "out.txt"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := "a\nb\npost\n"
	if string(data) != want {
		t.Fatalf("out.txt = %q, want %q", data, want)
	}
}

func TestRunForEachUsesCommandBackedValues(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"check",
		recipe.Recipe{
			ForEach:   recipe.ScriptCommand("printf 'a\\talpha\\nb\\tbeta\\n'"),
			Cmd:       recipe.ScriptCommand(`printf '%s:%s\n' "{item}" "{item_help}" >> out.txt`),
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

	if err := Run(t.Context(), Options{Resolved: resolved, SourceDir: source}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := "a:alpha\nb:beta\n"
	if string(data) != want {
		t.Fatalf("out.txt = %q, want %q", data, want)
	}
}

func TestRunPreservesScriptArgsWithLiteralRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("@noop\nprintf %s 'shadow'"),
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

func TestRunSupportsExportBeforeScriptRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("VALUE=shadow\nexport VALUE\n@echo-value"),
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
			"echo-value": {Cmd: recipe.Command{"sh", "-c", "printf %s \"$VALUE\""}},
		},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "shadow" {
		t.Fatalf("stdout = %q, want exported value", stdout.String())
	}
}

func TestRunUsesCurrentExportedValueForScriptRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("export VALUE=old\nVALUE=shadow\n@echo-value"),
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
			"echo-value": {Cmd: recipe.Command{"sh", "-c", "printf %s \"${VALUE:-missing}\""}},
		},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "shadow" {
		t.Fatalf("stdout = %q, want current exported value", stdout.String())
	}
}

func TestRunDoesNotReexportUnsetValueForScriptRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("export VALUE=shadow\nunset VALUE\n@echo-value"),
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
			"echo-value": {Cmd: recipe.Command{"sh", "-c", "printf %s \"${VALUE:-missing}\""}},
		},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "missing" {
		t.Fatalf("stdout = %q, want unset value hidden", stdout.String())
	}
}

func TestRunSupportsExportBeforeExternalCommandInScriptWithRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("VALUE=shadow\nexport VALUE\n@noop\nsh -c 'printf %s \"$VALUE\"'"),
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
		Recipes:   map[string]recipe.Recipe{"noop": {Cmd: recipe.Command{"true"}}},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "shadow" {
		t.Fatalf("stdout = %q, want exported value", stdout.String())
	}
}

func TestRunDoesNotPassUnexportedVariablesToScriptRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("VALUE=shadow\n@echo-value"),
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
			"echo-value": {Cmd: recipe.Command{"sh", "-c", "printf %s \"${VALUE:-missing}\""}},
		},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "missing" {
		t.Fatalf("stdout = %q, want unexported value hidden", stdout.String())
	}
}

func TestRunDoesNotPassRecreatedUnexportedVariableToScriptRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("unset VALUE\nVALUE=shadow\n@echo-value"),
			Env:       map[string]string{"VALUE": "base"},
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
			"echo-value": {Cmd: recipe.Command{"sh", "-c", "printf %s \"${VALUE:-missing}\""}},
		},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "missing" {
		t.Fatalf("stdout = %q, want unexported value hidden", stdout.String())
	}
}

func TestRunPreservesDashPrefixedScriptArgsWithLiteralRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("@noop\nprintf '%s:%s' '-n' 'shadow'"),
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
[env]
SCHEMA_VALUE = "{value}"

[recipes.gen-schema]
cmd = '''printf '%s\n' "$PWD"; printf '%s' "$SCHEMA_VALUE" > out.txt'''

[recipes.gen-schema.arguments.value]
required = true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.Command{"@webui:gen-schema", "value=shadow"},
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
		Recipes:   map[string]recipe.Recipe{"test": {Cmd: recipe.Command{"@webui:gen-schema", "value=shadow"}}},
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

func TestRunRejectsCrossConfigRecipeReferenceThroughOutsideSymlink(t *testing.T) {
	source := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, ".shadowtree.toml"), []byte(`
[recipes.gen-schema]
cmd = "printf shadow > out.txt"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "webui")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.Command{"@webui:gen-schema"},
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

	err = Run(t.Context(), Options{
		Resolved:  resolved,
		Recipes:   map[string]recipe.Recipe{"test": {Cmd: recipe.Command{"@webui:gen-schema"}}},
		SourceDir: source,
	})
	if err == nil || !strings.Contains(err.Error(), "path is outside source") {
		t.Fatalf("Run() error = %v, want outside source", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "out.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside out.txt err = %v, want not exist", err)
	}
}

func TestRunSameConfigRecipeReferenceExpandsGlobalEnvForTarget(t *testing.T) {
	source := t.TempDir()
	configEnv := map[string]string{"VALUE": "{value}"}
	parent := recipe.Recipe{
		Cmd:       recipe.Command{"@child", "value=shadow"},
		Sandboxed: new(false),
		Arguments: map[string]recipe.Argument{
			"value": {Default: "parent"},
		},
	}
	resolved, err := recipe.Resolve("parent", parent, nil, nil, configEnv, filepath.Join(source, ".shadowtree.toml"), "")
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved: resolved,
		Recipes: map[string]recipe.Recipe{
			"parent": parent,
			"child": {
				Cmd: recipe.ScriptCommand(`printf '%s' "$VALUE"`),
				Arguments: map[string]recipe.Argument{
					"value": {Required: true},
				},
			},
		},
		ConfigEnv: configEnv,
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "shadow" {
		t.Fatalf("stdout = %q, want child-expanded env", stdout.String())
	}
}

func TestCommandOutputCrossConfigRecipeReferenceExpandsGlobalEnv(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(source, "webui")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".shadowtree.toml"), []byte(`
[env]
SCHEMA_VALUE = "{value}"

[recipes.gen-schema]
cmd = '''printf '%s' "$SCHEMA_VALUE"'''

[recipes.gen-schema.arguments.value]
required = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	output, err := CommandOutput(t.Context(), source, nil, recipe.Command{"@webui:gen-schema", "value=shadow"}, CommandOutputOptions{
		ConfigPath: filepath.Join(source, ".shadowtree.toml"),
		SourceDir:  source,
		Recipes:    map[string]recipe.Recipe{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output != "shadow" {
		t.Fatalf("output = %q, want target-expanded env", output)
	}
}

func TestCommandOutputRejectsCrossConfigRecipeReferenceThroughOutsideSymlink(t *testing.T) {
	source := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, ".shadowtree.toml"), []byte(`
[recipes.gen-schema]
cmd = "printf shadow > out.txt"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "webui")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := CommandOutput(t.Context(), source, nil, recipe.Command{"@webui:gen-schema"}, CommandOutputOptions{
		ConfigPath: filepath.Join(source, ".shadowtree.toml"),
		SourceDir:  source,
		Recipes:    map[string]recipe.Recipe{},
	})
	if err == nil || !strings.Contains(err.Error(), "path is outside source") {
		t.Fatalf("CommandOutput() error = %v, want outside source", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "out.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside out.txt err = %v, want not exist", err)
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
cmd = "printf shadow > out.txt"
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

func TestSyncPathRejectsWorkspaceParentSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	source := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "file.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "out", "file.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SyncPath(workspace, source, "out/file.txt"); err == nil {
		t.Fatal("SyncPath succeeded, want symlink escape error")
	}
	data, err := os.ReadFile(filepath.Join(source, "out", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "host" {
		t.Fatalf("source data = %q, want host", data)
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
