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

// WorkspaceStrategy identifies how a runtime can expose a private writable
// project view without making the source checkout writable.
type WorkspaceStrategy string

const (
	WorkspaceCopied  WorkspaceStrategy = "copied"
	WorkspaceOverlay WorkspaceStrategy = "overlay"
)

// RuntimeCandidates returns system runtime candidates in probe order.
func RuntimeCandidates() []RuntimeName {
	return []RuntimeName{Docker, Podman, Nerdctl}
}

const probeTimeout = 5 * time.Second

const (
	probeWaitDelay   = time.Second
	probeOutputLimit = 64 << 10
	buildOutputLimit = 4 << 20
)

const buildOutputTruncation = "shadowtree: earlier container build output omitted\n"

type commandRunner func(context.Context, string, ...string) ([]byte, error)
type buildCommandRunner func(context.Context, string, ...string) ([]byte, error)

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
	{phase: "nested, read-only, and executable tmpfs mounts, UID/GID, signalling identity, SELinux relabelling, and stdin", args: []string{"create", "--help"}, requiredOptions: []string{"--mount", "--tmpfs", "--read-only", "--user", "--platform", "--name", "--interactive"}},
	{phase: "attached container start", args: []string{"start", "--help"}, requiredOptions: []string{"--attach", "--interactive"}},
	{phase: "container signalling", args: []string{"kill", "--help"}, requiredOptions: []string{"--signal"}},
	{phase: "forced container removal", args: []string{"rm", "--help"}, requiredOptions: []string{"--force"}},
}

// RuntimeSelection captures one usable runtime and the confinement policy
// derived from the same observed engine security state.
type RuntimeSelection struct {
	Name              RuntimeName
	Confinement       ConfinementPolicy
	WorkspaceStrategy WorkspaceStrategy
}

// Detect probes supported runtimes in stable order and returns the first usable
// direct-argv adapter. It creates no images, volumes, workspaces, or containers.
func Detect(ctx context.Context, progress io.Writer) (RuntimeSelection, error) {
	return detect(ctx, progress, RuntimeCandidates(), os.Getuid(), os.Getgid(), directCommand)
}

