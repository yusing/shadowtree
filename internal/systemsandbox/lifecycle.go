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

const lifecyclePrivateTempMount = "/tmp:rw,exec,nosuid,nodev,mode=1777"

// LifecycleOptions describes one ephemeral system-container invocation.
type LifecycleOptions struct {
	Image         string
	Platform      string
	Workspace     WorkspaceMount
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

// WorkspaceMount describes either a copied private tree or an OverlayFS lower,
// upper, and work set prepared by the runner. Runtime adapters only expose this
// view; they do not interpret or synchronize its changes.
type WorkspaceMount struct {
	Strategy WorkspaceStrategy
	Source   string
	Upper    string
	Work     string
}

// WorkspaceSetupError reports a failure that happened before container user
// code could start. Callers may safely retry the lifecycle with a copied
// private workspace when this error wraps an overlay strategy.
type WorkspaceSetupError struct {
	err error
}

func (err WorkspaceSetupError) Error() string { return err.err.Error() }
func (err WorkspaceSetupError) Unwrap() error { return err.err }

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
	workspaceVolume := ""
	if options.Workspace.Strategy == WorkspaceOverlay && runtime == Docker {
		workspaceVolume = name + "-workspace"
	}
	workspaceArgs, err := workspaceMountArgs(runtime, options.Workspace, options.WorkspacePath, workspaceVolume, options.Confinement.SELinux)
	if err != nil {
		if options.Workspace.Strategy == WorkspaceOverlay {
			return WorkspaceSetupError{err: err}
		}
		return err
	}
	if workspaceVolume != "" {
		if err := createDockerOverlayVolume(ctx, runtime, workspaceVolume, options.Workspace, control); err != nil {
			if cleanupErr := removeWorkspaceVolume(ctx, runtime, workspaceVolume, control); cleanupErr != nil {
				return errors.Join(err, fmt.Errorf("remove possibly created system workspace volume: %w", cleanupErr))
			}
			return WorkspaceSetupError{err: err}
		}
		defer func() {
			if cleanupErr := removeWorkspaceVolume(ctx, runtime, workspaceVolume, control); cleanupErr != nil {
				if setupErr, ok := errors.AsType[WorkspaceSetupError](runErr); ok {
					runErr = setupErr.err
				}
				runErr = errors.Join(runErr, fmt.Errorf("remove system workspace volume: %w", cleanupErr))
				return
			}
			if options.Progress != nil {
				fmt.Fprintln(options.Progress, "shadowtree: system workspace volume cleanup complete")
			}
		}()
	}
	createArgs := []string{
		"create", "--interactive", "--name", name,
		"--read-only", "--platform", options.Platform, "--user", options.Confinement.User,
		"--tmpfs", lifecyclePrivateTempMount,
	}
	if options.Confinement.UserNamespace != "" {
		createArgs = append(createArgs, "--userns", options.Confinement.UserNamespace)
	}
	createArgs = append(createArgs, workspaceArgs...)
	for _, mount := range []struct {
		host, destination string
		readOnly          bool
	}{
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
		cleanupOutput, cleanupErr := control(cleanupCtx, string(runtime), "rm", "--force", name)
		cancel()
		if cleanupErr != nil && containerMissing(cleanupOutput) {
			cleanupErr = nil
		}
		if cause := context.Cause(ctx); cause != nil {
			return errors.Join(cause, cleanupErr)
		}
		redactions := []string{
			name, workspaceVolume, options.Image, options.Platform, options.Workspace.Source, options.Workspace.Upper, options.Workspace.Work, options.WorkspacePath,
			options.HelperHost, options.PlanHost, options.ExportHost, options.Confinement.User,
			options.Confinement.UserNamespace,
		}
		for _, cache := range options.Caches {
			redactions = append(redactions, cache.Name, cache.MountPath)
		}
		var createErr error
		if output := redactedCommandDiagnostic(createOutput, redactions...); output != "" {
			createErr = fmt.Errorf("runtime %s create system container: %s; verify image, mount, identity, and runtime storage compatibility, then retry: %w", runtime, output, err)
		} else {
			createErr = fmt.Errorf("runtime %s create system container; verify image, mount, identity, and runtime storage compatibility, then retry: %w", runtime, err)
		}
		if cleanupErr != nil {
			return errors.Join(createErr, fmt.Errorf("remove possibly created system container: %w", cleanupErr))
		}
		if options.Workspace.Strategy == WorkspaceOverlay {
			return WorkspaceSetupError{err: createErr}
		}
		return createErr
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
		runErr = errors.Join(runErr, fmt.Errorf("remove system container: %w", cleanupErr))
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

func removeWorkspaceVolume(ctx context.Context, runtime RuntimeName, name string, control commandRunner) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleStopTimeout)
	output, err := control(cleanupCtx, string(runtime), "volume", "rm", name)
	cancel()
	if err != nil && !volumeMissing(output) {
		return err
	}
	return nil
}

