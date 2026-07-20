//go:build linux

package systemsandbox

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCapableHostRuntimeConfinement is an opt-in compatibility gate intended
// to run once per supported engine. The image must already exist or be
// pullable by the selected runtime.
func TestCapableHostRuntimeConfinement(t *testing.T) {
	runtimeName := RuntimeName(os.Getenv("SHADOWTREE_CAPABLE_RUNTIME"))
	image := os.Getenv("SHADOWTREE_CAPABLE_IMAGE")
	if runtimeName == "" || image == "" {
		t.Skip("set SHADOWTREE_CAPABLE_RUNTIME and SHADOWTREE_CAPABLE_IMAGE")
	}
	if runtimeName != Docker && runtimeName != Podman && runtimeName != Nerdctl {
		t.Fatalf("unsupported SHADOWTREE_CAPABLE_RUNTIME %q", runtimeName)
	}
	security, err := probe(t.Context(), runtimeName, directCommand)
	if err != nil {
		t.Fatalf("probe %s: %v", runtimeName, err)
	}
	policy, err := confinementPolicy(runtimeName, security, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	invocation := t.TempDir()
	helper := filepath.Join(invocation, "helper")
	identity := filepath.Join(workspace, "identity")
	script := fmt.Sprintf("#!/bin/sh\nid -u | tr -d '\\n' > %q\nprintf ':' >> %q\nid -g >> %q\n", identity, identity, identity)
	if err := os.WriteFile(helper, []byte(script), 0o500); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(invocation, "plan.json")
	if err := os.WriteFile(plan, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = RunLifecycle(t.Context(), runtimeName, LifecycleOptions{
		Image: image, Platform: "linux/" + runtime.GOARCH, Confinement: policy,
		WorkspaceHost: workspace, WorkspacePath: workspace,
		HelperHost: helper, PlanHost: plan, ExportHost: t.TempDir(),
		Stdout: &output, Stderr: &output, Progress: &output,
	})
	if err != nil {
		t.Fatalf("confined lifecycle on %s: %v\n%s", runtimeName, err, output.String())
	}
	data, err := os.ReadFile(identity)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != policy.User {
		t.Fatalf("container identity = %q, policy = %q", got, policy.User)
	}
}
