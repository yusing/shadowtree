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
			Image: "image:test", Platform: "linux/amd64", User: "1000:1000",
			WorkspaceHost: workspace, WorkspacePath: "/workspace", HelperHost: helper, PlanHost: plan,
			Progress: &progress,
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
		Image: "image:test", Platform: "linux/amd64", User: "1000:1000",
		WorkspaceHost: t.TempDir(), WorkspacePath: "/workspace", HelperHost: helper, PlanHost: plan,
	}
}
