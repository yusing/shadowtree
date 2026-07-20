package systemsandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
)

func TestPlanCompositionBuildsFiveOrderedImmutableStages(t *testing.T) {
	dir := t.TempDir()
	writeImageTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.26\n")
	writeImageTestFile(t, filepath.Join(dir, "go.sum"), "example.com/dependency v1.0.0 h1:sum\n")
	resolved := systemImageRecipe(t, recipe.GoProfile, recipe.Requirements{
		SystemPackages: []string{"git", "ca-certificates"},
		GoCommands:     map[string]string{"stringer": "golang.org/x/tools/cmd/stringer@v0.34.0"},
	})
	plan, err := PlanComposition(testImageRequest(resolved), dir)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"base", "toolchains", "system-packages", "recipe-packages", "dependencies"}
	if len(plan.Stages) != len(wantNames) {
		t.Fatalf("stages = %d, want %d", len(plan.Stages), len(wantNames))
	}
	for i, stage := range plan.Stages {
		if stage.Name != wantNames[i] || stage.Key == "" || stage.Tag != "shadowtree.local/stage/"+stage.Name+":"+stage.Key {
			t.Fatalf("stage[%d] = %#v", i, stage)
		}
		if stage.Labels["shadowtree.key"] != stage.Key || stage.Labels["shadowtree.parent-key"] != stage.ParentKey {
			t.Fatalf("stage[%d] labels = %#v", i, stage.Labels)
		}
		if !strings.HasPrefix(stage.Containerfile, "FROM ") {
			t.Fatalf("stage[%d] Containerfile = %q", i, stage.Containerfile)
		}
		if i > 0 && stage.ParentKey != plan.Stages[i-1].Key {
			t.Fatalf("stage[%d] parent = %q, want %q", i, stage.ParentKey, plan.Stages[i-1].Key)
		}
	}
	if !strings.Contains(plan.Stages[2].Containerfile, "'ca-certificates' 'git'") {
		t.Fatalf("system package order is not canonical:\n%s", plan.Stages[2].Containerfile)
	}
	if !strings.Contains(plan.Stages[4].Containerfile, "go mod download") || len(plan.Stages[4].Context) != 2 {
		t.Fatalf("dependency stage = %#v", plan.Stages[4])
	}
}

func TestPlanCompositionExcludesUnrelatedGoModules(t *testing.T) {
	dir := t.TempDir()
	writeImageTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.26\n")
	writeImageTestFile(t, filepath.Join(dir, "go.sum"), "root\n")
	writeImageTestFile(t, filepath.Join(dir, "unrelated", "go.mod"), "module example.com/unrelated\n\ngo 1.26\n")
	writeImageTestFile(t, filepath.Join(dir, "unrelated", "go.sum"), "unrelated\n")
	plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.GoProfile, recipe.Requirements{})), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"unrelated/go.mod", "unrelated/go.sum"} {
		if _, ok := plan.Stages[4].Context[file]; ok {
			t.Fatalf("unrelated Go module input %s entered dependency context", file)
		}
	}
}

func TestPlanCompositionChangesOnlyAffectedStageAndStagesAbove(t *testing.T) {
	dir := t.TempDir()
	writeImageTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.26\n")
	writeImageTestFile(t, filepath.Join(dir, "go.sum"), "first\n")
	resolved := systemImageRecipe(t, recipe.GoProfile, recipe.Requirements{SystemPackages: []string{"git"}})
	first, err := PlanComposition(testImageRequest(resolved), dir)
	if err != nil {
		t.Fatal(err)
	}
	writeImageTestFile(t, filepath.Join(dir, "go.sum"), "second\n")
	second, err := PlanComposition(testImageRequest(resolved), dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		if first.Stages[i].Key != second.Stages[i].Key {
			t.Fatalf("stage %s changed after lockfile-only edit", first.Stages[i].Name)
		}
	}
	if first.Stages[4].Key == second.Stages[4].Key {
		t.Fatal("dependency stage did not change after lockfile edit")
	}
}

