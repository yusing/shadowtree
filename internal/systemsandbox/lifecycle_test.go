package systemsandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunLifecycleSignalsContainerAndPreservesCancellation(t *testing.T) {
	bin := t.TempDir()
	state := filepath.Join(bin, "pid")
	removed := filepath.Join(bin, "removed")
	script := `#!/bin/sh
case "$1" in
  create)
    exit 0
    ;;
  start)
    printf '%s' "$$" > "` + state + `"
    trap 'exit 143' TERM
    while :; do sleep 1; done
    ;;
  kill)
    signal=TERM
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "--signal" ]; then shift; signal=$1; fi
      shift
    done
    kill -"$signal" "$(cat "` + state + `")"
    ;;
  rm)
    printf removed > "` + removed + `"
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	workspace := t.TempDir()
	helper := filepath.Join(t.TempDir(), "helper")
	plan := filepath.Join(t.TempDir(), "plan.json")
	for _, file := range []string{helper, plan} {
		if err := os.WriteFile(file, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	var progress bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- RunLifecycle(ctx, Docker, LifecycleOptions{
			Image: "image:test", Platform: "linux/amd64", Confinement: ConfinementPolicy{User: "1000:1000"},
			Workspace: WorkspaceMount{Strategy: WorkspaceCopied, Source: workspace}, WorkspacePath: "/workspace", HelperHost: helper, PlanHost: plan,
			ExportHost: t.TempDir(),
			Progress:   &progress,
		})
	}()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(state); err == nil {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("fake container did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunLifecycle error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunLifecycle did not stop after cancellation")
	}
	if !strings.Contains(progress.String(), "stopping system container") {
		t.Fatalf("progress missing graceful stop:\n%s", progress.String())
	}
	if _, err := os.Stat(removed); err != nil {
		t.Fatalf("container was not removed after cancellation: %v", err)
	}
}

func TestRunLifecycleRemovesContainerWhenAttachedClientFails(t *testing.T) {
	bin := t.TempDir()
	removed := filepath.Join(bin, "removed")
	script := `#!/bin/sh
case "$1" in
  create) exit 0 ;;
  start) exit 42 ;;
  rm) printf removed > "` + removed + `" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := RunLifecycle(t.Context(), Docker, lifecycleTestOptions(t))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 {
		t.Fatalf("RunLifecycle error = %v, want attached-client exit 42", err)
	}
	if _, err := os.Stat(removed); err != nil {
		t.Fatalf("container was not removed after attached-client failure: %v", err)
	}
}

func TestRunLifecycleReportsReadyAfterCreateBeforeAttachedOutput(t *testing.T) {
	bin := t.TempDir()
	created := filepath.Join(bin, "created")
	ready := filepath.Join(bin, "ready")
	script := `#!/bin/sh
case "$1" in
  create) printf created > "` + created + `" ;;
  start)
    test -f "` + ready + `" || exit 9
    printf recipe-output
    ;;
  rm) exit 0 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout bytes.Buffer
	options := lifecycleTestOptions(t)
	options.Stdout = &stdout
	options.Ready = func() error {
		if _, err := os.Stat(created); err != nil {
			return errors.New("container was not created before ready callback")
		}
		return os.WriteFile(ready, nil, 0o600)
	}
	if err := RunLifecycle(t.Context(), Docker, options); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "recipe-output" {
		t.Fatalf("attached stdout = %q, want recipe output", stdout.String())
	}
}

func TestRunLifecycleFinishesRegistrationAndRemovesBeforeReturningCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var removed bool
	err := runLifecycle(ctx, Docker, lifecycleTestOptions(t), func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "create":
			cancel()
			return nil, nil
		case "rm":
			removed = true
			return nil, nil
		default:
			t.Fatalf("unexpected runtime control command: %v", args)
			return nil, nil
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runLifecycle error = %v, want context cancellation", err)
	}
	if !removed {
		t.Fatal("registered container was not removed before cancellation returned")
	}
}

func TestRunLifecycleMountsPlannedCacheVolume(t *testing.T) {
	bin := t.TempDir()
	argsPath := filepath.Join(bin, "create-args")
	script := `#!/bin/sh
case "$1" in
  create) printf '%s' "$*" > "` + argsPath + `" ;;
  start) exit 0 ;;
  rm) exit 0 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	options := lifecycleTestOptions(t)
	options.Caches = []CachePlan{{Name: "shadowtree-cache-key", Key: "key", ProjectKey: "project", ProjectRoot: "/project", WorkspaceRoot: "/project", Provider: "go-build", Format: "go-build-v1", MountPath: "/opt/shadowtree/cache/go-build", Concurrency: "shared"}}
	if err := RunLifecycle(t.Context(), Docker, options); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "type=volume,src=shadowtree-cache-key,dst=/opt/shadowtree/cache/go-build") {
		t.Fatalf("create args missing cache mount: %s", data)
	}
}

