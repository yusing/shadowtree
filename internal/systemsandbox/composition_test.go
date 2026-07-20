package systemsandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
)

func TestPlanCompositionCombinesEverySupportedProfileOnTrixie(t *testing.T) {
	root := compositionProject(t)
	request := compositionRequest(t, root, []string{recipe.RustProfile, recipe.GoProfile, recipe.NodeProfile})

	plan, err := PlanComposition(request, root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.BaseImage != managedFoundation {
		t.Fatalf("foundation = %q", plan.BaseImage)
	}
	kinds := make([]string, 0, len(plan.Toolchains))
	for _, toolchain := range plan.Toolchains {
		kinds = append(kinds, toolchain.Kind)
		containerfile := plan.Stages[1].Containerfile
		if !strings.Contains(containerfile, "/opt/shadowtree/toolchains/"+toolchain.Kind+"/") {
			t.Fatalf("toolchain %s does not use its managed prefix:\n%s", toolchain.Kind, containerfile)
		}
	}
	if !slices.Equal(kinds, []string{"go", "node", "rust"}) {
		t.Fatalf("toolchain kinds = %v", kinds)
	}
	if plan.ToolchainKey == "" {
		t.Fatal("toolchain key is empty")
	}
}

func TestPlanCompositionToolchainKeyIgnoresOrderAndProject(t *testing.T) {
	firstRoot := compositionProject(t)
	firstRequest := compositionRequest(t, firstRoot, []string{recipe.GoProfile, recipe.NodeProfile, recipe.RustProfile})
	first, err := PlanComposition(firstRequest, firstRoot)
	if err != nil {
		t.Fatal(err)
	}

	secondRoot := compositionProject(t)
	secondRequest := compositionRequest(t, secondRoot, []string{recipe.RustProfile, recipe.NodeProfile, recipe.GoProfile})
	second, err := PlanComposition(secondRequest, secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	if first.ToolchainKey != second.ToolchainKey {
		t.Fatalf("toolchain keys differ by order/project: %s != %s", first.ToolchainKey, second.ToolchainKey)
	}
	if first.FinalTag == second.FinalTag {
		t.Fatal("project-owned final tags unexpectedly match")
	}
}

func TestPlanCompositionRejectsUnsupportedExplicitFoundation(t *testing.T) {
	root := compositionProject(t)
	request := compositionRequest(t, root, []string{recipe.GoProfile})
	request.Contributions[0].Resolved.Recipe.System = &recipe.SystemConfig{BaseImage: "alpine:3.22"}
	request.Root.Recipe.System = request.Contributions[0].Resolved.Recipe.System

	_, err := PlanComposition(request, root)
	if err == nil || !strings.Contains(err.Error(), "Debian or Ubuntu") {
		t.Fatalf("PlanComposition error = %v", err)
	}
}

func compositionProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":       "module example.com/app\n\ngo 1.26.4\n",
		"package.json": `{"packageManager":"npm@11.4.2"}`,
		"Cargo.toml":   "[package]\nname = \"app\"\nversion = \"0.1.0\"\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func compositionRequest(t *testing.T, root string, profiles []string) ImageRequest {
	t.Helper()
	contributions := make([]ImageContribution, 0, len(profiles))
	for index, profile := range profiles {
		name := "recipe-" + profile
		resolved, err := recipe.Resolve(name, recipe.Recipe{Cmd: recipe.Command{"true"}, Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, filepath.Join(root, ".shadowtree.toml"), profile)
		if err != nil {
			t.Fatal(err)
		}
		contributions = append(contributions, ImageContribution{Resolved: resolved, ConfigIdentity: ".shadowtree.toml", ReferenceRoute: []ReferenceRouteStep{{Recipe: "root", Stage: "cmd", Reference: name}}})
		if index == 0 {
			contributions[0].ReferenceRoute = nil
		}
	}
	return ImageRequest{Root: contributions[0].Resolved, Contributions: contributions}
}