func TestPlanCompositionPreservesLowerStagesForPackageChanges(t *testing.T) {
	dir := t.TempDir()
	writeImageTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.26\n")
	writeImageTestFile(t, filepath.Join(dir, "go.sum"), "sum\n")
	first, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.GoProfile, recipe.Requirements{SystemPackages: []string{"git"}, GoCommands: map[string]string{"tool": "example.com/tool@v1.2.3"}})), dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.GoProfile, recipe.Requirements{SystemPackages: []string{"curl"}, GoCommands: map[string]string{"tool": "example.com/tool@v1.2.3"}})), dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Stages[0].Key != second.Stages[0].Key || first.Stages[1].Key != second.Stages[1].Key {
		t.Fatal("system-package change invalidated base or tooling")
	}
	for i := 2; i < 5; i++ {
		if first.Stages[i].Key == second.Stages[i].Key {
			t.Fatalf("stage %s did not change above system packages", first.Stages[i].Name)
		}
	}
	third, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.GoProfile, recipe.Requirements{SystemPackages: []string{"curl"}, GoCommands: map[string]string{"tool": "example.com/tool@v1.2.4"}})), dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if second.Stages[i].Key != third.Stages[i].Key {
			t.Fatalf("recipe-package change invalidated %s", second.Stages[i].Name)
		}
	}
	if second.Stages[3].Key == third.Stages[3].Key || second.Stages[4].Key == third.Stages[4].Key {
		t.Fatal("recipe-package change did not invalidate recipe/dependency stages")
	}
}

func TestPlanCompositionSkipsUnlockedDependencyPreparation(t *testing.T) {
	dir := t.TempDir()
	writeImageTestFile(t, filepath.Join(dir, "package.json"), `{"packageManager":"pnpm@10.12.1"}`)
	resolved := systemImageRecipe(t, recipe.NodeProfile, recipe.Requirements{})
	plan, err := PlanComposition(testImageRequest(resolved), dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Stages[4].Containerfile, " install ") {
		t.Fatalf("unlocked dependency preparation enabled:\n%s", plan.Stages[4].Containerfile)
	}
	if !strings.Contains(plan.Stages[1].Containerfile, "pnpm@10.12.1") {
		t.Fatalf("exact package manager missing:\n%s", plan.Stages[1].Containerfile)
	}
}

func TestPlanCompositionKeepsCompleteNodeWorkspaceManifestContextAndSeedContract(t *testing.T) {
	dir := t.TempDir()
	member := filepath.Join(dir, "packages", "app")
	writeImageTestFile(t, filepath.Join(dir, "package.json"), `{"packageManager":"pnpm@10.12.1","workspaces":["packages/*"]}`)
	writeImageTestFile(t, filepath.Join(dir, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
	writeImageTestFile(t, filepath.Join(dir, "pnpm-workspace.yaml"), "packages: ['packages/*']\n")
	writeImageTestFile(t, filepath.Join(member, "package.json"), `{"name":"app"}`)
	writeImageTestFile(t, filepath.Join(member, "main.js"), "ordinary source")
	resolved := systemImageRecipe(t, recipe.NodeProfile, recipe.Requirements{})
	resolved.Recipe.Workdir = "packages/app"
	plan, err := PlanComposition(testImageRequest(resolved), dir)
	if err != nil {
		t.Fatal(err)
	}
	context := plan.Stages[4].Context
	for _, path := range []string{"package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "packages/app/package.json"} {
		if _, ok := context[path]; !ok {
			t.Fatalf("dependency context missing %s: %#v", path, context)
		}
	}
	if _, ok := context["packages/app/main.js"]; ok {
		t.Fatal("ordinary source entered dependency context")
	}
	if len(plan.DependencySeeds) != 1 || plan.DependencySeeds[0].Provider != "pnpm" || !strings.Contains(plan.Stages[4].Containerfile, "--store-dir .shadowtree-pnpm-store") {
		t.Fatalf("dependency seed contract = %#v\n%s", plan.DependencySeeds, plan.Stages[4].Containerfile)
	}
}

func TestPlanCompositionExcludesUnrelatedNodeProjects(t *testing.T) {
	dir := t.TempDir()
	writeImageTestFile(t, filepath.Join(dir, "package.json"), `{"packageManager":"npm@11.4.2"}`)
	writeImageTestFile(t, filepath.Join(dir, "package-lock.json"), `{}`)
	writeImageTestFile(t, filepath.Join(dir, "unrelated", "package.json"), `{"dependencies":{"local":"file:../private"}}`)
	writeImageTestFile(t, filepath.Join(dir, "unrelated", ".npmrc"), "//registry.example/:_authToken=secret\n")
	plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.NodeProfile, recipe.Requirements{})), dir)
	if err != nil {
		t.Fatal(err)
	}
	context := plan.Stages[4].Context
	for _, file := range []string{"unrelated/package.json", "unrelated/.npmrc"} {
		if _, ok := context[file]; ok {
			t.Fatalf("unrelated project input %s entered dependency context", file)
		}
	}
}

