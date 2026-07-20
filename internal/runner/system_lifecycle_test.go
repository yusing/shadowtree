package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/systemsandbox"
	"golang.org/x/sys/unix"
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
	if os.Geteuid() != 0 {
		if err := os.WriteFile(filepath.Join(source, "unreadable\nname"), []byte("secret"), 0o000); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := recipe.Resolve("root", recipe.Recipe{
		Cmd: recipe.Command{"false"}, Sandboxed: new(recipe.SandboxModeSystem),
		SyncOut: []string{"output.txt"}, Log: "run.log",
	}, nil, nil, nil, filepath.Join(source, ".shadowtree.toml"), "")
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	err = runSystemLifecycle(t.Context(), systemsandbox.Docker, systemsandbox.ConfinementPolicy{User: "1000:1000"}, systemsandbox.WorkspaceCopied, systemsandbox.ImagePlan{FinalTag: "image:test", Platform: "linux/amd64"}, Options{Resolved: resolved, SourceDir: source}, nil, nil, &stderr, &stderr, newSystemProgress(io.Discard))
	var exit ExitError
	if !errors.As(err, &exit) || exit.Code != 7 {
		t.Fatalf("runSystemLifecycle error = %v, want exit 7", err)
	}
	assertFileContent(t, filepath.Join(source, "output.txt"), "host")
	assertFileContent(t, filepath.Join(source, "run.log"), "lifecycle-log")
	if os.Geteuid() != 0 && !strings.Contains(stderr.String(), `skipped unreadable path "unreadable\nname"`) {
		t.Fatalf("stderr does not quote unreadable path:\n%s", stderr.String())
	}
}

func TestRunSystemLifecycleFallsBackToCopyOnlyAfterOverlayCreateFailure(t *testing.T) {
	bin := t.TempDir()
	workspacePath := filepath.Join(bin, "workspace")
	writeExecutable(t, bin, "podman", `
case "$1" in
  create)
    case "$*" in
      *:O,upperdir=*) printf '%s' 'overlay setup rejected' >&2; exit 1 ;;
    esac
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
    printf fallback > "$workspace/output.txt"
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
		Cmd: recipe.Command{"true"}, Sandboxed: new(recipe.SandboxModeSystem), SyncOut: []string{"output.txt"},
	}, nil, nil, nil, filepath.Join(source, ".shadowtree.toml"), "")
	if err != nil {
		t.Fatal(err)
	}
	var verbose bytes.Buffer
	err = runSystemLifecycle(
		t.Context(), systemsandbox.Podman,
		systemsandbox.ConfinementPolicy{User: "0:0", UserNamespace: "host"},
		systemsandbox.WorkspaceOverlay,
		systemsandbox.ImagePlan{FinalTag: "image:test", Platform: "linux/amd64"},
		Options{Resolved: resolved, SourceDir: source}, nil, nil, io.Discard, &verbose, newSystemProgress(io.Discard),
	)
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(source, "output.txt"), "fallback")
	if !strings.Contains(verbose.String(), "runtime overlay workspace unavailable") || !strings.Contains(verbose.String(), "using copied fallback") {
		t.Fatalf("verbose output missing quoted runtime fallback reason:\n%s", verbose.String())
	}
}

func TestRunSystemLifecycleDoesNotFallbackAfterUncertainOverlayCleanup(t *testing.T) {
	bin := t.TempDir()
	createCalls := filepath.Join(bin, "create-calls")
	writeExecutable(t, bin, "podman", `
case "$1" in
  create)
    printf 'create\n' >> "`+createCalls+`"
    printf '%s' 'overlay setup rejected' >&2
    exit 1
    ;;
  rm)
    printf '%s' 'cleanup failed' >&2
    exit 1
    ;;
  *) exit 1 ;;