func TestRunLifecycleMountsExecutablePrivateTemp(t *testing.T) {
	bin := t.TempDir()
	writeLifecycleRuntime(t, bin, Docker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var create []string
	err := runLifecycle(t.Context(), Docker, lifecycleTestOptions(t), func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "create" {
			create = append([]string(nil), args...)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(create, " ")
	if !strings.Contains(joined, "--read-only") || !strings.Contains(joined, "--tmpfs /tmp:rw,exec,nosuid,nodev,mode=1777") {
		t.Fatalf("create args do not preserve a read-only root with executable private temp: %s", joined)
	}
	if strings.Contains(joined, "type=tmpfs,dst=/tmp") {
		t.Fatalf("create args retain the implicitly noexec tmpfs mount: %s", joined)
	}
}

func TestRunLifecycleCreatesAndCleansDockerOverlayVolume(t *testing.T) {
	bin := t.TempDir()
	writeLifecycleRuntime(t, bin, Docker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	options := lifecycleTestOptions(t)
	options.Workspace = WorkspaceMount{
		Strategy: WorkspaceOverlay,
		Source:   t.TempDir(),
		Upper:    t.TempDir(),
		Work:     t.TempDir(),
	}
	var calls [][]string
	err := runLifecycle(t.Context(), Docker, options, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("runtime control calls = %v, want volume create, container create, container rm, volume rm", calls)
	}
	volumeName := calls[0][len(calls[0])-1]
	wantVolumeOptions := "o=lowerdir=" + options.Workspace.Source + ",upperdir=" + options.Workspace.Upper + ",workdir=" + options.Workspace.Work + ",userxattr"
	for _, want := range []string{
		"volume create --driver local --label shadowtree.owner=github.com/yusing/shadowtree --label shadowtree.kind=system-workspace --label shadowtree.invocation=" + volumeName + " --opt type=overlay --opt device=overlay --opt " + wantVolumeOptions + " " + volumeName,
		"--mount type=volume,src=" + volumeName + ",dst=" + options.WorkspacePath + ",volume-nocopy",
	} {
		joined := strings.Join(calls[0], " ") + "\n" + strings.Join(calls[1], " ")
		if !strings.Contains(joined, want) {
			t.Fatalf("Docker overlay calls missing %q:\n%s", want, joined)
		}
	}
	if calls[2][0] != "rm" || strings.Join(calls[3][:2], " ") != "volume rm" || calls[3][2] != volumeName {
		t.Fatalf("cleanup order = %v, want container before workspace volume", calls[2:])
	}
}

func TestRunLifecycleUsesPodmanNativeOverlayVolume(t *testing.T) {
	bin := t.TempDir()
	writeLifecycleRuntime(t, bin, Podman)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	options := lifecycleTestOptions(t)
	options.Confinement = ConfinementPolicy{User: "0:0", UserNamespace: "host"}
	options.Workspace = WorkspaceMount{
		Strategy: WorkspaceOverlay,
		Source:   t.TempDir(),
		Upper:    t.TempDir(),
		Work:     t.TempDir(),
	}
	var create []string
	err := runLifecycle(t.Context(), Podman, options, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "create" {
			create = append([]string(nil), args...)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := options.Workspace.Source + ":" + options.WorkspacePath + ":O,upperdir=" + options.Workspace.Upper + ",workdir=" + options.Workspace.Work
	joined := strings.Join(create, " ")
	for _, part := range []string{"--userns host", "--volume " + want} {
		if !strings.Contains(joined, part) {
			t.Fatalf("Podman overlay create missing %q: %s", part, joined)
		}
	}
	if strings.Contains(joined, options.Workspace.Source+":"+options.WorkspacePath+":Z") {
		t.Fatalf("Podman overlay relabelled the source lower: %s", joined)
	}
}

func TestRunLifecycleMarksOverlayCreateFailureSafeForCopyFallback(t *testing.T) {
	options := lifecycleTestOptions(t)
	options.Workspace = WorkspaceMount{Strategy: WorkspaceOverlay, Source: t.TempDir(), Upper: t.TempDir(), Work: t.TempDir()}
	wantErr := errors.New("overlay mount rejected")
	started := false
	err := runLifecycle(t.Context(), Podman, options, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "create" {
			return []byte("overlay mount rejected"), wantErr
		}
		if args[0] == "start" {
			started = true
		}
		return nil, nil
	})
	if _, ok := errors.AsType[WorkspaceSetupError](err); !ok || !errors.Is(err, wantErr) {
		t.Fatalf("runLifecycle error = %v, want WorkspaceSetupError wrapping create failure", err)
	}
	if started {
		t.Fatal("container user code started after overlay create failure")
	}
}

func TestRunLifecycleDoesNotMarkOverlayCreateFailureRetryableWhenContainerCleanupFails(t *testing.T) {
	options := lifecycleTestOptions(t)
	options.Workspace = WorkspaceMount{Strategy: WorkspaceOverlay, Source: t.TempDir(), Upper: t.TempDir(), Work: t.TempDir()}
	createErr := errors.New("overlay create failed")
	cleanupErr := errors.New("container cleanup failed")
	err := runLifecycle(t.Context(), Podman, options, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "create" {
			return nil, createErr
		}
		if args[0] == "rm" {
			return []byte("cleanup failed"), cleanupErr
		}
		return nil, nil
	})
	if _, ok := errors.AsType[WorkspaceSetupError](err); ok {
		t.Fatalf("runLifecycle error remained retryable after uncertain cleanup: %v", err)
	}
	for _, want := range []error{createErr, cleanupErr} {
		if !errors.Is(err, want) {
			t.Fatalf("runLifecycle error = %v, want %v", err, want)
		}
	}
}

func TestRunLifecycleCleansPossiblyCreatedDockerVolumeAfterCreateFailure(t *testing.T) {
	options := lifecycleTestOptions(t)
	options.Workspace = WorkspaceMount{Strategy: WorkspaceOverlay, Source: t.TempDir(), Upper: t.TempDir(), Work: t.TempDir()}
	wantErr := errors.New("volume create connection lost")
	removed := ""
	err := runLifecycle(t.Context(), Docker, options, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "volume" && args[1] == "create" {
			return nil, wantErr
		}
		if len(args) == 3 && args[0] == "volume" && args[1] == "rm" {
			removed = args[2]
		}
		return nil, nil
	})
	if _, ok := errors.AsType[WorkspaceSetupError](err); !ok || !errors.Is(err, wantErr) {
		t.Fatalf("runLifecycle error = %v, want cleanup-preserving WorkspaceSetupError", err)
	}
	if removed == "" || !strings.HasSuffix(removed, "-workspace") {
		t.Fatalf("possibly created workspace volume was not removed: %q", removed)
	}
}

func TestRunLifecycleDoesNotMarkDockerVolumeFailureRetryableWhenCleanupFails(t *testing.T) {
	options := lifecycleTestOptions(t)
	options.Workspace = WorkspaceMount{Strategy: WorkspaceOverlay, Source: t.TempDir(), Upper: t.TempDir(), Work: t.TempDir()}
	createErr := errors.New("volume create connection lost")
	cleanupErr := errors.New("volume cleanup failed")
	err := runLifecycle(t.Context(), Docker, options, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "volume" && args[1] == "create" {
			return nil, createErr
		}
		if len(args) >= 2 && args[0] == "volume" && args[1] == "rm" {
			return []byte("cleanup failed"), cleanupErr
		}
		return nil, nil
	})
	if _, ok := errors.AsType[WorkspaceSetupError](err); ok {
		t.Fatalf("runLifecycle error remained retryable after uncertain volume cleanup: %v", err)
	}
	for _, want := range []error{createErr, cleanupErr} {
		if !errors.Is(err, want) {
			t.Fatalf("runLifecycle error = %v, want %v", err, want)
		}
	}
}

func TestRunLifecycleJoinsContainerAndVolumeCleanupErrorsWithExit(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n  start) exit 23 ;;\n  *) exit 0 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, string(Docker)), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	options := lifecycleTestOptions(t)
	options.Workspace = WorkspaceMount{Strategy: WorkspaceOverlay, Source: t.TempDir(), Upper: t.TempDir(), Work: t.TempDir()}
	containerCleanupErr := errors.New("container cleanup failed")
	volumeCleanupErr := errors.New("volume cleanup failed")
	err := runLifecycle(t.Context(), Docker, options, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "rm" {
			return nil, containerCleanupErr
		}
		if len(args) >= 2 && args[0] == "volume" && args[1] == "rm" {
			return nil, volumeCleanupErr
		}
		return nil, nil
	})
	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exit.ExitCode() != 23 {
		t.Fatalf("runLifecycle error = %v, want attached exit 23", err)
	}
	for _, want := range []error{containerCleanupErr, volumeCleanupErr} {
		if !errors.Is(err, want) {
			t.Fatalf("runLifecycle error = %v, want joined cleanup error %v", err, want)
		}
	}
}

