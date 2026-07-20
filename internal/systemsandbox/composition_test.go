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

func TestPlanCompositionSupportsEveryNonEmptyProfileCombination(t *testing.T) {
	profiles := recipe.SupportedProfiles()
	for mask := 1; mask < 1<<len(profiles); mask++ {
		var selected []string
		for index, profile := range profiles {
			if mask&(1<<index) != 0 {
				selected = append(selected, profile)
			}
		}
		t.Run(strings.Join(selected, "+"), func(t *testing.T) {
			root := compositionProject(t)
			if _, err := PlanComposition(compositionRequest(t, root, selected), root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlanCompositionSupportsNodeVariantsInMixedSets(t *testing.T) {
	for manager, declaration := range map[string]string{
		"npm": "npm@11.4.2", "pnpm": "pnpm@10.12.1", "yarn": "yarn@4.9.2", "bun": "bun@1.2.17",
	} {
		t.Run(manager, func(t *testing.T) {
			root := compositionProject(t)
			if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"packageManager":"`+declaration+`"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			plan, err := PlanComposition(compositionRequest(t, root, []string{recipe.GoProfile, recipe.NodeProfile, recipe.RustProfile}), root)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, toolchain := range plan.Toolchains {
				found = found || toolchain.Variant == manager
			}
			if !found {
				t.Fatalf("toolchains = %#v, missing %s variant", plan.Toolchains, manager)
			}
		})
	}
}

func TestResolveToolchainsMergesExactAndDefaultButRejectsExactConflict(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"default", "exact", "other"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeImageTestFile(t, filepath.Join(root, "exact", "go.mod"), "module example.com/exact\n\ngo 1.25\ntoolchain go1.25.5\n")
	writeImageTestFile(t, filepath.Join(root, "other", "go.mod"), "module example.com/other\n\ngo 1.24\ntoolchain go1.24.7\n")
	contribution := func(name, workdir string) ImageContribution {
		resolved := systemImageRecipe(t, recipe.GoProfile, recipe.Requirements{})
		resolved.Name = name
		return ImageContribution{Resolved: resolved, Workdir: workdir, ConfigIdentity: ".shadowtree.toml"}
	}
	merged, _, err := resolveToolchains(ImageRequest{Contributions: []ImageContribution{contribution("default", "default"), contribution("exact", "exact")}}, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 || merged[0].Identity != "1.25.5" || len(merged[0].Origins) != 2 {
		t.Fatalf("merged toolchain = %#v", merged)
	}
	_, _, err = resolveToolchains(ImageRequest{Contributions: []ImageContribution{contribution("exact", "exact"), contribution("other", "other")}}, root)
	if err == nil || !strings.Contains(err.Error(), "exact") || !strings.Contains(err.Error(), "other") {
		t.Fatalf("conflict error = %v", err)
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
	for index := range first.Stages {
		if first.Stages[index].Key != second.Stages[index].Key {
			t.Fatalf("stage %s differs by order/project: %s != %s", first.Stages[index].Name, first.Stages[index].Key, second.Stages[index].Key)
		}
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

func TestPlanCompositionVerifiesFoundationBeforeProviders(t *testing.T) {
	root := compositionProject(t)
	plan, err := PlanComposition(compositionRequest(t, root, []string{recipe.GoProfile}), root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Stages[0].Containerfile, "/etc/os-release") || !strings.Contains(plan.Stages[0].Containerfile, "debian|ubuntu") {
		t.Fatalf("base stage lacks distribution verification:\n%s", plan.Stages[0].Containerfile)
	}
}

func TestPlanCompositionAddsInstallableCommandProviders(t *testing.T) {
	root := compositionProject(t)
	resolved := systemImageRecipe(t, "", recipe.Requirements{
		GoCommands:   map[string]string{"stringer": "golang.org/x/tools/cmd/stringer@v0.34.0"},
		NodeCommands: map[string]string{"eslint": "eslint@9.30.0"},
	})
	request := testImageRequest(resolved)
	plan, err := PlanComposition(request, root)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, toolchain := range plan.Toolchains {
		kinds = append(kinds, toolchain.Kind)
	}
	if !slices.Equal(kinds, []string{"go", "node"}) {
		t.Fatalf("implicit toolchains = %#v", plan.Toolchains)
	}
}

func TestPlanCompositionKeepsCorepackPayloadInManagedEnvironment(t *testing.T) {
	root := compositionProject(t)
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"packageManager":"pnpm@10.12.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanComposition(compositionRequest(t, root, []string{recipe.NodeProfile}), root)
	if err != nil {
		t.Fatal(err)
	}
	toolchain := plan.Toolchains[0]
	if !strings.HasPrefix(toolchain.Environment["COREPACK_HOME"], "/opt/shadowtree/toolchains/node/") || !strings.Contains(strings.Join(toolchain.Setup, "\n"), "COREPACK_HOME=") {
		t.Fatalf("Corepack provider = %#v", toolchain)
	}
}

func TestPlanCompositionRejectsYarnPnPSeed(t *testing.T) {
	root := compositionProject(t)
	writeImageTestFile(t, filepath.Join(root, "package.json"), `{"packageManager":"yarn@4.9.2"}`)
	writeImageTestFile(t, filepath.Join(root, "yarn.lock"), "# lock\n")
	_, err := PlanComposition(compositionRequest(t, root, []string{recipe.NodeProfile}), root)
	if err == nil || !strings.Contains(err.Error(), "Plug'n'Play") {
		t.Fatalf("PlanComposition error = %v", err)
	}
}

func TestPlanCompositionPreservesPluralDependencyPlansAndSeeds(t *testing.T) {
	root := t.TempDir()
	var contributions []ImageContribution
	for _, dir := range []string{"packages/a", "packages/b"} {
		absolute := filepath.Join(root, filepath.FromSlash(dir))
		if err := os.MkdirAll(absolute, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(absolute, "package.json"), []byte(`{"packageManager":"npm@11.4.2"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(absolute, "package-lock.json"), []byte(`{"lockfileVersion":3}`), 0o644); err != nil {
			t.Fatal(err)
		}
		resolved, err := recipe.Resolve(filepath.Base(dir), recipe.Recipe{Cmd: recipe.Command{"true"}, Workdir: dir, Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, filepath.Join(root, ".shadowtree.toml"), recipe.NodeProfile)
		if err != nil {
			t.Fatal(err)
		}
		contributions = append(contributions, ImageContribution{Resolved: resolved, ConfigIdentity: ".shadowtree.toml", Workdir: dir})
	}
	request := ImageRequest{Root: contributions[0].Resolved, Contributions: contributions}
	plan, err := PlanComposition(request, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Dependencies) != 2 || len(plan.DependencySeeds) != 2 {
		t.Fatalf("dependencies/seeds = %#v / %#v", plan.Dependencies, plan.DependencySeeds)
	}
	if plan.DependencySeeds[0].TargetPath != "packages/a/node_modules" || plan.DependencySeeds[1].TargetPath != "packages/b/node_modules" {
		t.Fatalf("seed targets = %#v", plan.DependencySeeds)
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