func containerMissing(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "no such container") || strings.Contains(message, "container not found") || strings.Contains(message, "no such object")
}

func workspaceMountArgs(runtime RuntimeName, workspace WorkspaceMount, destination, dockerVolume string, selinux bool) ([]string, error) {
	if !path.IsAbs(destination) {
		return nil, fmt.Errorf("system workspace mount requires an absolute container path: %q", destination)
	}
	switch workspace.Strategy {
	case WorkspaceCopied:
		return bindMountArgs(workspace.Source, destination, false, selinux)
	case WorkspaceOverlay:
		if selinux {
			return nil, errors.New("system overlay workspace is unavailable with SELinux labelling")
		}
		for _, item := range []struct {
			label string
			value string
		}{
			{label: "source", value: workspace.Source},
			{label: "upper", value: workspace.Upper},
			{label: "work", value: workspace.Work},
		} {
			if !filepath.IsAbs(item.value) {
				return nil, fmt.Errorf("system overlay %s path must be absolute: %q", item.label, item.value)
			}
			if strings.ContainsAny(item.value, ":,\n\r") {
				return nil, fmt.Errorf("system overlay %s path contains an unsupported delimiter: %q", item.label, item.value)
			}
		}
		switch runtime {
		case Podman:
			return []string{"--volume", workspace.Source + ":" + destination + ":O,upperdir=" + workspace.Upper + ",workdir=" + workspace.Work}, nil
		case Docker:
			if dockerVolume == "" || strings.ContainsAny(dockerVolume, ",\n\r") {
				return nil, errors.New("system Docker overlay volume has an invalid identity")
			}
			return []string{"--mount", "type=volume,src=" + dockerVolume + ",dst=" + destination + ",volume-nocopy"}, nil
		default:
			return nil, fmt.Errorf("runtime %s cannot expose an overlay system workspace", runtime)
		}
	default:
		return nil, fmt.Errorf("unsupported system workspace strategy %q", workspace.Strategy)
	}
}

func createDockerOverlayVolume(ctx context.Context, runtime RuntimeName, name string, workspace WorkspaceMount, control commandRunner) error {
	args := []string{
		"volume", "create",
		"--driver", "local",
		"--label", "shadowtree.owner=github.com/yusing/shadowtree",
		"--label", "shadowtree.kind=system-workspace",
		"--label", "shadowtree.invocation=" + name,
		"--opt", "type=overlay",
		"--opt", "device=overlay",
		"--opt", "o=lowerdir=" + workspace.Source + ",upperdir=" + workspace.Upper + ",workdir=" + workspace.Work + ",userxattr",
		name,
	}
	createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleCreateTimeout)
	output, err := control(createCtx, string(runtime), args...)
	cancel()
	if err == nil {
		return nil
	}
	redactions := []string{name, workspace.Source, workspace.Upper, workspace.Work}
	if diagnostic := redactedCommandDiagnostic(output, redactions...); diagnostic != "" {
		return fmt.Errorf("runtime %s create overlay workspace volume: %s: %w", runtime, diagnostic, err)
	}
	return fmt.Errorf("runtime %s create overlay workspace volume: %w", runtime, err)
}

// CleanupOverlayWorkspace uses the selected runtime only when host cleanup
// cannot remove runtime-owned OverlayFS work state.
func CleanupOverlayWorkspace(ctx context.Context, runtime RuntimeName, image, overlayRoot string, progress io.Writer) error {
	if runtime != Docker && runtime != Podman {
		return fmt.Errorf("runtime %s does not own host-inaccessible overlay cleanup", runtime)
	}
	if !filepath.IsAbs(overlayRoot) || strings.ContainsAny(overlayRoot, ",\n\r") {
		return fmt.Errorf("system overlay cleanup path is unsafe: %q", overlayRoot)
	}
	args := []string{
		"run", "--rm", "--network", "none", "--read-only", "--user", "0:0",
	}
	if runtime == Podman {
		args = append(args, "--userns", "host")
	}
	args = append(args,
		"--mount", "type=bind,src="+overlayRoot+",dst=/opt/shadowtree/cleanup",
		image, "/bin/rm", "-rf", "/opt/shadowtree/cleanup/upper", "/opt/shadowtree/cleanup/work",
	)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleCreateTimeout)
	output, err := directCommand(cleanupCtx, string(runtime), args...)
	cancel()
	if err != nil {
		if diagnostic := redactedCommandDiagnostic(output, image, overlayRoot); diagnostic != "" {
			return fmt.Errorf("runtime %s clean overlay workspace: %s: %w", runtime, diagnostic, err)
		}
		return fmt.Errorf("runtime %s clean overlay workspace: %w", runtime, err)
	}
	if progress != nil {
		fmt.Fprintln(progress, "shadowtree: runtime-owned overlay cleanup complete")
	}
	return nil
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
