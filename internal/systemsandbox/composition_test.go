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
	if plan.Stages[1].Key == "" {
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

func TestPlanCompositionAddsNodeProviderForNodeCommandsInBunProject(t *testing.T) {
	root := compositionProject(t)
	writeImageTestFile(t, filepath.Join(root, "package.json"), `{"packageManager":"bun@1.2.17"}`)
	request := compositionRequest(t, root, []string{recipe.NodeProfile})
	request.Root.Recipe.Requires.NodeCommands = map[string]string{"tool": "example-tool@1.2.3"}

	plan, err := PlanComposition(request, root)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, toolchain := range plan.Toolchains {
		kinds = append(kinds, toolchain.Kind)
	}
	if !slices.Equal(kinds, []string{"bun", "node"}) {
		t.Fatalf("toolchain kinds = %v, want bun and node", kinds)
	}
	if containerfile := plan.Stages[3].Containerfile; !strings.Contains(containerfile, "npm install --global --prefix /opt/shadowtree") {
		t.Fatalf("node command is not installed with the composed npm provider:\n%s", containerfile)
	}
}

func TestPlanCompositionPreservesCorepackProviderForNodeCommands(t *testing.T) {
	for manager, test := range map[string]struct {
		declaration       string
		lockfile          string
		config            map[string]string
		dependencyCommand string
	}{
		"pnpm": {
			declaration:       "pnpm@10.12.1",
			lockfile:          "pnpm-lock.yaml",
			dependencyCommand: "pnpm install --frozen-lockfile",
		},
		"yarn": {
			declaration: "yarn@4.9.2",
			lockfile:    "yarn.lock",
			config: map[string]string{
				".yarnrc.yml": "nodeLinker: node-modules\n",
			},
			dependencyCommand: "yarn install --immutable",
		},
	} {
		t.Run(manager, func(t *testing.T) {
			root := compositionProject(t)
			writeImageTestFile(t, filepath.Join(root, "package.json"), `{"packageManager":"`+test.declaration+`"}`)
			writeImageTestFile(t, filepath.Join(root, test.lockfile), "lock\n")
			for name, content := range test.config {
				writeImageTestFile(t, filepath.Join(root, name), content)
			}
			request := compositionRequest(t, root, []string{recipe.NodeProfile})
			request.Root.Recipe.Requires.NodeCommands = map[string]string{"tool": "example-tool@1.2.3"}

			plan, err := PlanComposition(request, root)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Toolchains) != 1 || plan.Toolchains[0].Kind != "node" || plan.Toolchains[0].Variant != manager {
				t.Fatalf("toolchains = %#v, want the project %s provider only", plan.Toolchains, manager)
			}
			if tooling := plan.Stages[1].Containerfile; !strings.Contains(tooling, "corepack prepare '"+test.declaration+"'") || !strings.Contains(tooling, "COREPACK_HOME=") {
				t.Fatalf("tooling stage lost the %s Corepack provider:\n%s", manager, tooling)
			}
			if recipePackages := plan.Stages[3].Containerfile; !strings.Contains(recipePackages, "npm install --global --prefix /opt/shadowtree") {
				t.Fatalf("recipe-package stage does not install the Node command with npm:\n%s", recipePackages)
			}
			if dependencies := plan.Stages[4].Containerfile; !strings.Contains(dependencies, test.dependencyCommand) {
				t.Fatalf("dependency stage lost the %s project manager:\n%s", manager, dependencies)
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
	if first.Stages[1].Key != second.Stages[1].Key {
		t.Fatalf("toolchain keys differ by order/project: %s != %s", first.Stages[1].Key, second.Stages[1].Key)
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

func TestSupportedCompositionFoundationNormalizesPinnedReferences(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for image, want := range map[string]bool{
		"ubuntu:24.04@sha256:" + digest:                         true,
		"debian:trixie@sha256:" + digest:                        true,
		"registry.example:5000/library/ubuntu@sha256:" + digest: true,
		"ubuntu@sha256:" + digest:                               true,
		"alpine:3.22@sha256:" + digest:                          false,
	} {
		if got := supportedCompositionFoundation(image); got != want {
			t.Errorf("supportedCompositionFoundation(%q) = %t, want %t", image, got, want)
		}
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
	if !strings.Contains(plan.Stages[0].Containerfile, "'ca-certificates' 'curl' 'tzdata' 'wget'") {
		t.Fatalf("base stage lacks managed network and timezone packages:\n%s", plan.Stages[0].Containerfile)
	}
}

func TestPlanCompositionRequiresSupportedFoundationWithoutToolchains(t *testing.T) {
	root := compositionProject(t)
	request := testImageRequest(systemImageRecipe(t, "", recipe.Requirements{}))
	request.Contributions[0].Resolved.Recipe.System = &recipe.SystemConfig{BaseImage: "alpine:3.22"}
	request.Root.Recipe.System = request.Contributions[0].Resolved.Recipe.System

	_, err := PlanComposition(request, root)
	if err == nil || !strings.Contains(err.Error(), "managed base packages") {
		t.Fatalf("PlanComposition error = %v", err)
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
	containerfile := plan.Stages[1].Containerfile
	pathIndex := strings.Index(containerfile, "ENV PATH=/opt/shadowtree/bin:$PATH")
	corepackIndex := strings.Index(containerfile, "/opt/shadowtree/bin/corepack enable")
	if pathIndex < 0 || corepackIndex < 0 || pathIndex > corepackIndex {
		t.Fatalf("managed PATH is not published before Corepack setup:\n%s", containerfile)
	}
	if strings.Count(containerfile, "ENV PATH=") != 1 {
		t.Fatalf("managed PATH publication is ambiguous:\n%s", containerfile)
	}
}

func TestToolchainStageCommandsRejectsInvalidProviderContracts(t *testing.T) {
	for name, toolchain := range map[string]ResolvedToolchain{
		"missing contract": {
			Kind: "future",
		},
		"managed PATH collision": {
			Kind: "future", ContractVersion: toolchainContractVersion,
			Environment: map[string]string{"PATH": "/future/bin"},
		},
		"malformed environment": {
			Kind: "future", ContractVersion: toolchainContractVersion,
			Environment: map[string]string{"BAD NAME": "value"},
		},
		"unknown contract": {
			Kind: "future", ContractVersion: "toolchain-provider-v2",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := toolchainStageCommands([]ResolvedToolchain{toolchain}); err == nil {
				t.Fatalf("toolchainStageCommands accepted %#v", toolchain)
			}
		})
	}
}

func TestToolchainStageCommandsAllowsUnrelatedEnvironmentNames(t *testing.T) {
	commands, _, err := toolchainStageCommands([]ResolvedToolchain{{
		Kind:            "future",
		ContractVersion: toolchainContractVersion,
		Environment:     map[string]string{"PATH_SUFFIX": "/future/bin", "PYTHONPATH": "/future/lib"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	containerfile := strings.Join(commands, "\n")
	for _, environment := range []string{"ENV PATH_SUFFIX=/future/bin", "ENV PYTHONPATH=/future/lib"} {
		if !strings.Contains(containerfile, environment) {
			t.Fatalf("toolchain commands omit %q:\n%s", environment, containerfile)
		}
	}
}

func TestDependencySeedReadabilityCommandConfinesPaths(t *testing.T) {
	command := dependencySeedReadabilityCommand([]DependencySeed{
		{SourcePath: "/opt/shadowtree/dependencies/webui/node_modules"},
		{SourcePath: "/opt/shadowtree/dependencies-other/collision"},
		{SourcePath: "/opt/shadowtree/dependencies/../../future"},
	})
	for _, expected := range []string{
		"'/opt/shadowtree/dependencies'",
		"'/opt/shadowtree/dependencies/webui'",
		"'/opt/shadowtree/dependencies/webui/node_modules'",
	} {
		if !strings.Contains(command, expected) {
			t.Fatalf("dependency readability command omits %s: %s", expected, command)
		}
	}
	for _, unrelated := range []string{"dependencies-other", "future"} {
		if strings.Contains(command, unrelated) {
			t.Fatalf("dependency readability command includes unrelated path %q: %s", unrelated, command)
		}
	}
	if got := dependencySeedReadabilityCommand(nil); got != "" {
		t.Fatalf("empty dependency readability command = %q", got)
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

func TestPlanCompositionParsesYarnNodeLinkerAsYAML(t *testing.T) {
	for name, test := range map[string]struct {
		config string
		ok     bool
	}{
		"inline comment": {config: "nodeLinker: node-modules # required layout\n", ok: true},
		"quoted":         {config: "nodeLinker: 'node-modules'\n", ok: true},
		"nested":         {config: "settings:\n  nodeLinker: node-modules\n"},
		"block scalar":   {config: "note: |\n  nodeLinker: node-modules\n"},
		"pnp":            {config: "nodeLinker: pnp\n"},
		"malformed":      {config: "nodeLinker: [\n"},
	} {
		t.Run(name, func(t *testing.T) {
			root := compositionProject(t)
			writeImageTestFile(t, filepath.Join(root, "package.json"), `{"packageManager":"yarn@4.9.2"}`)
			writeImageTestFile(t, filepath.Join(root, "yarn.lock"), "# lock\n")
			writeImageTestFile(t, filepath.Join(root, ".yarnrc.yml"), test.config)
			plan, err := PlanComposition(compositionRequest(t, root, []string{recipe.NodeProfile}), root)
			if test.ok {
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.DependencySeeds) != 1 || plan.DependencySeeds[0].Provider != "yarn" {
					t.Fatalf("Yarn seeds = %#v", plan.DependencySeeds)
				}
				return
			}
			if err == nil {
				t.Fatal("PlanComposition accepted unproven Yarn node_modules layout")
			}
		})
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