func TestPlanCompositionRejectsUnsafeLocalNodeDependencies(t *testing.T) {
	for _, source := range []string{"file:../local", "link:../local", "portal:../local"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			writeImageTestFile(t, filepath.Join(dir, "package.json"), `{"dependencies":{"local":"`+source+`"}}`)
			writeImageTestFile(t, filepath.Join(dir, "package-lock.json"), `{}`)
			_, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.NodeProfile, recipe.Requirements{})), dir)
			if err == nil || !strings.Contains(err.Error(), "local source") {
				t.Fatalf("PlanComposition error = %v, want local-source rejection", err)
			}
		})
	}
}

func TestPlanCompositionFailsClosedForCredentialOrExecutableManagerConfig(t *testing.T) {
	for name, tc := range map[string]struct{ path, content, want string }{
		"credential":          {path: ".npmrc", content: "//registry.example/:_authToken=secret\n", want: "credential"},
		"embedded credential": {path: "package-lock.json", content: `{"packages":{},"resolved":"https://token@registry.example/package.tgz"}`, want: "embedded url credentials"},
		"executable":          {path: ".pnpmfile.cjs", content: "module.exports = {}\n", want: "executable"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeImageTestFile(t, filepath.Join(dir, "package.json"), `{"packageManager":"pnpm@10.12.1"}`)
			writeImageTestFile(t, filepath.Join(dir, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
			writeImageTestFile(t, filepath.Join(dir, tc.path), tc.content)
			_, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.NodeProfile, recipe.Requirements{})), dir)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("PlanComposition error = %v, want %s rejection", err, name)
			}
		})
	}
}

func TestPlanCompositionFailsClosedForUnsafeCargoConfiguration(t *testing.T) {
	for name, content := range map[string]string{
		"credential": `[registries.private]
credential-provider = "cargo:token"
`,
		"vendored source": `[source.crates-io]
replace-with = "vendored-sources"
[source.vendored-sources]
directory = "vendor"
`,
		"local registry": `[source.local]
local-registry = "registry"
`,
		"path override": `paths = ["../local-crate"]
`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeImageTestFile(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"app\"\nversion = \"0.1.0\"\n")
			writeImageTestFile(t, filepath.Join(dir, "Cargo.lock"), "# lock\n")
			writeImageTestFile(t, filepath.Join(dir, ".cargo", "config.toml"), content)
			_, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.RustProfile, recipe.Requirements{})), dir)
			if err == nil || (!strings.Contains(strings.ToLower(err.Error()), "credential") && !strings.Contains(err.Error(), "cannot safely pre-key")) {
				t.Fatalf("PlanComposition error = %v, want unsafe Cargo configuration rejection", err)
			}
		})
	}
}

func TestPlanCompositionCreatesSafeRustTargetsWithoutPersistingOrdinaryDiffs(t *testing.T) {
	dir := t.TempDir()
	writeImageTestFile(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"app\"\nversion = \"0.1.0\"\nedition = \"2024\"\n")
	writeImageTestFile(t, filepath.Join(dir, "Cargo.lock"), "version = 4\n\n[[package]]\nname = \"app\"\nversion = \"0.1.0\"\n")
	writeImageTestFile(t, filepath.Join(dir, "src", "main.rs"), "fn main() {}\n")
	writeImageTestFile(t, filepath.Join(dir, "private.diff"), "private source fragment\n")
	writeImageTestFile(t, filepath.Join(dir, "unrelated", "Cargo.toml"), "[package]\nname = \"unrelated\"\nversion = \"0.1.0\"\n")
	writeImageTestFile(t, filepath.Join(dir, "unrelated", ".cargo", "config.toml"), "[registry]\ntoken = \"secret\"\n")
	plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.RustProfile, recipe.Requirements{})), dir)
	if err != nil {
		t.Fatal(err)
	}
	context := plan.Stages[4].Context
	for _, target := range []string{"src/lib.rs", "src/main.rs"} {
		if _, ok := context[target]; !ok {
			t.Fatalf("Rust dependency context missing safe target %s: %#v", target, context)
		}
	}
	if _, ok := context["private.diff"]; ok {
		t.Fatal("unrelated diff entered Rust dependency context")
	}
	for _, file := range []string{"unrelated/Cargo.toml", "unrelated/.cargo/config.toml"} {
		if _, ok := context[file]; ok {
			t.Fatalf("unrelated Rust project input %s entered dependency context", file)
		}
	}
	if got := string(context["src/main.rs"]); strings.Contains(got, "fn main") {
		t.Fatalf("ordinary Rust source entered dependency context: %q", got)
	}
}