func TestRunLifecyclePreservesCreateDiagnosticAndRecovery(t *testing.T) {
	options := lifecycleTestOptions(t)
	wantErr := errors.New("exit status 1")
	err := runLifecycle(t.Context(), Nerdctl, options, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "create" {
			return []byte("snapshotter rejected bind mount: " + strings.Join(args, " ")), wantErr
		}
		return nil, nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runLifecycle error = %v, want wrapped error", err)
	}
	for _, want := range []string{"runtime nerdctl create system container", "snapshotter rejected bind mount", "verify image, mount, identity, and runtime storage compatibility, then retry"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("runLifecycle error = %v, want %q", err, want)
		}
	}
	for _, sensitive := range []string{options.Image, options.Platform, options.Workspace.Source, options.WorkspacePath, options.HelperHost, options.PlanHost, options.ExportHost, options.Confinement.User} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("runLifecycle error exposed %q: %v", sensitive, err)
		}
	}
}

func TestRunLifecycleAppliesMappedRootAndSELinuxRelabelling(t *testing.T) {
	bin := t.TempDir()
	argsPath := filepath.Join(bin, "create-args")
	script := `#!/bin/sh
case "$1" in
  create) printf '%s' "$*" > "` + argsPath + `" ;;
  start) exit 0 ;;
  rm) exit 0 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "podman"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	options := lifecycleTestOptions(t)
	options.Confinement = ConfinementPolicy{User: "0:0", UserNamespace: "host", SELinux: true}
	if err := RunLifecycle(t.Context(), Podman, options); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	arguments := string(data)
	for _, want := range []string{
		"--user 0:0 --tmpfs /tmp:rw,exec,nosuid,nodev,mode=1777 --userns host",
		"--volume " + options.Workspace.Source + ":" + options.WorkspacePath + ":Z",
		"--volume " + options.HelperHost + ":/opt/shadowtree/helper:ro,Z",
	} {
		if !strings.Contains(arguments, want) {
			t.Fatalf("create args missing %q: %s", want, arguments)
		}
	}
}

func TestBindMountArgsRejectsUnsafeDelimiters(t *testing.T) {
	if _, err := bindMountArgs("/host,escape", "/workspace", false, false); err == nil {
		t.Fatal("ordinary bind accepted comma delimiter")
	}
	if _, err := bindMountArgs("/host:escape", "/workspace", false, true); err == nil {
		t.Fatal("SELinux bind accepted colon delimiter")
	}
	if _, err := bindMountArgs("relative", "/workspace", false, false); err == nil {
		t.Fatal("bind accepted relative host path")
	}
}

func lifecycleTestOptions(t *testing.T) LifecycleOptions {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "helper")
	plan := filepath.Join(t.TempDir(), "plan.json")
	for _, file := range []string{helper, plan} {
		if err := os.WriteFile(file, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return LifecycleOptions{
		Image: "image:test", Platform: "linux/amd64", Confinement: ConfinementPolicy{User: "1000:1000"},
		Workspace: WorkspaceMount{Strategy: WorkspaceCopied, Source: t.TempDir()}, WorkspacePath: "/workspace", HelperHost: helper, PlanHost: plan, ExportHost: t.TempDir(),
	}
}

func writeLifecycleRuntime(t *testing.T, bin string, runtime RuntimeName) {
	t.Helper()
	script := "#!/bin/sh\ncase \"$1\" in\n  start) exit 0 ;;\n  *) exit 0 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, string(runtime)), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}