esac`)
	t.Setenv("PATH", bin)
	source := t.TempDir()
	resolved, err := recipe.Resolve("root", recipe.Recipe{
		Cmd: recipe.Command{"true"}, Sandboxed: new(recipe.SandboxModeSystem),
	}, nil, nil, nil, filepath.Join(source, ".shadowtree.toml"), "")
	if err != nil {
		t.Fatal(err)
	}
	err = runSystemLifecycle(
		t.Context(), systemsandbox.Podman,
		systemsandbox.ConfinementPolicy{User: "0:0", UserNamespace: "host"},
		systemsandbox.WorkspaceOverlay,
		systemsandbox.ImagePlan{FinalTag: "image:test", Platform: "linux/amd64"},
		Options{Resolved: resolved, SourceDir: source}, nil, nil, io.Discard, io.Discard, newSystemProgress(io.Discard),
	)
	if err == nil || !strings.Contains(err.Error(), "remove possibly created system container") {
		t.Fatalf("runSystemLifecycle error = %v, want uncertain cleanup failure", err)
	}
	data, readErr := os.ReadFile(createCalls)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.Count(string(data), "create\n"); got != 1 {
		t.Fatalf("container create attempts = %d, want no copied fallback retry", got)
	}
}

func TestSystemLifecycleEnvironmentUsesDeterministicLocaleUnlessExplicitlyOverridden(t *testing.T) {
	host := []string{
		"PATH=/host/bin",
		"HOME=/host/home",
		"TMPDIR=/host/tmp",
		"LANG=zh_HK.UTF-8",
		"LANGUAGE=zh_HK:zh",
		"LC_ALL=en_US.UTF-8",
		"LC_TIME=de_DE.UTF-8",
		"LC_FUTURE=malformed locale value",
		"LOCAL_SETTING=preserved",
		"XLC_ALL=preserved",
	}

	environment := systemLifecycleEnvironment(host, recipe.Resolved{}, nil)
	for _, name := range []string{"LANGUAGE", "LC_ALL", "LC_TIME", "LC_FUTURE"} {
		if _, ok := environment[name]; ok {
			t.Errorf("inherited %s was preserved: %#v", name, environment)
		}
	}
	for name, want := range map[string]string{
		"LANG":          systemDefaultLocale,
		"HOME":          "/tmp/shadowtree-home",
		"TMPDIR":        "/tmp",
		"LOCAL_SETTING": "preserved",
		"XLC_ALL":       "preserved",
	} {
		if got := environment[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if _, ok := environment["PATH"]; ok {
		t.Fatalf("host PATH was preserved: %#v", environment)
	}

	resolved := recipe.Resolved{
		GlobalEnv: map[string]string{"LANG": "de_DE.UTF-8", "LC_TIME": "fr_FR.UTF-8"},
		Recipe:    recipe.Recipe{Env: map[string]string{"LANG": "ja_JP.UTF-8", "LC_NUMERIC": "ar_EG.UTF-8"}},
	}
	caches := []systemsandbox.CachePlan{{Environment: map[string]string{"GOCACHE": "/opt/shadowtree/cache/go-build"}}}
	environment = systemLifecycleEnvironment(host, resolved, caches)
	for name, want := range map[string]string{
		"LANG":       "ja_JP.UTF-8",
		"LC_TIME":    "fr_FR.UTF-8",
		"LC_NUMERIC": "ar_EG.UTF-8",
		"GOCACHE":    "/opt/shadowtree/cache/go-build",
	} {
		if got := environment[name]; got != want {
			t.Errorf("explicit %s = %q, want %q", name, got, want)
		}
	}

	runtime := []string{
		"PATH=/opt/shadowtree/bin:/usr/bin",
		"COREPACK_HOME=/opt/shadowtree/corepack",
		"LANG=base-image-locale",
		"LC_ALL=base-image-locale",
		"LC_FUTURE=base-image-locale",
	}
	merged := envListMap(systemRuntimeEnvironment(runtime, environment))
	for name, want := range map[string]string{
		"PATH":          "/opt/shadowtree/bin:/usr/bin",
		"COREPACK_HOME": "/opt/shadowtree/corepack",
		"LANG":          "ja_JP.UTF-8",
		"LC_TIME":       "fr_FR.UTF-8",
		"LC_NUMERIC":    "ar_EG.UTF-8",
		"GOCACHE":       "/opt/shadowtree/cache/go-build",
	} {
		if got := merged[name]; got != want {
			t.Errorf("runtime %s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"LC_ALL", "LC_FUTURE"} {
		if _, ok := merged[name]; ok {
			t.Errorf("base image %s was preserved: %#v", name, merged)
		}
	}
}

func TestCopySystemWorkspaceTreePreservesConfigsAndReadableFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode-000 files")
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "readable.txt"), []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".shadowtree.toml"), []byte("profile = \"go\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, ".shadowtree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".shadowtree", "included.toml"), []byte("[recipes.check]\ncmd = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "HEAD"), []byte("ref: main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "runtime-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "private-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "private-dir", "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "private-dir"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(source, "private-dir"), 0o755)
	})
	if err := os.Mkdir(filepath.Join(source, "read-only"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "read-only", "input.txt"), []byte("input"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "read-only"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(source, "read-only"), 0o755)
	})
	unreadable := filepath.Join(source, "runtime-data", "secret.zip")
	if err := os.WriteFile(unreadable, []byte("secret"), 0o000); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "workspace")
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(destination, "read-only"), 0o755)
	})
	skipped, err := copySystemWorkspaceTree(source, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(skipped, []string{"private-dir", "runtime-data/secret.zip"}) {
		t.Fatalf("skipped = %v", skipped)
	}
	assertFileContent(t, filepath.Join(destination, "readable.txt"), "readable")
	assertFileContent(t, filepath.Join(destination, "read-only", "input.txt"), "input")
	info, err := os.Stat(filepath.Join(destination, "read-only"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o555 {
		t.Fatalf("read-only directory mode = %v, want 0555", got)
	}
	assertFileContent(t, filepath.Join(destination, ".shadowtree.toml"), "profile = \"go\"\n")
	assertFileContent(t, filepath.Join(destination, ".shadowtree", "included.toml"), "[recipes.check]\ncmd = \"true\"\n")
	if _, err := os.Stat(filepath.Join(destination, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".git copied into system workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "runtime-data", "secret.zip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreadable file copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "private-dir")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreadable directory left in destination: %v", err)
	}
}

func TestValidateSkippedSystemPathsProtectsSyncBoundaries(t *testing.T) {
	for name, test := range map[string]struct {
		skipped    []string
		syncOut    []string
		syncOutAll bool
		wantErr    bool
	}{
		"no skipped paths": {
			syncOut: []string{"generated/output"},
		},
		"unrelated future output": {
			skipped: []string{"runtime-data/secret.zip"}, syncOut: []string{"future/output"},
		},
		"selected ancestor collision": {
			skipped: []string{"runtime-data/secret.zip"}, syncOut: []string{"runtime-data"}, wantErr: true,
		},
		"selected descendant collision": {
			skipped: []string{"runtime-data"}, syncOut: []string{"runtime-data/generated"}, wantErr: true,
		},
		"malformed selection": {
			skipped: []string{"runtime-data/secret.zip"}, syncOut: []string{"../escape"}, wantErr: true,
		},
		"whole workspace sync": {
			skipped: []string{"runtime-data/secret.zip"}, syncOutAll: true, wantErr: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateSkippedSystemPaths(test.skipped, test.syncOut, test.syncOutAll)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateSkippedSystemPaths() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestOverlaySystemWorkspaceMaterializesBeforeApplyingCacheExport(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "output.txt"), []byte("lower"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	workspace, err := prepareSystemWorkspace(source, dir, systemsandbox.WorkspaceOverlay, []string{"output.txt"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.mount.Strategy != systemsandbox.WorkspaceOverlay {
		t.Skipf("overlay preparation unavailable: %v", workspace.fallbackReason)
	}
	if err := os.WriteFile(filepath.Join(workspace.overlay.upper, "output.txt"), []byte("upper"), 0o644); err != nil {
		t.Fatal(err)
	}
	export := filepath.Join(dir, "export")
	if err := os.Mkdir(export, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(export, "output.txt"), []byte("cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	options := Options{
		SourceDir: source,
		Resolved:  recipe.Resolved{SyncOut: []string{"output.txt"}},
	}
	cache := systemsandbox.CachePlan{Provider: "test-cache", OutputPath: filepath.Join(source, "output.txt")}
	if err := workspace.syncSuccess([]systemsandbox.CachePlan{cache}, options, export); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(source, "output.txt"), "cache")
}

func TestOverlayWholeSyncCacheFailureLeavesSourceUnchanged(t *testing.T) {
	source := t.TempDir()
	output := filepath.Join(source, "output.txt")
	if err := os.WriteFile(output, []byte("lower"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err := prepareSystemWorkspace(source, t.TempDir(), systemsandbox.WorkspaceOverlay, nil, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.mount.Strategy != systemsandbox.WorkspaceOverlay {
		t.Skipf("overlay preparation unavailable: %v", workspace.fallbackReason)
	}
	if err := os.WriteFile(filepath.Join(workspace.overlay.upper, "output.txt"), []byte("upper"), 0o644); err != nil {
		t.Fatal(err)
	}
	export := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(export, []byte("invalid export root"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("root", recipe.Recipe{Cmd: recipe.Command{"true"}, Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, filepath.Join(source, ".shadowtree.toml"), "")
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Resolved: resolved, SourceDir: source, SyncOutAll: true}
	cache := systemsandbox.CachePlan{Provider: "test-cache", OutputPath: filepath.Join(source, "cache")}
	if err := workspace.syncSuccess([]systemsandbox.CachePlan{cache}, options, export); err == nil {
		t.Fatal("whole sync accepted invalid cache export root")
	}
	assertFileContent(t, output, "lower")
}

func TestSystemDirectorySyncRetainsNestedShadowtreeConfiguration(t *testing.T) {
	for _, strategy := range []systemsandbox.WorkspaceStrategy{systemsandbox.WorkspaceOverlay, systemsandbox.WorkspaceCopied} {
		t.Run(string(strategy), func(t *testing.T) {
			source := t.TempDir()
			project := filepath.Join(source, "subproject")
			if err := os.MkdirAll(filepath.Join(project, ".shadowtree"), 0o755); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{
				".shadowtree.toml":         "profile = \"go\"\n",
				".shadowtree.local.toml":   "[vars]\nmode = \"local\"\n",
				".shadowtree/include.toml": "[recipes.check]\ncmd = \"true\"\n",
			} {
				if err := os.WriteFile(filepath.Join(project, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			workspace, err := prepareSystemWorkspace(source, t.TempDir(), strategy, []string{"subproject"}, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if strategy == systemsandbox.WorkspaceOverlay && workspace.mount.Strategy != systemsandbox.WorkspaceOverlay {
				t.Skipf("overlay preparation unavailable: %v", workspace.fallbackReason)
			}
			options := Options{SourceDir: source, Resolved: recipe.Resolved{SyncOut: []string{"subproject"}}}
			if err := workspace.syncSuccess(nil, options, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{
				".shadowtree.toml":         "profile = \"go\"\n",
				".shadowtree.local.toml":   "[vars]\nmode = \"local\"\n",
				".shadowtree/include.toml": "[recipes.check]\ncmd = \"true\"\n",
			} {
				assertFileContent(t, filepath.Join(project, filepath.FromSlash(name)), want)
			}
		})
	}
}

func TestSystemWorkspaceRejectsUnsafeWholeSyncProtectedExclusion(t *testing.T) {
	source := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(source, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	_, err := prepareSystemWorkspace(source, t.TempDir(), systemsandbox.WorkspaceCopied, nil, true, nil)
	if err == nil || !strings.Contains(err.Error(), `protecting excluded path "pipe"`) {
		t.Fatalf("prepareSystemWorkspace error = %v, want protected sync boundary rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(source, "pipe")); statErr != nil {
		t.Fatalf("protected source path changed after rejection: %v", statErr)
	}
}

func TestSystemWorkspaceRejectsSelectedProtectedExclusions(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(source, "runtime", "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []systemsandbox.WorkspaceStrategy{systemsandbox.WorkspaceOverlay, systemsandbox.WorkspaceCopied} {
		for _, selected := range []string{"runtime/pipe", "runtime", ".git"} {
			t.Run(string(strategy)+"/"+strings.ReplaceAll(selected, "/", "-"), func(t *testing.T) {
				_, err := prepareSystemWorkspace(source, t.TempDir(), strategy, []string{selected}, false, nil)
				if err == nil || !strings.Contains(err.Error(), "protected path") || !strings.Contains(err.Error(), selected) {
					t.Fatalf("prepareSystemWorkspace error = %v, want protected overlap for %q", err, selected)
				}
			})
		}
	}
	for _, name := range []string{"runtime/pipe", ".git"} {
		if _, err := os.Stat(filepath.Join(source, name)); err != nil {
			t.Fatalf("protected source path %s changed after rejection: %v", name, err)
		}
	}
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
		DependencySeeds: []systemsandbox.DependencySeed{{SourcePath: seed, TargetPath: "dependencies", Provider: "npm"}},
	}
	if code := runSystemHelperPlan(t, plan); code != 0 {
		t.Fatalf("SystemHelperMain code = %d", code)
	}
	assertFileContent(t, filepath.Join(source, "dependencies", "node_modules", "tool", "index.js"), "seed")
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

func TestSystemHelperReplacesStaleDependencySeedTarget(t *testing.T) {
	source := t.TempDir()
	seed := t.TempDir()
	if err := os.WriteFile(filepath.Join(seed, "current"), []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "node_modules", "stale"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("root", recipe.Recipe{Cmd: recipe.Command{"true"}, Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	plan := systemLifecyclePlan{Protocol: systemHelperProtocol, Resolved: resolved, SourceDir: source, DependencySeeds: []systemsandbox.DependencySeed{{SourcePath: seed, TargetPath: "node_modules", Provider: "npm"}}}
	if code := runSystemHelperPlan(t, plan); code != 0 {
		t.Fatalf("SystemHelperMain code = %d", code)
	}
	assertFileContent(t, filepath.Join(source, "node_modules", "current"), "current")
	if _, err := os.Stat(filepath.Join(source, "node_modules", "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale dependency survived seed replacement: %v", err)
	}
}

func TestCopiedSystemWorkspaceOmitsDependencySeedTarget(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(source, "webui", "node_modules")
	cache := filepath.Join(target, ".vite")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "stale"), []byte("lower"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cache, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(cache, 0o755) }()

	workspace, err := prepareSystemWorkspace(
		source,
		t.TempDir(),
		systemsandbox.WorkspaceCopied,
		nil,
		false,
		[]systemsandbox.DependencySeed{{
			Provider: "bun", SourcePath: "/opt/shadowtree/dependencies/webui/node_modules", TargetPath: "webui/node_modules",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace.copyRoot, "webui", "node_modules")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("copied workspace retained dependency seed target: %v", err)
	}
	assertFileContent(t, filepath.Join(cache, "stale"), "lower")
}

func TestOverlayCleanupResultUsesSuccessfulFinalRetry(t *testing.T) {
	initialErr := errors.New("initial host removal")
	runtimeErr := errors.New("runtime cleanup transport")
	retryErr := errors.New("final host removal")
	if err := overlayCleanupResult(initialErr, runtimeErr, nil); err != nil {
		t.Fatalf("successful final retry retained cleanup error: %v", err)
	}
	err := overlayCleanupResult(initialErr, runtimeErr, retryErr)
	for _, want := range []error{initialErr, runtimeErr, retryErr} {
		if !errors.Is(err, want) {
			t.Fatalf("failed final retry omitted %v: %v", want, err)
		}
	}
}

func TestPrepareSystemWorkspaceRejectsUnsafeDependencySeedTargets(t *testing.T) {
	for name, seeds := range map[string][]systemsandbox.DependencySeed{
		"workspace root alias": {
			{Provider: "bun", SourcePath: "/opt/shadowtree/dependencies/node_modules", TargetPath: "webui/.."},
		},
		"canonical overlap": {
			{Provider: "bun", SourcePath: "/opt/shadowtree/dependencies/webui/node_modules", TargetPath: "webui/node_modules"},
			{Provider: "npm", SourcePath: "/opt/shadowtree/dependencies/webui/package/node_modules", TargetPath: "webui/./node_modules/package"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := prepareSystemWorkspace(t.TempDir(), t.TempDir(), systemsandbox.WorkspaceCopied, nil, false, seeds)
			if err == nil {
				t.Fatal("prepareSystemWorkspace accepted unsafe dependency seed targets")
			}
		})
	}
}

func TestSystemHelperRejectsSymlinkedDependencySeedTarget(t *testing.T) {
	source := t.TempDir()
	external := t.TempDir()
	seed := t.TempDir()
	if err := os.WriteFile(filepath.Join(seed, "payload"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(source, "linked")); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("root", recipe.Recipe{Cmd: recipe.Command{"true"}, Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	plan := systemLifecyclePlan{Protocol: systemHelperProtocol, Resolved: resolved, SourceDir: source, DependencySeeds: []systemsandbox.DependencySeed{{SourcePath: seed, TargetPath: "linked/node_modules", Provider: "npm"}}}
	if code := runSystemHelperPlan(t, plan); code != 1 {
		t.Fatalf("SystemHelperMain code = %d, want symlink rejection", code)
	}
	if _, err := os.Stat(filepath.Join(external, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("dependency seed escaped through symlink: %v", err)
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
