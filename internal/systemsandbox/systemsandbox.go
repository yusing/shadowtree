// Package systemsandbox owns system-container runtime integration, immutable
// image planning, and project-scoped mutable cache contracts.
package systemsandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// RuntimeName identifies a supported external container runtime CLI.
type RuntimeName string

const (
	Docker  RuntimeName = "docker"
	Podman  RuntimeName = "podman"
	Nerdctl RuntimeName = "nerdctl"
)

// RuntimeCandidates returns system runtime candidates in probe order.
func RuntimeCandidates() []RuntimeName {
	return []RuntimeName{Docker, Podman, Nerdctl}
}

const probeTimeout = 5 * time.Second

const (
	probeWaitDelay   = time.Second
	probeOutputLimit = 64 << 10
)

type commandRunner func(context.Context, string, ...string) ([]byte, error)
type streamingCommandRunner func(context.Context, io.Writer, string, ...string) ([]byte, error)

type capabilityProbe struct {
	phase           string
	args            []string
	requiredOptions []string
}

var capabilityProbes = []capabilityProbe{
	{phase: "engine reachability", args: []string{"info"}},
	{phase: "image inspection", args: []string{"image", "inspect", "--help"}},
	{phase: "image tagging", args: []string{"image", "tag", "--help"}},
	{phase: "image building", args: []string{"build", "--help"}, requiredOptions: []string{"--file", "--tag", "--label", "--platform", "--secret", "--build-arg"}},
	{phase: "labelled volumes", args: []string{"volume", "create", "--help"}, requiredOptions: []string{"--label"}},
	{phase: "volume inspection", args: []string{"volume", "inspect", "--help"}, requiredOptions: []string{"--format"}},
	{phase: "filtered volume listing", args: []string{"volume", "ls", "--help"}, requiredOptions: []string{"--filter", "--format"}},
	{phase: "volume removal", args: []string{"volume", "rm", "--help"}},
	{phase: "container volume-use inspection", args: []string{"ps", "--help"}, requiredOptions: []string{"--filter", "--format"}},
	{phase: "nested and read-only mounts, UID/GID, signalling identity, SELinux relabelling, and stdin", args: []string{"create", "--help"}, requiredOptions: []string{"--mount", "--read-only", "--user", "--platform", "--name", "--interactive"}},
	{phase: "attached container start", args: []string{"start", "--help"}, requiredOptions: []string{"--attach", "--interactive"}},
	{phase: "container signalling", args: []string{"kill", "--help"}, requiredOptions: []string{"--signal"}},
	{phase: "forced container removal", args: []string{"rm", "--help"}, requiredOptions: []string{"--force"}},
}

// RuntimeSelection captures one usable runtime and the confinement policy
// derived from the same observed engine security state.
type RuntimeSelection struct {
	Name        RuntimeName
	Confinement ConfinementPolicy
}

// Detect probes supported runtimes in stable order and returns the first usable
// direct-argv adapter. It creates no images, volumes, workspaces, or containers.
func Detect(ctx context.Context, progress io.Writer) (RuntimeSelection, error) {
	return detect(ctx, progress, RuntimeCandidates(), directCommand)
}

func detect(ctx context.Context, progress io.Writer, candidates []RuntimeName, run commandRunner) (RuntimeSelection, error) {
	if progress == nil {
		progress = io.Discard
	}
	failures := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if err := context.Cause(ctx); err != nil {
			return RuntimeSelection{}, err
		}
		fmt.Fprintf(progress, "shadowtree: detecting system runtime %s\n", candidate)
		security, err := probe(ctx, candidate, run)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return RuntimeSelection{}, cause
			}
			failure := fmt.Sprintf("%s: %v", candidate, err)
			failures = append(failures, failure)
			fmt.Fprintf(progress, "shadowtree: system runtime rejected: %s\n", failure)
			continue
		}
		fmt.Fprintf(progress, "shadowtree: selected system runtime %s\n", candidate)
		return RuntimeSelection{Name: candidate, Confinement: confinementPolicy(candidate, security, os.Getuid(), os.Getgid())}, nil
	}
	return RuntimeSelection{}, fmt.Errorf("no usable system runtime: %s", strings.Join(failures, "; "))
}

func probe(ctx context.Context, name RuntimeName, run commandRunner) (runtimeSecurity, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var security runtimeSecurity
	for _, probe := range capabilityProbes {
		output, err := run(ctx, string(name), probe.args...)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return runtimeSecurity{}, fmt.Errorf("%s probe timed out after %s", probe.phase, probeTimeout)
			}
			return runtimeSecurity{}, fmt.Errorf("%s: %s", probe.phase, commandFailure(err, output))
		}
		if probe.phase == "engine reachability" {
			security, err = inspectRuntimeSecurity(ctx, name, run)
			if err != nil {
				return runtimeSecurity{}, err
			}
		}
		options := helpOptions(output)
		required := slices.Clone(probe.requiredOptions)
		if len(probe.args) == 2 && probe.args[0] == "create" {
			if security.rootless && name == Podman {
				required = append(required, "--userns")
			}
			if security.selinux {
				required = append(required, "--volume")
			}
		}
		for _, option := range required {
			if _, ok := options[option]; !ok {
				return runtimeSecurity{}, fmt.Errorf("%s: help output lacks exact option %s", probe.phase, option)
			}
		}
	}
	return security, nil
}