func TestRustDependencyContextSupportsLockedCargoFetch(t *testing.T) {
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo is not installed")
	}
	dir := t.TempDir()
	writeImageTestFile(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"app\"\nversion = \"0.1.0\"\nedition = \"2024\"\n")
	writeImageTestFile(t, filepath.Join(dir, "Cargo.lock"), "version = 4\n\n[[package]]\nname = \"app\"\nversion = \"0.1.0\"\n")
	plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.RustProfile, recipe.Requirements{})), dir)
	if err != nil {
		t.Fatal(err)
	}
	contextDir := t.TempDir()
	for name, data := range plan.Stages[4].Context {
		writeImageTestFile(t, filepath.Join(contextDir, filepath.FromSlash(name)), string(data))
	}
	cmd := exec.CommandContext(t.Context(), cargo, "fetch", "--locked", "--offline")
	cmd.Dir = contextDir
	cmd.Env = append(os.Environ(), "CARGO_HOME="+filepath.Join(t.TempDir(), "cargo"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo fetch --locked against manifest-only context: %v\n%s", err, output)
	}
}

func TestPlanCompositionSelectsExactProfileBasesAndTooling(t *testing.T) {
	tests := []struct {
		name         string
		profile      string
		files        map[string]string
		wantTool     string
		wantPlatform string
	}{
		{name: "none"},
		{name: "go", profile: recipe.GoProfile, files: map[string]string{"go.mod": "module example.com/app\n\ngo 1.26\n"}, wantTool: "go1.26.4"},
		{name: "npm", profile: recipe.NodeProfile, files: map[string]string{"package.json": `{}`}, wantTool: "npm --version"},
		{name: "pnpm", profile: recipe.NodeProfile, files: map[string]string{"package.json": `{"packageManager":"pnpm@10.12.1"}`}, wantTool: "pnpm@10.12.1"},
		{name: "yarn", profile: recipe.NodeProfile, files: map[string]string{"package.json": `{"packageManager":"yarn@4.9.2"}`}, wantTool: "yarn@4.9.2"},
		{name: "bun", profile: recipe.NodeProfile, files: map[string]string{"package.json": `{"packageManager":"bun@1.2.17"}`}, wantTool: "bun --version"},
		{name: "rust", profile: recipe.RustProfile, files: map[string]string{"Cargo.toml": "[package]\nname = \"app\"\nversion = \"0.1.0\"\n"}, wantTool: "release: 1.96.0"},
		{name: "host-qualified rust", profile: recipe.RustProfile, files: map[string]string{"Cargo.toml": "[package]\nname = \"app\"\nversion = \"0.1.0\"\n", "rust-toolchain": "1.96.0-x86_64-unknown-linux-gnu\n"}, wantTool: "host: x86_64-unknown-linux-gnu", wantPlatform: "linux/amd64"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for path, content := range tc.files {
				writeImageTestFile(t, filepath.Join(dir, path), content)
			}
			plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, tc.profile, recipe.Requirements{})), dir)
			if err != nil {
				t.Fatal(err)
			}
			if plan.BaseImage != managedFoundation {
				t.Fatalf("base = %q, want %q", plan.BaseImage, managedFoundation)
			}
			if tc.wantTool != "" && !strings.Contains(plan.Stages[1].Containerfile, tc.wantTool) {
				t.Fatalf("toolchains stage does not contain %q:\n%s", tc.wantTool, plan.Stages[1].Containerfile)
			}
			wantPlatform := tc.wantPlatform
			if wantPlatform == "" {
				wantPlatform = "linux/" + runtime.GOARCH
			}
			if plan.Platform != wantPlatform {
				t.Fatalf("platform = %q, want %q", plan.Platform, wantPlatform)
			}
		})
	}
}

