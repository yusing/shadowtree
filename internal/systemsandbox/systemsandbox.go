// Package systemsandbox owns system-container runtime integration, immutable
// image planning, and project-scoped mutable cache contracts.
package systemsandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
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
	probeOutputLimit = 4 << 10
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
	{phase: "nested and read-only mounts, UID/GID, signalling identity, and stdin", args: []string{"create", "--help"}, requiredOptions: []string{"--mount", "--read-only", "--user", "--platform", "--name", "--interactive"}},
	{phase: "attached container start", args: []string{"start", "--help"}, requiredOptions: []string{"--attach", "--interactive"}},
	{phase: "container signalling", args: []string{"kill", "--help"}, requiredOptions: []string{"--signal"}},
	{phase: "forced container removal", args: []string{"rm", "--help"}, requiredOptions: []string{"--force"}},
}

// Detect probes supported runtimes in stable order and returns the first usable
// direct-argv adapter. It creates no images, volumes, workspaces, or containers.
func Detect(ctx context.Context, progress io.Writer) (RuntimeName, error) {
	return detect(ctx, progress, RuntimeCandidates(), directCommand)
}

func detect(ctx context.Context, progress io.Writer, candidates []RuntimeName, run commandRunner) (RuntimeName, error) {
	if progress == nil {
		progress = io.Discard
	}
	failures := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		fmt.Fprintf(progress, "shadowtree: detecting system runtime %s\n", candidate)
		if err := probe(ctx, candidate, run); err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return "", cause
			}
			failure := fmt.Sprintf("%s: %v", candidate, err)
			failures = append(failures, failure)
			fmt.Fprintf(progress, "shadowtree: system runtime rejected: %s\n", failure)
			continue
		}
		fmt.Fprintf(progress, "shadowtree: selected system runtime %s\n", candidate)
		return candidate, nil
	}
	return "", fmt.Errorf("no usable system runtime: %s", strings.Join(failures, "; "))
}

func probe(ctx context.Context, name RuntimeName, run commandRunner) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	for _, probe := range capabilityProbes {
		output, err := run(ctx, string(name), probe.args...)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%s probe timed out after %s", probe.phase, probeTimeout)
			}
			return fmt.Errorf("%s: %s", probe.phase, commandFailure(err, output))
		}
		options := helpOptions(output)
		for _, option := range probe.requiredOptions {
			if _, ok := options[option]; !ok {
				return fmt.Errorf("%s: help output lacks exact option %s", probe.phase, option)
			}
		}
	}
	return nil
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
