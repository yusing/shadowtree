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

	"github.com/yusing/shadowtree/internal/recipe"
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

// TestCapableHostComposedToolchains is an opt-in provider gate that builds the
// complete supported profile composition through the selected real runtime.
func TestCapableHostComposedToolchains(t *testing.T) {
	runtimeName := RuntimeName(os.Getenv("SHADOWTREE_CAPABLE_RUNTIME"))
	if runtimeName == "" {
		t.Skip("set SHADOWTREE_CAPABLE_RUNTIME")
	}
	if runtimeName != Docker && runtimeName != Podman && runtimeName != Nerdctl {
		t.Fatalf("unsupported SHADOWTREE_CAPABLE_RUNTIME %q", runtimeName)
	}
	root := compositionProject(t)
	manager := os.Getenv("SHADOWTREE_CAPABLE_NODE_MANAGER")
	if manager == "" {
		manager = "npm@11.4.2"
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"packageManager":"`+manager+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	request := compositionRequest(t, root, []string{recipe.GoProfile, recipe.NodeProfile, recipe.RustProfile})
	plan, err := PlanComposition(request, root)
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	if err := BuildImages(t.Context(), runtimeName, plan, &progress); err != nil {
		t.Fatalf("build composed toolchains on %s: %v\n%s", runtimeName, err, progress.String())
	}
	output, err := directCommand(t.Context(), string(runtimeName), "run", "--rm", "--network", "none", "--read-only", plan.FinalTag, "/bin/sh", "-c", "go version && (node --version || bun --version) && rustc --version")
	if err != nil {
		t.Fatalf("verify composed toolchains on %s: %s", runtimeName, commandFailure(err, output))
	}
	t.Logf("composed toolchains verified:\n%s", output)
}