func TestPlanCompositionRejectsNonExactInstallablePackages(t *testing.T) {
	for name, requirements := range map[string]recipe.Requirements{
		"go":   {GoCommands: map[string]string{"tool": "example.com/tool@latest"}},
		"node": {NodeCommands: map[string]string{"tool": "tool@^1.2.3"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PlanComposition(testImageRequest(systemImageRecipe(t, "", requirements)), t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "exact") {
				t.Fatalf("PlanComposition error = %v, want exact version rejection", err)
			}
		})
	}
}

func TestPlanCompositionRejectsSystemPackagesForUnsupportedDistribution(t *testing.T) {
	for _, image := range []string{
		"alpine:3.22.0",
		"example.com/alpine-bookworm:1.0.0",
		"example.com/bun:1.0.0",
		"node:24-alpine",
		"golang:1.26-alpine",
		"rust:1.96-alpine",
		"node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		t.Run(image, func(t *testing.T) {
			resolved := systemImageRecipe(t, "", recipe.Requirements{SystemPackages: []string{"git"}})
			resolved.Recipe.System = &recipe.SystemConfig{BaseImage: image}
			_, err := PlanComposition(testImageRequest(resolved), t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "Debian or Ubuntu") {
				t.Fatalf("PlanComposition error = %v, want provider rejection", err)
			}
		})
	}
}

func TestPlanCompositionRejectsMutableOrDynamicBaseOverride(t *testing.T) {
	for _, image := range []string{"ubuntu:latest", "{IMAGE}", "ubuntu"} {
		t.Run(image, func(t *testing.T) {
			resolved := systemImageRecipe(t, "", recipe.Requirements{})
			resolved.Recipe.System = &recipe.SystemConfig{BaseImage: image}
			_, err := PlanComposition(testImageRequest(resolved), t.TempDir())
			if err == nil {
				t.Fatalf("PlanComposition accepted %q", image)
			}
		})
	}
}

func TestPlanCompositionInstallsNodeCommandsOnManagedPath(t *testing.T) {
	plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, "", recipe.Requirements{NodeCommands: map[string]string{"tool": "example-tool@1.2.3"}})), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	containerfile := plan.Stages[3].Containerfile
	if !strings.Contains(containerfile, "npm install --global --prefix /opt/shadowtree") {
		t.Fatalf("node command is not installed on managed PATH:\n%s", containerfile)
	}
}

func TestBuildImagesBuildsMissingStagesAndPublishesFinalAlias(t *testing.T) {
	plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, "", recipe.Requirements{})), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]map[string]string{}
	var builds []string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "image" && args[1] == "inspect" {
			tag := args[len(args)-1]
			labels, ok := images[tag]
			if !ok {
				return []byte("no such image"), errors.New("exit status 1")
			}
			return json.Marshal(labels)
		}
		if len(args) > 0 && args[0] == "build" {
			labels := map[string]string{}
			var tag string
			for i := 1; i < len(args)-1; i++ {
				switch args[i] {
				case "--tag":
					i++
					tag = args[i]
				case "--label":
					i++
					key, value, _ := strings.Cut(args[i], "=")
					labels[key] = value
				}
			}
			images[tag] = labels
			builds = append(builds, tag)
			return nil, nil
		}
		if len(args) == 4 && args[0] == "image" && args[1] == "tag" {
			images[args[3]] = images[args[2]]
			return nil, nil
		}
		return nil, errors.New("unexpected command")
	}
	if err := buildImagesForTest(t.Context(), Docker, plan, nil, run); err != nil {
		t.Fatal(err)
	}
	if len(builds) != 5 {
		t.Fatalf("builds = %d, want 5", len(builds))
	}
	if images[plan.FinalTag]["shadowtree.key"] != plan.Stages[4].Key {
		t.Fatalf("final labels = %#v", images[plan.FinalTag])
	}
}