type runtimeSecurity struct {
	rootless bool
	selinux  bool
}

// ConfinementPolicy is the runtime-specific identity and bind-labelling policy
// required for one lifecycle.
type ConfinementPolicy struct {
	User          string
	UserNamespace string
	SELinux       bool
}

func confinementPolicy(runtime RuntimeName, security runtimeSecurity, uid, gid int) ConfinementPolicy {
	policy := ConfinementPolicy{User: fmt.Sprintf("%d:%d", uid, gid), SELinux: security.selinux}
	if !security.rootless {
		return policy
	}
	if runtime == Podman {
		policy.UserNamespace = "keep-id"
		return policy
	}
	policy.User = "0:0"
	return policy
}

func inspectRuntimeSecurity(ctx context.Context, runtime RuntimeName, run commandRunner) (runtimeSecurity, error) {
	switch runtime {
	case Docker, Nerdctl:
		output, err := run(ctx, string(runtime), "info", "--format", "{{json .SecurityOptions}}")
		if err != nil {
			return runtimeSecurity{}, fmt.Errorf("runtime security inspection: %s", commandFailure(err, output))
		}
		var options []string
		if err := json.Unmarshal(bytes.TrimSpace(output), &options); err != nil {
			return runtimeSecurity{}, fmt.Errorf("runtime security inspection returned malformed JSON: %w", err)
		}
		if options == nil {
			return runtimeSecurity{}, errors.New("runtime security inspection omitted security options")
		}
		var security runtimeSecurity
		for _, option := range options {
			name, _, _ := strings.Cut(option, ",")
			name = strings.TrimPrefix(name, "name=")
			security.rootless = security.rootless || name == "rootless"
			security.selinux = security.selinux || name == "selinux"
		}
		return security, nil
	case Podman:
		output, err := run(ctx, string(runtime), "info", "--format", "json")
		if err != nil {
			return runtimeSecurity{}, fmt.Errorf("runtime security inspection: %s", commandFailure(err, output))
		}
		var document struct {
			Host struct {
				Security struct {
					Rootless       *bool `json:"rootless"`
					SELinuxEnabled *bool `json:"selinuxEnabled"`
				} `json:"security"`
			} `json:"host"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(output), &document); err != nil {
			return runtimeSecurity{}, fmt.Errorf("runtime security inspection returned malformed JSON: %w", err)
		}
		if document.Host.Security.Rootless == nil || document.Host.Security.SELinuxEnabled == nil {
			return runtimeSecurity{}, errors.New("runtime security inspection omitted rootless or SELinux state")
		}
		return runtimeSecurity{rootless: *document.Host.Security.Rootless, selinux: *document.Host.Security.SELinuxEnabled}, nil
	default:
		return runtimeSecurity{}, fmt.Errorf("unsupported system runtime %q", runtime)
	}
}

func directCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = probeWaitDelay
	var output boundedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if cause := context.Cause(ctx); cause != nil {
		return output.buf.Bytes(), cause
	}
	return output.buf.Bytes(), err
}

func directStreamingCommand(ctx context.Context, progress io.Writer, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = probeWaitDelay
	output := commandOutput{progress: progress}
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if cause := context.Cause(ctx); cause != nil {
		return output.Bytes(), cause
	}
	return output.Bytes(), err
}

type commandOutput struct {
	mu       sync.Mutex
	progress io.Writer
	buf      boundedBuffer
}

func (output *commandOutput) Write(p []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	_, _ = output.buf.Write(p)
	if output.progress == nil {
		return len(p), nil
	}
	return output.progress.Write(p)
}

func (output *commandOutput) Bytes() []byte {
	output.mu.Lock()
	defer output.mu.Unlock()
	return bytes.Clone(output.buf.buf.Bytes())
}

type boundedBuffer struct {
	buf bytes.Buffer
}

func (buffer *boundedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := probeOutputLimit - buffer.buf.Len()
	if remaining > 0 {
		_, _ = buffer.buf.Write(p[:min(len(p), remaining)])
	}
	return written, nil
}

func helpOptions(output []byte) map[string]struct{} {
	options := map[string]struct{}{}
	for field := range strings.FieldsSeq(string(output)) {
		field = strings.Trim(field, ",")
		if strings.HasPrefix(field, "-") {
			options[field] = struct{}{}
		}
	}
	return options
}

func commandFailure(err error, output []byte) string {
	message := strings.TrimSpace(string(output))
	if len(message) > 240 {
		message = message[:240] + "..."
	}
	if message == "" {
		return err.Error()
	}
	return fmt.Sprintf("%v: %s", err, strings.Join(strings.Fields(message), " "))
}
