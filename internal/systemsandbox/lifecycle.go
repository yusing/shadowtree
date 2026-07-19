package systemsandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const lifecycleStopTimeout = 5 * time.Second

const lifecycleCreateTimeout = 30 * time.Second

// LifecycleOptions describes one ephemeral system-container invocation.
type LifecycleOptions struct {
	Image         string
	Platform      string
	WorkspaceHost string
	WorkspacePath string
	HelperHost    string
	PlanHost      string
	User          string
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	Progress      io.Writer
}

// RunLifecycle runs one named ephemeral container and preserves graceful
// cancellation long enough for the in-container helper to run post stages.
func RunLifecycle(ctx context.Context, runtime RuntimeName, options LifecycleOptions) error {
	return runLifecycle(ctx, runtime, options, directCommand)
}

func runLifecycle(ctx context.Context, runtime RuntimeName, options LifecycleOptions, control commandRunner) (runErr error) {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if options.Image == "" || options.Platform == "" || options.User == "" {
		return errors.New("system lifecycle requires image, platform, and user identity")
	}
	for label, value := range map[string]string{
		"workspace host": options.WorkspaceHost,
		"workspace path": options.WorkspacePath,
		"helper host":    options.HelperHost,
		"plan host":      options.PlanHost,
	} {
		if value == "" || strings.Contains(value, ",") {
			return fmt.Errorf("system lifecycle %s path is empty or contains an unsupported comma", label)
		}
	}
	name, err := lifecycleContainerName()
	if err != nil {
		return err
	}
	createArgs := []string{
		"create", "--interactive", "--name", name,
		"--read-only", "--platform", options.Platform, "--user", options.User,
		"--mount", "type=bind,src=" + options.WorkspaceHost + ",dst=" + options.WorkspacePath,
		"--mount", "type=bind,src=" + options.HelperHost + ",dst=/opt/shadowtree/helper,readonly",
		"--mount", "type=bind,src=" + options.PlanHost + ",dst=/opt/shadowtree/plan.json,readonly",
		"--mount", "type=tmpfs,dst=/tmp",
		options.Image, "/opt/shadowtree/helper", "__shadowtree_system_helper", "/opt/shadowtree/plan.json",
	}
	if options.Progress != nil {
		fmt.Fprintln(options.Progress, "shadowtree: creating system container")
	}
	createCtx, cancelCreate := context.WithTimeout(context.WithoutCancel(ctx), lifecycleCreateTimeout)
	_, err = control(createCtx, string(runtime), createArgs...)
	cancelCreate()
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleStopTimeout)
		_, _ = control(cleanupCtx, string(runtime), "rm", "--force", name)
		cancel()
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return fmt.Errorf("create system container: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleStopTimeout)
		_, cleanupErr := control(cleanupCtx, string(runtime), "rm", "--force", name)
		cancel()
		if cleanupErr == nil {
			if options.Progress != nil {
				fmt.Fprintln(options.Progress, "shadowtree: system container cleanup complete")
			}
			return
		}
		if runErr == nil {
			runErr = fmt.Errorf("remove system container: %w", cleanupErr)
			return
		}
		runErr = fmt.Errorf("%w; remove system container: %v", runErr, cleanupErr)
	}()
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	cmd := exec.Command(string(runtime), "start", "--attach", "--interactive", name)
	cmd.Stdin = options.Stdin
	cmd.Stdout = options.Stdout
	cmd.Stderr = options.Stderr
	if options.Progress != nil {
		fmt.Fprintln(options.Progress, "shadowtree: starting system container")
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start system container: %w", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	select {
	case err := <-wait:
		return err
	case <-ctx.Done():
	}
	if options.Progress != nil {
		fmt.Fprintln(options.Progress, "shadowtree: stopping system container")
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleStopTimeout)
	_, stopErr := control(stopCtx, string(runtime), "kill", "--signal", "TERM", name)
	cancel()
	if stopErr != nil && options.Progress != nil {
		fmt.Fprintf(options.Progress, "shadowtree: graceful container stop failed: %v\n", stopErr)
	}
	timer := time.NewTimer(lifecycleStopTimeout)
	defer timer.Stop()
	select {
	case <-wait:
		return context.Cause(ctx)
	case <-timer.C:
	}
	if options.Progress != nil {
		fmt.Fprintln(options.Progress, "shadowtree: forcing system container cleanup")
	}
	killCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleStopTimeout)
	_, killErr := control(killCtx, string(runtime), "kill", "--signal", "KILL", name)
	cancel()
	if killErr != nil && options.Progress != nil {
		fmt.Fprintf(options.Progress, "shadowtree: forced container stop failed: %v\n", killErr)
	}
	select {
	case <-wait:
		return context.Cause(ctx)
	case <-time.After(lifecycleStopTimeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-wait:
			return context.Cause(ctx)
		case <-time.After(lifecycleStopTimeout):
			return fmt.Errorf("system runtime client did not exit after forced cleanup: %w", context.Cause(ctx))
		}
	}
}

func lifecycleContainerName() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create system container identity: %w", err)
	}
	return "shadowtree-" + hex.EncodeToString(random[:]), nil
}