func TestBuildImagesRejectsExistingTagLabelCollision(t *testing.T) {
	plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, "", recipe.Requirements{})), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"shadowtree.key":"other"}`), nil
	}
	err = buildImagesForTest(t.Context(), Docker, plan, nil, run)
	if err == nil || !strings.Contains(err.Error(), "tag collision") {
		t.Fatalf("buildImages error = %v, want collision", err)
	}
}

func TestBuildImagesReusesValidLowerStages(t *testing.T) {
	plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, "", recipe.Requirements{})), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]map[string]string{}
	for _, stage := range plan.Stages[:3] {
		images[stage.Tag] = stage.Labels
	}
	var builds, publications int
	run := imageRuntimeFake(images, &builds, &publications, nil)
	if err := buildImagesForTest(t.Context(), Docker, plan, nil, run); err != nil {
		t.Fatal(err)
	}
	if builds != 2 || publications != 1 {
		t.Fatalf("builds/publications = %d/%d, want 2/1", builds, publications)
	}
}

func TestBuildImagesFailureDoesNotPublishFinalAlias(t *testing.T) {
	plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, "", recipe.Requirements{})), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var publications int
	run := imageRuntimeFake(map[string]map[string]string{}, nil, &publications, errors.New("build failed"))
	err = buildImagesForTest(t.Context(), Docker, plan, nil, run)
	if err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("buildImages error = %v, want build failure", err)
	}
	if publications != 0 {
		t.Fatalf("final alias publications = %d, want 0", publications)
	}
}

func TestBuildImagesPropagatesStreamingBuildCancellation(t *testing.T) {
	plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, "", recipe.Requirements{})), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := imageRuntimeFake(map[string]map[string]string{}, nil, nil, nil)
	stream := func(ctx context.Context, _ io.Writer, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = buildImagesWith(ctx, Docker, plan, nil, run, stream)
	if !errors.Is(err, context.Canceled) && (err == nil || !strings.Contains(err.Error(), context.Canceled.Error())) {
		t.Fatalf("buildImages error = %v, want cancellation", err)
	}
}

func TestBuildImagesRejectsUnverifiedFinalAlias(t *testing.T) {
	plan, err := PlanComposition(testImageRequest(systemImageRecipe(t, "", recipe.Requirements{})), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]map[string]string{}
	run := imageRuntimeFake(images, nil, nil, nil)
	ignoreFinalTag := func(ctx context.Context, executable string, args ...string) ([]byte, error) {
		if len(args) == 4 && args[0] == "image" && args[1] == "tag" {
			return nil, nil
		}
		return run(ctx, executable, args...)
	}
	err = buildImagesForTest(t.Context(), Docker, plan, nil, ignoreFinalTag)
	if err == nil || !strings.Contains(err.Error(), "did not resolve") {
		t.Fatalf("buildImages error = %v, want final alias verification failure", err)
	}
}

func imageRuntimeFake(images map[string]map[string]string, builds, publications *int, buildErr error) commandRunner {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "image" && args[1] == "inspect" {
			labels, ok := images[args[len(args)-1]]
			if !ok {
				return []byte("no such image"), errors.New("exit status 1")
			}
			return json.Marshal(labels)
		}
		if len(args) > 0 && args[0] == "build" {
			if buildErr != nil {
				return []byte(buildErr.Error()), buildErr
			}
			labels := map[string]string{}
			var tag string
			for i := 1; i < len(args)-1; i++ {
				switch args[i] {
				case "--tag":
					i++
					tag = args[i]
				case "--label":
					i++
					key, value, _ := strings.Cut(args[i], "=")
					labels[key] = value
				}
			}
			images[tag] = labels
			if builds != nil {
				*builds++
			}
			return nil, nil
		}
		if len(args) == 4 && args[0] == "image" && args[1] == "tag" {
			images[args[3]] = images[args[2]]
			if publications != nil {
				*publications++
			}
			return nil, nil
		}
		return nil, errors.New("unexpected command")
	}
}

func buildImagesForTest(ctx context.Context, runtime RuntimeName, plan ImagePlan, progress io.Writer, run commandRunner) error {
	stream := func(ctx context.Context, progress io.Writer, executable string, args ...string) ([]byte, error) {
		output, err := run(ctx, executable, args...)
		if len(output) > 0 && progress != nil {
			_, _ = progress.Write(output)
		}
		return output, err
	}
	return buildImagesWith(ctx, runtime, plan, progress, run, stream)
}

func systemImageRecipe(t *testing.T, profile string, requirements recipe.Requirements) recipe.Resolved {
	t.Helper()
	resolved, err := recipe.Resolve("test", recipe.Recipe{
		Cmd:       recipe.Command{"true"},
		Sandboxed: new(recipe.SandboxModeSystem),
		Requires:  requirements,
	}, nil, nil, nil, "/project/.shadowtree.toml", profile)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func testImageRequest(resolved recipe.Resolved) ImageRequest {
	return ImageRequest{
		Root: resolved,
		Contributions: []ImageContribution{{
			Resolved: resolved,
			Workdir:  filepath.ToSlash(resolved.Recipe.Workdir),
		}},
	}
}

func writeImageTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
