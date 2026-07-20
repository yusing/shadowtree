package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/systemsandbox"
)

func TestRunSystemLifecycleDoesNotSyncOutputsAfterFailureButKeepsLog(t *testing.T) {
	bin := t.TempDir()
	workspacePath := filepath.Join(bin, "workspace")
	writeExecutable(t, bin, "docker", `
case "$1" in
  create)
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "--mount" ]; then
        shift
        case "$1" in
          type=bind,src=*,dst=*)
            workspace=${1#type=bind,src=}
            workspace=${workspace%%,dst=*}
            printf '%s' "$workspace" > "`+workspacePath+`"
            break
            ;;
        esac
      fi
      shift
    done
    ;;
  start)
    workspace=$(/bin/cat "`+workspacePath+`")
    printf sandbox > "$workspace/output.txt"
    printf lifecycle-log > "$workspace/run.log"
    exit 7
    ;;
  rm) exit 0 ;;
  *) exit 1 ;;
esac`)
	t.Setenv("PATH", bin)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "output.txt"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("root", recipe.Recipe{
		Cmd: recipe.Command{"false"}, Sandboxed: new(recipe.SandboxModeSystem),
		SyncOut: []string{"output.txt"}, Log: "run.log",
	}, nil, nil, nil, filepath.Join(source, ".shadowtree.toml"), "")
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	err = runSystemLifecycle(t.Context(), systemsandbox.Docker, systemsandbox.ConfinementPolicy{User: "1000:1000"}, systemsandbox.ImagePlan{FinalTag: "image:test", Platform: "linux/amd64"}, Options{Resolved: resolved, SourceDir: source}, nil, nil, &stderr)
	var exit ExitError
	if !errors.As(err, &exit) || exit.Code != 7 {
		t.Fatalf("runSystemLifecycle error = %v, want exit 7", err)
	}
	assertFileContent(t, filepath.Join(source, "output.txt"), "host")
	assertFileContent(t, filepath.Join(source, "run.log"), "lifecycle-log")
}

func TestSystemHelperRunsOneLifecycleAndNestedReferences(t *testing.T) {
	source := t.TempDir()
	recipes := map[string]recipe.Recipe{
		"root": {
			Pre:       stageCommands(recipe.Command{"sh", "-c", "printf pre > state"}),
			Cmd:       recipe.Command{"@child"},
			Post:      stageCommands(recipe.Command{"sh", "-c", "printf post >> state"}),
			Sandboxed: new(recipe.SandboxModeSystem),
		},
		"child": {Cmd: recipe.Command{"sh", "-c", "printf child >> state"}},
	}
	resolved, err := recipe.ResolveWithOptions("root", recipes["root"], nil, nil, nil, filepath.Join(source, ".shadowtree.toml"), "", recipe.ResolveOptions{Recipes: recipes})
	if err != nil {
		t.Fatal(err)
	}
	plan := systemLifecyclePlan{Protocol: systemHelperProtocol, Resolved: resolved, Recipes: recipes, SourceDir: source}
	if code := runSystemHelperPlan(t, plan); code != 0 {
		t.Fatalf("SystemHelperMain code = %d", code)
	}
	assertFileContent(t, filepath.Join(source, "state"), "prechildpost")
}

