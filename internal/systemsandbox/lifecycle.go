package systemsandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
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
	Confinement   ConfinementPolicy
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	Progress      io.Writer
	Ready         func() error
	Caches        []CachePlan
	ExportHost    string
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
	if options.Image == "" || options.Platform == "" || options.Confinement.User == "" {
		return errors.New("system lifecycle requires image, platform, and user identity")
	}
	if options.Confinement.UserNamespace != "" && options.Confinement.UserNamespace != "host" {
		return fmt.Errorf("unsupported system lifecycle user namespace %q", options.Confinement.UserNamespace)
	}
	name, err := lifecycleContainerName()
	if err != nil {
		return err
	}
	createArgs := []string{
		"create", "--interactive", "--name", name,
		"--read-only", "--platform", options.Platform, "--user", options.Confinement.User,
		"--mount", "type=tmpfs,dst=/tmp",
	}
	if options.Confinement.UserNamespace != "" {
		createArgs = append(createArgs, "--userns", options.Confinement.UserNamespace)
	}
	for _, mount := range []struct {
		host, destination string
		readOnly          bool
	}{
		{options.WorkspaceHost, options.WorkspacePath, false},
		{options.HelperHost, "/opt/shadowtree/helper", true},
		{options.PlanHost, "/opt/shadowtree/plan.json", true},
		{options.ExportHost, "/opt/shadowtree/export", false},
	} {
		args, err := bindMountArgs(mount.host, mount.destination, mount.readOnly, options.Confinement.SELinux)
		if err != nil {
			return err
		}
		createArgs = append(createArgs, args...)
	}
	for _, cache := range options.Caches {
		if err := validateCachePlan(cache); err != nil {
			return err
		}
		if strings.ContainsAny(cache.Name, ",\n\r") || strings.ContainsAny(cache.MountPath, ",\n\r") {
			return fmt.Errorf("cache %s volume or mount path contains an unsupported delimiter", cache.Provider)
		}
		createArgs = append(createArgs, "--mount", "type=volume,src="+cache.Name+",dst="+cache.MountPath)
	}
	createArgs = append(createArgs, options.Image, "/opt/shadowtree/helper", "__shadowtree_system_helper", "/opt/shadowtree/plan.json")
	if options.Progress != nil {
		fmt.Fprintln(options.Progress, "shadowtree: creating system container")
	}
	createCtx, cancelCreate := context.WithTimeout(context.WithoutCancel(ctx), lifecycleCreateTimeout)
	createOutput, err := control(createCtx, string(runtime), createArgs...)
	cancelCreate()
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleStopTimeout)
		_, _ = control(cleanupCtx, string(runtime), "rm", "--force", name)
		cancel()
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		redactions := []string{
			name, options.Image, options.Platform, options.WorkspaceHost, options.WorkspacePath,
			options.HelperHost, options.PlanHost, options.ExportHost, options.Confinement.User,
			options.Confinement.UserNamespace,
		}
		for _, cache := range options.Caches {
			redactions = append(redactions, cache.Name, cache.MountPath)
		}
		if output := redactedCommandDiagnostic(createOutput, redactions...); output != "" {
			return fmt.Errorf("runtime %s create system container: %s; verify image, mount, identity, and runtime storage compatibility, then retry: %w", runtime, output, err)
		}
		return fmt.Errorf("runtime %s create system container; verify image, mount, identity, and runtime storage compatibility, then retry: %w", runtime, err)
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
	if options.Ready != nil {
		if err := options.Ready(); err != nil {
			return fmt.Errorf("prepare attached system container output: %w", err)
		}
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

func bindMountArgs(host, destination string, readOnly, selinux bool) ([]string, error) {
	if !filepath.IsAbs(host) || !path.IsAbs(destination) {
		return nil, fmt.Errorf("system lifecycle bind mount requires absolute host and container paths: %q -> %q", host, destination)
	}
	if selinux {
		if strings.ContainsAny(host, ":\n\r") || strings.ContainsAny(destination, ":\n\r") {
			return nil, fmt.Errorf("system lifecycle SELinux bind path contains an unsupported delimiter: %q -> %q", host, destination)
		}
		options := "Z"
		if readOnly {
			options = "ro,Z"
		}
		return []string{"--volume", host + ":" + destination + ":" + options}, nil
	}
	if strings.ContainsAny(host, ",\n\r") || strings.ContainsAny(destination, ",\n\r") {
		return nil, fmt.Errorf("system lifecycle bind path contains an unsupported delimiter: %q -> %q", host, destination)
	}
	mount := "type=bind,src=" + host + ",dst=" + destination
	if readOnly {
		mount += ",readonly"
	}
	return []string{"--mount", mount}, nil
}

func lifecycleContainerName() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create system container identity: %w", err)
	}
	return "shadowtree-" + hex.EncodeToString(random[:]), nil
}