func detect(ctx context.Context, progress io.Writer, candidates []RuntimeName, uid, gid int, run commandRunner) (RuntimeSelection, error) {
	if progress == nil {
		progress = io.Discard
	}
	failures := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if err := context.Cause(ctx); err != nil {
			return RuntimeSelection{}, err
		}
		_, _ = fmt.Fprintf(progress, "shadowtree: detecting system runtime %s\n", candidate)
		security, err := probe(ctx, candidate, run)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return RuntimeSelection{}, cause
			}
			failure := fmt.Sprintf("%s: %v", candidate, err)
			failures = append(failures, failure)
			_, _ = fmt.Fprintf(progress, "shadowtree: system runtime rejected: %s\n", failure)
			continue
		}
		policy, err := confinementPolicy(candidate, security, uid, gid)
		if err != nil {
			failure := fmt.Sprintf("%s: rootless UID/GID mapping: %v", candidate, err)
			failures = append(failures, failure)
			_, _ = fmt.Fprintf(progress, "shadowtree: system runtime rejected: %s\n", failure)
			continue
		}
		strategy := workspaceStrategy(candidate, policy, security.overlayWorkspace)
		_, _ = fmt.Fprintf(progress, "shadowtree: selected system runtime %s with %s workspace\n", candidate, strategy)
		return RuntimeSelection{Name: candidate, Confinement: policy, WorkspaceStrategy: strategy}, nil
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
		if len(probe.args) == 3 && probe.args[0] == "volume" && probe.args[1] == "create" && name == Docker {
			_, hasDriver := options["--driver"]
			_, hasOptions := options["--opt"]
			security.overlayWorkspace = hasDriver && hasOptions
		}
		if len(probe.args) == 2 && probe.args[0] == "create" {
			if security.rootless && name == Podman {
				required = append(required, "--userns")
			}
			if name == Podman {
				required = append(required, "--volume")
				security.overlayWorkspace = true
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

func workspaceStrategy(runtime RuntimeName, confinement ConfinementPolicy, overlayCapable bool) WorkspaceStrategy {
	if confinement.SELinux {
		return WorkspaceCopied
	}
	if overlayCapable && (runtime == Docker || runtime == Podman) {
		return WorkspaceOverlay
	}
	return WorkspaceCopied
}

type runtimeSecurity struct {
	rootless                 bool
	selinux                  bool
	overlayWorkspace         bool
	rootHostUID, rootHostGID int
	hasRootMapping           bool
}

// ConfinementPolicy is the runtime-specific identity and bind-labelling policy
// required for one lifecycle.
type ConfinementPolicy struct {
	User          string
	UserNamespace string
	SELinux       bool
}

func confinementPolicy(runtime RuntimeName, security runtimeSecurity, uid, gid int) (ConfinementPolicy, error) {
	policy := ConfinementPolicy{User: fmt.Sprintf("%d:%d", uid, gid), SELinux: security.selinux}
	if !security.rootless {
		return policy, nil
	}
	if runtime == Podman {
		if !security.hasRootMapping || security.rootHostUID != uid || security.rootHostGID != gid {
			return ConfinementPolicy{}, fmt.Errorf("container root maps to host %d:%d, want %d:%d", security.rootHostUID, security.rootHostGID, uid, gid)
		}
		policy.UserNamespace = "host"
	}
	policy.User = "0:0"
	return policy, nil
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
				IDMappings struct {
					UIDMap []runtimeIDMap `json:"uidmap"`
					GIDMap []runtimeIDMap `json:"gidmap"`
				} `json:"idMappings"`
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
		security := runtimeSecurity{rootless: *document.Host.Security.Rootless, selinux: *document.Host.Security.SELinuxEnabled}
		if security.rootless {
			uid, uidOK := runtimeRootMapping(document.Host.IDMappings.UIDMap)
			gid, gidOK := runtimeRootMapping(document.Host.IDMappings.GIDMap)
			if !uidOK || !gidOK {
				return runtimeSecurity{}, errors.New("runtime security inspection omitted rootless UID/GID mappings")
			}
			security.rootHostUID, security.rootHostGID, security.hasRootMapping = uid, gid, true
		}
		return security, nil
	default:
		return runtimeSecurity{}, fmt.Errorf("unsupported system runtime %q", runtime)
	}
}

type runtimeIDMap struct {
	ContainerID int `json:"container_id"`
	HostID      int `json:"host_id"`
	Size        int `json:"size"`
}

func runtimeRootMapping(mappings []runtimeIDMap) (int, bool) {
	for _, mapping := range mappings {
		if mapping.ContainerID == 0 && mapping.Size > 0 {
			return mapping.HostID, true
		}
	}
	return 0, false
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

func bufferedBuildCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = probeWaitDelay
	output := tailBuffer{limit: buildOutputLimit}
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	if runErr != nil {
		return output.Bytes(), runErr
	}
	return nil, nil
}

type tailBuffer struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	start     int
	truncated bool
}

func (buffer *tailBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	written := len(data)
	if buffer.limit <= 0 || len(data) == 0 {
		return written, nil
	}
	if len(data) >= buffer.limit {
		buffer.buf = append(buffer.buf[:0], data[len(data)-buffer.limit:]...)
		buffer.start = 0
		buffer.truncated = true
		return written, nil
	}
	if available := buffer.limit - len(buffer.buf); available > 0 {
		n := min(available, len(data))
		buffer.buf = append(buffer.buf, data[:n]...)
		data = data[n:]
		if len(data) == 0 {
			return written, nil
		}
	}
	first := min(len(data), buffer.limit-buffer.start)
	copy(buffer.buf[buffer.start:], data[:first])
	copy(buffer.buf, data[first:])
	buffer.start = (buffer.start + len(data)) % buffer.limit
	buffer.truncated = true
	return written, nil
}

func (buffer *tailBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if !buffer.truncated {
		return bytes.Clone(buffer.buf)
	}
	output := make([]byte, 0, len(buildOutputTruncation)+len(buffer.buf))
	output = append(output, buildOutputTruncation...)
	output = append(output, buffer.buf[buffer.start:]...)
	return append(output, buffer.buf[:buffer.start]...)
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
	message := commandDiagnostic(output)
	if message == "" {
		return err.Error()
	}
	return fmt.Sprintf("%v: %s", err, message)
}

func commandDiagnostic(output []byte) string {
	message := strings.TrimSpace(string(output))
	if len(message) > 240 {
		message = message[:240] + "..."
	}
	return strings.Join(strings.Fields(message), " ")
}

func redactedCommandDiagnostic(output []byte, values ...string) string {
	message := string(output)
	for _, value := range values {
		if value != "" {
			message = strings.ReplaceAll(message, value, "<redacted>")
		}
	}
	return commandDiagnostic([]byte(message))
}