func TestSystemHelperRunsPostAfterCancellationAndDoesNotExport(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve("root", recipe.Recipe{
		Cmd:       recipe.Command{"sh", "-c", "printf main > output"},
		Post:      stageCommands(recipe.Command{"sh", "-c", "printf post > cleanup"}),
		Sandboxed: new(recipe.SandboxModeSystem),
	}, nil, nil, nil, filepath.Join(source, ".shadowtree.toml"), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	plan := systemLifecyclePlan{Protocol: systemHelperProtocol, Resolved: resolved, SourceDir: source}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := SystemHelperMain(ctx, []string{path}); code != 130 {
		t.Fatalf("SystemHelperMain code = %d, want 130", code)
	}
	assertFileContent(t, filepath.Join(source, "cleanup"), "post")
	if _, err := os.Stat(filepath.Join(source, "output")); !os.IsNotExist(err) {
		t.Fatalf("main output exists after initial cancellation: %v", err)
	}
}

func TestSystemHelperCopiesDependencySeedBeforeLifecycle(t *testing.T) {
	source := t.TempDir()
	seed := t.TempDir()
	if err := os.MkdirAll(filepath.Join(seed, "node_modules", "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "node_modules", "tool", "index.js"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("root", recipe.Recipe{Cmd: recipe.Command{"true"}, Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	plan := systemLifecyclePlan{
		Protocol: systemHelperProtocol, Resolved: resolved, SourceDir: source,
		DependencySeeds: []systemsandbox.DependencySeed{{SourcePath: seed, TargetPath: ".", Provider: "npm"}},
	}
	if code := runSystemHelperPlan(t, plan); code != 0 {
		t.Fatalf("SystemHelperMain code = %d", code)
	}
	assertFileContent(t, filepath.Join(source, "node_modules", "tool", "index.js"), "seed")
}

func TestSystemHelperValidatesEveryDependencySeedBeforeCopying(t *testing.T) {
	source := t.TempDir()
	valid := t.TempDir()
	if err := os.WriteFile(filepath.Join(valid, "copied"), []byte("must not copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("root", recipe.Recipe{Cmd: recipe.Command{"true"}, Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	plan := systemLifecyclePlan{
		Protocol: systemHelperProtocol, Resolved: resolved, SourceDir: source,
		DependencySeeds: []systemsandbox.DependencySeed{
			{SourcePath: valid, TargetPath: "first", Provider: "npm"},
			{SourcePath: filepath.Join(t.TempDir(), "missing"), TargetPath: "second", Provider: "pnpm"},
		},
	}
	if code := runSystemHelperPlan(t, plan); code != 1 {
		t.Fatalf("SystemHelperMain code = %d, want validation failure", code)
	}
	if _, err := os.Stat(filepath.Join(source, "first")); !os.IsNotExist(err) {
		t.Fatalf("first seed was copied before full validation: %v", err)
	}
}

func TestSystemHelperRunsAggregateInsideOneLifecycle(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "sub", "go.mod"), []byte("module example.com/sub\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "go.work"), []byte("go 1.26\nuse (\n.\n./sub\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := recipe.Recipe{
		Pre:       stageCommands(recipe.Command{"sh", "-c", "printf pre > pre.txt"}),
		Cmd:       recipe.Command{"sh", "-c", "printf target > target.txt"},
		Post:      stageCommands(recipe.Command{"sh", "-c", "printf post > post.txt"}),
		Sandboxed: new(recipe.SandboxModeSystem),
	}
	resolved, err := recipe.ResolveWithOptions("test", rec, nil, nil, nil, "", recipe.GoProfile, recipe.ResolveOptions{TargetSource: recipe.GoModuleTargets, TargetDomain: "modules"})
	if err != nil {
		t.Fatal(err)
	}
	code := runSystemHelperPlan(t, systemLifecyclePlan{Protocol: systemHelperProtocol, Resolved: resolved, SourceDir: source})
	if code != 0 {
		t.Fatalf("SystemHelperMain code = %d", code)
	}
	for _, file := range []string{"pre.txt", "post.txt", "target.txt", "sub/target.txt"} {
		if _, err := os.Stat(filepath.Join(source, file)); err != nil {
			t.Fatalf("aggregate lifecycle missing %s: %v", file, err)
		}
	}
}

func TestSystemHelperAggregateFailureStillRunsPostOnce(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "sub", "go.mod"), []byte("module example.com/sub\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "go.work"), []byte("go 1.26\nuse (\n.\n./sub\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := recipe.Recipe{
		Pre:       stageCommands(recipe.Command{"sh", "-c", "printf pre >> lifecycle.txt"}),
		Cmd:       recipe.Command{"sh", "-c", `if [ "$PWD" = "` + filepath.Join(source, "sub") + `" ]; then exit 9; fi; printf target > target.txt`},
		Post:      stageCommands(recipe.Command{"sh", "-c", "printf post >> lifecycle.txt"}),
		Sandboxed: new(recipe.SandboxModeSystem),
	}
	resolved, err := recipe.ResolveWithOptions("test", rec, nil, nil, nil, "", recipe.GoProfile, recipe.ResolveOptions{TargetSource: recipe.GoModuleTargets, TargetDomain: "modules"})
	if err != nil {
		t.Fatal(err)
	}
	code := runSystemHelperPlan(t, systemLifecyclePlan{Protocol: systemHelperProtocol, Resolved: resolved, SourceDir: source})
	if code != 9 {
		t.Fatalf("SystemHelperMain code = %d, want 9", code)
	}
	assertFileContent(t, filepath.Join(source, "lifecycle.txt"), "prepost")
	assertFileContent(t, filepath.Join(source, "target.txt"), "target")
	if _, err := os.Stat(filepath.Join(source, "sub", "target.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failing target output stat error = %v, want not exist", err)
	}
}

func TestSystemCacheExportSnapshotsSelectedVolumePath(t *testing.T) {
	source := t.TempDir()
	export := t.TempDir()
	workspace := t.TempDir()
	cacheMount := filepath.Join(t.TempDir(), "cargo-target")
	cache := systemsandbox.CachePlan{Name: "cache", Key: "key", ProjectKey: "project", Provider: "cargo-target", MountPath: cacheMount, OutputPath: filepath.Join(source, "target")}
	if err := os.MkdirAll(filepath.Join(cacheMount, "debug"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheMount, "debug", "app"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "target", "debug"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "target", "debug", "app"), []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := systemLifecyclePlan{
		Caches: []systemsandbox.CachePlan{cache}, SourceDir: source,
		SyncOut: []string{"target/debug/app"}, ExportDir: export,
	}
	if err := exportSystemCaches(plan); err != nil {
		t.Fatal(err)
	}
	options := Options{SourceDir: source, Resolved: recipe.Resolved{SyncOut: plan.SyncOut}}
	if err := applySystemCacheExports(plan.Caches, options, export, workspace); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(workspace, "target", "debug", "app"), "new")
}

func TestSystemCacheExportRejectsSymlinkedCacheAncestor(t *testing.T) {
	source := t.TempDir()
	cacheMount := t.TempDir()
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(cacheMount, "debug")); err != nil {
		t.Fatal(err)
	}
	plan := systemLifecyclePlan{
		Caches:    []systemsandbox.CachePlan{{MountPath: cacheMount, OutputPath: filepath.Join(source, "target")}},
		SourceDir: source, SyncOut: []string{"target/debug/secret"}, ExportDir: t.TempDir(),
	}
	if err := exportSystemCaches(plan); err == nil {
		t.Fatal("exportSystemCaches followed a symlinked cache ancestor")
	}
	if _, err := os.Stat(filepath.Join(plan.ExportDir, "target", "debug", "secret")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exported secret stat error = %v, want not exist", err)
	}
}

func TestSystemCacheApplyReplacesSymlinkedWorkspaceAncestorWithoutEscaping(t *testing.T) {
	source := t.TempDir()
	export := t.TempDir()
	workspace := t.TempDir()
	external := t.TempDir()
	marker := filepath.Join(external, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(workspace, "target")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(export, "target", "debug"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(export, "target", "debug", "app"), []byte("app"), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := systemsandbox.CachePlan{OutputPath: filepath.Join(source, "target")}
	options := Options{SourceDir: source, Resolved: recipe.Resolved{SyncOut: []string{"target/debug/app"}}}
	if err := applySystemCacheExports([]systemsandbox.CachePlan{cache}, options, export, workspace); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, marker, "keep")
	assertFileContent(t, filepath.Join(workspace, "target", "debug", "app"), "app")
}

func runSystemHelperPlan(t *testing.T, plan systemLifecyclePlan) int {
	t.Helper()
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return SystemHelperMain(t.Context(), []string{path})
}
