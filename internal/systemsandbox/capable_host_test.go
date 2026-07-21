//go:build linux

package systemsandbox

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
	"golang.org/x/sys/unix"
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
	script := fmt.Sprintf("#!/bin/sh\nset -eu\ntemporary=/tmp/shadowtree-capable-executable\nprintf '#!/bin/sh\\nexit 0\\n' > \"$temporary\"\nchmod 700 \"$temporary\"\n\"$temporary\"\nrm \"$temporary\"\nid -u | tr -d '\\n' > %q\nprintf ':' >> %q\nid -g >> %q\n", identity, identity, identity)
	if err := os.WriteFile(helper, []byte(script), 0o500); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(invocation, "plan.json")
	if err := os.WriteFile(plan, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if runtimeName == Podman {
		err = runCapablePodmanCopied(t, image, policy, WorkspaceMount{Strategy: WorkspaceCopied, Source: workspace}, workspace, helper, plan, t.TempDir(), &output)
	} else {
		err = RunLifecycle(t.Context(), runtimeName, LifecycleOptions{
			Image: image, Platform: "linux/" + runtime.GOARCH, Confinement: policy,
			Workspace: WorkspaceMount{Strategy: WorkspaceCopied, Source: workspace}, WorkspacePath: workspace,
			HelperHost: helper, PlanHost: plan, ExportHost: t.TempDir(),
			Stdout: &output, Stderr: &output, Progress: &output,
		})
	}
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

func runCapablePodmanCopied(t *testing.T, image string, policy ConfinementPolicy, workspace WorkspaceMount, destination, helper, plan, export string, output *bytes.Buffer) (runErr error) {
	t.Helper()
	name, err := lifecycleContainerName()
	if err != nil {
		return err
	}
	args := []string{
		"create", "--name", name, "--network", "none", "--read-only",
		"--platform", "linux/" + runtime.GOARCH, "--user", policy.User,
		"--tmpfs", lifecyclePrivateTempMount,
	}
	if policy.UserNamespace != "" {
		args = append(args, "--userns", policy.UserNamespace)
	}
	workspaceArgs, err := workspaceMountArgs(Podman, workspace, destination, "", policy.SELinux)
	if err != nil {
		return err
	}
	args = append(args, workspaceArgs...)
	for _, mount := range []struct {
		host, destination string
		readOnly          bool
	}{
		{host: helper, destination: "/opt/shadowtree/helper", readOnly: true},
		{host: plan, destination: "/opt/shadowtree/plan.json", readOnly: true},
		{host: export, destination: "/opt/shadowtree/export"},
	} {
		mountArgs, err := bindMountArgs(mount.host, mount.destination, mount.readOnly, policy.SELinux)
		if err != nil {
			return err
		}
		args = append(args, mountArgs...)
	}
	args = append(args, image, "/opt/shadowtree/helper", "__shadowtree_system_helper", "/opt/shadowtree/plan.json")
	if commandOutput, err := directCommand(t.Context(), string(Podman), args...); err != nil {
		return fmt.Errorf("create network-isolated Podman confinement proof: %s", commandFailure(err, commandOutput))
	}
	defer func() {
		_, cleanupErr := directCommand(t.Context(), string(Podman), "rm", "--force", name)
		runErr = errors.Join(runErr, cleanupErr)
	}()
	commandOutput, err := directCommand(t.Context(), string(Podman), "start", "--attach", name)
	output.Write(commandOutput)
	return err
}

// TestCapableHostOverlayWorkspace proves that a selected local Docker or
// Podman engine writes only to Shadowtree's upper layer. SELinux hosts select
// the copied strategy until a non-source-relabeling overlay contract exists.
func TestCapableHostOverlayWorkspace(t *testing.T) {
	runtimeName := RuntimeName(os.Getenv("SHADOWTREE_CAPABLE_RUNTIME"))
	image := os.Getenv("SHADOWTREE_CAPABLE_IMAGE")
	if runtimeName == "" || image == "" {
		t.Skip("set SHADOWTREE_CAPABLE_RUNTIME and SHADOWTREE_CAPABLE_IMAGE")
	}
	if runtimeName != Docker && runtimeName != Podman {
		t.Skip("overlay workspace is implemented by Docker and Podman")
	}
	security, err := probe(t.Context(), runtimeName, directCommand)
	if err != nil {
		t.Fatalf("probe %s: %v", runtimeName, err)
	}
	policy, err := confinementPolicy(runtimeName, security, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	if workspaceStrategy(runtimeName, policy, security.overlayWorkspace) != WorkspaceOverlay {
		t.Skip("runtime security state requires copied workspace fallback")
	}

	source := t.TempDir()
	for name, content := range map[string]string{"original.txt": "lower", "deleted.txt": "lower"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".shadowtree.toml"), []byte("profile = \"go\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dependencyTarget := filepath.Join(source, "webui", "node_modules")
	dependencyCache := filepath.Join(dependencyTarget, ".vite")
	if err := os.MkdirAll(dependencyCache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dependencyCache, "stale"), []byte("lower"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dependencyCache, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dependencyCache, 0o755) }()
	state := t.TempDir()
	upper := filepath.Join(state, "upper")
	work := filepath.Join(state, "work")
	for _, dir := range []string{upper, work} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	protectedGit := filepath.Join(upper, ".git")
	if err := os.WriteFile(protectedGit, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setxattr(protectedGit, "user.overlay.whiteout", []byte("y"), 0); err != nil {
		t.Skipf("user overlay whiteouts unavailable: %v", err)
	}
	dependencyWhiteout := filepath.Join(upper, "webui", "node_modules")
	if err := os.MkdirAll(filepath.Dir(dependencyWhiteout), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dependencyWhiteout, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setxattr(dependencyWhiteout, "user.overlay.whiteout", []byte("y"), 0); err != nil {
		t.Skipf("user overlay replacement whiteouts unavailable: %v", err)
	}
	invocation := t.TempDir()
	helper := filepath.Join(invocation, "helper")
	script := fmt.Sprintf("#!/bin/sh\nset -eu\ntest ! -e %q\ntest -f %q\ntest ! -e %q\nmkdir -p %q\nprintf seeded > %q\nprintf created > %q\nprintf changed > %q\nrm %q\n", filepath.Join(source, ".git"), filepath.Join(source, ".shadowtree.toml"), dependencyTarget, dependencyTarget, filepath.Join(dependencyTarget, "current"), filepath.Join(source, "created.txt"), filepath.Join(source, "original.txt"), filepath.Join(source, "deleted.txt"))
	if err := os.WriteFile(helper, []byte(script), 0o500); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(invocation, "plan.json")
	if err := os.WriteFile(plan, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	workspaceMount := WorkspaceMount{Strategy: WorkspaceOverlay, Source: source, Upper: upper, Work: work}
	if runtimeName == Podman {
		err = runCapablePodmanOverlay(t, image, policy, workspaceMount, source, strings.TrimPrefix(script, "#!/bin/sh\n"), &output)
	} else {
		err = RunLifecycle(t.Context(), runtimeName, LifecycleOptions{
			Image: image, Platform: "linux/" + runtime.GOARCH, Confinement: policy,
			Workspace: workspaceMount, WorkspacePath: source,
			HelperHost: helper, PlanHost: plan, ExportHost: t.TempDir(),
			Stdout: &output, Stderr: &output, Progress: &output,
		})
	}
	if err != nil {
		t.Fatalf("overlay lifecycle on %s: %v\n%s", runtimeName, err, output.String())
	}
	for name, want := range map[string]string{"original.txt": "lower", "deleted.txt": "lower"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil || string(data) != want {
			t.Fatalf("source lower %s = %q, %v; want %q", name, data, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(source, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("created output escaped into source lower: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, ".git")); err != nil {
		t.Fatalf("protected .git lower changed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dependencyCache, "stale"))
	if err != nil || string(data) != "lower" {
		t.Fatalf("dependency lower changed: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dependencyTarget, "current")); !os.IsNotExist(err) {
		t.Fatalf("seeded dependency escaped into source lower: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(upper, "webui", "node_modules", "current"))
	if err != nil || string(data) != "seeded" {
		t.Fatalf("dependency replacement missing from upper: %q, %v", data, err)
	}
	for _, name := range []string{"created.txt", "original.txt", "deleted.txt"} {
		if _, err := os.Lstat(filepath.Join(upper, name)); err != nil {
			t.Fatalf("upper layer missing %s: %v", name, err)
		}
	}
	if runtimeName == Docker || runtimeName == Podman {
		if err := CleanupOverlayWorkspace(t.Context(), runtimeName, image, state, &output); err != nil {
			t.Fatalf("clean rootful Docker overlay state: %v\n%s", err, output.String())
		}
	}
}

func runCapablePodmanOverlay(t *testing.T, image string, policy ConfinementPolicy, workspace WorkspaceMount, destination, script string, output *bytes.Buffer) (runErr error) {
	t.Helper()
	name, err := lifecycleContainerName()
	if err != nil {
		return err
	}
	mount, err := workspaceMountArgs(Podman, workspace, destination, "", false)
	if err != nil {
		return err
	}
	args := []string{
		"create", "--name", name, "--network", "none", "--read-only",
		"--platform", "linux/" + runtime.GOARCH, "--user", policy.User,
	}
	if policy.UserNamespace != "" {
		args = append(args, "--userns", policy.UserNamespace)
	}
	args = append(args, mount...)
	args = append(args, image, "/bin/sh", "-c", script)
	if commandOutput, err := directCommand(t.Context(), string(Podman), args...); err != nil {
		return fmt.Errorf("create network-isolated Podman overlay proof: %s", commandFailure(err, commandOutput))
	}
	defer func() {
		_, cleanupErr := directCommand(t.Context(), string(Podman), "rm", "--force", name)
		runErr = errors.Join(runErr, cleanupErr)
	}()
	commandOutput, err := directCommand(t.Context(), string(Podman), "start", "--attach", name)
	output.Write(commandOutput)
	return err
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
	if err := BuildImages(t.Context(), runtimeName, plan, ImageBuildOptions{Verbose: &progress}); err != nil {
		t.Fatalf("build composed toolchains on %s: %v\n%s", runtimeName, err, progress.String())
	}
	output, err := directCommand(t.Context(), string(runtimeName), "run", "--rm", "--network", "none", "--read-only", plan.FinalTag, "/bin/sh", "-c", "go version && (node --version || bun --version) && rustc --version")
	if err != nil {
		t.Fatalf("verify composed toolchains on %s: %s", runtimeName, commandFailure(err, output))
	}
	t.Logf("composed toolchains verified:\n%s", output)
}
