package configfile

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
)

func TestLoadTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
profile = "go"
shell = "bash"
shell_prelude = "set -eu"

[vars]
go_ldflags = "-s -w"

[var_commands]
project_root = "pwd"

[recipes.test]
help = "Run tests."
sandboxed = false
cmd = "go test -count {count} {pkg} {@}"
pre = ["go generate ./..."]

[recipes.test.requires]
commands = ["go"]
optional_commands = ["h2load"]
go_commands = { stringer = "golang.org/x/tools/cmd/stringer@latest" }
node_commands = { eslint = "eslint@^9" }

[recipes.test.arguments.pkg]
type = "rel_path"
default = "."

[recipes.test.arguments.count]
help = "Repeat count."
type = "int"
default = 1

[recipes.test.presets.stable.arguments]
count = 5
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Profile != "go" {
		t.Fatalf("Profile = %q", loaded.Config.Profile)
	}
	if got := recipe.ScriptBody(loaded.Config.Recipes["test"].Pre[0].Cmd); got != "go generate ./..." {
		t.Fatalf("pre command = %#v", loaded.Config.Recipes["test"].Pre)
	}
	if got := loaded.Config.Shell; got != "bash" {
		t.Fatalf("Shell = %q", got)
	}
	if got := loaded.Config.ShellPrelude; got != "set -eu" {
		t.Fatalf("ShellPrelude = %q", got)
	}
	if got := loaded.Config.Vars["go_ldflags"]; got != "-s -w" {
		t.Fatalf("go_ldflags = %q", got)
	}
	if got := loaded.Config.VarCommands["project_root"]; len(got) != 2 || got[0] != "__shadowtree_script__" {
		t.Fatalf("project_root command = %#v", got)
	}
	if got := loaded.Config.Recipes["test"].Help; got != "Run tests." {
		t.Fatalf("Help = %q", got)
	}
	if got := loaded.Config.Recipes["test"].Sandboxed; got == nil || *got {
		t.Fatalf("Sandboxed = %#v, want false", got)
	}
	if got := loaded.Config.Recipes["test"].Cmd; len(got) != 2 || got[0] != "__shadowtree_script__" {
		t.Fatalf("cmd = %#v", got)
	}
	requires := loaded.Config.Recipes["test"].Requires
	if !slices.Equal(requires.Commands, []string{"go"}) {
		t.Fatalf("requires.commands = %#v", requires.Commands)
	}
	if !slices.Equal(requires.OptionalCommands, []string{"h2load"}) {
		t.Fatalf("requires.optional_commands = %#v", requires.OptionalCommands)
	}
	if requires.GoCommands["stringer"] != "golang.org/x/tools/cmd/stringer@latest" {
		t.Fatalf("requires.go_commands = %#v", requires.GoCommands)
	}
	if requires.NodeCommands["eslint"] != "eslint@^9" {
		t.Fatalf("requires.node_commands = %#v", requires.NodeCommands)
	}
	if got := loaded.Config.Recipes["test"].Arguments["count"].Default; got == nil {
		t.Fatal("count default is nil")
	}
	resolved, err := recipe.Resolve("test", loaded.Config.Recipes["test"], []string{"preset=stable"}, nil, nil, path, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recipe.ScriptBody(resolved.Main), "5") {
		t.Fatalf("resolved main = %#v, want profile count", resolved.Main)
	}
}

func TestInitWritesNoBuiltinRecipeOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".shadowtree.toml")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for name := range recipe.Builtins(recipe.GoProfile, recipe.BuiltinOptions{Dir: filepath.Dir(path)}) {
		if _, ok := loaded.Config.Recipes[name]; ok {
			t.Fatalf("init wrote built-in recipe override %q", name)
		}
	}
	rec := loaded.Config.Recipes["codegen-test"]
	if values := recipe.ScriptBody(rec.ForEach); values != recipe.GoModuleValuesCommand {
		t.Fatalf("codegen-test for_each = %q", values)
	}
	if rec.Workdir != "{item}" {
		t.Fatalf("codegen-test workdir = %q, want {item}", rec.Workdir)
	}
}

func TestLoadBareRecipeReferenceCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
[recipes.test]
cmd = "true"
pre = ["echo 123", "@foo"]

[recipes.foo]
cmd = "true"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("test", loaded.Config.Recipes["test"], nil, nil, nil, loaded.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	pre := resolved.Recipe.Pre
	if len(pre) != 2 {
		t.Fatalf("pre = %#v, want two commands", pre)
	}
	output, err := recipe.CommandOutput(t.Context(), filepath.Dir(path), nil, pre[0].Cmd)
	if err != nil {
		t.Fatal(err)
	}
	if output != "123\n" {
		t.Fatalf("pre[0] output = %q, want echo output", output)
	}
	if len(pre[1].Cmd) != 1 || pre[1].Cmd[0] != "@foo" {
		t.Fatalf("pre[1] = %#v, want recipe reference", pre[1])
	}
}

func TestLoadStructuredStageCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
[recipes.benchmark]
cmd = "go test ./..."

[recipes.benchmark.pre]
cmd = "benchmark_prepare"
timeout = "120s"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := loaded.Config.Recipes["benchmark"]
	if len(rec.Pre) != 1 {
		t.Fatalf("pre = %#v, want one structured command", rec.Pre)
	}
	if got := recipe.ScriptBody(rec.Pre[0].Cmd); got != "benchmark_prepare" {
		t.Fatalf("pre command = %q, want benchmark_prepare", got)
	}
	if rec.Pre[0].Timeout != "120s" {
		t.Fatalf("pre timeout = %q, want 120s", rec.Pre[0].Timeout)
	}
	resolved, err := recipe.Resolve("benchmark", rec, nil, nil, nil, loaded.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := recipe.StageTimeout(resolved.Recipe.Pre[0]); got.String() != "2m0s" {
		t.Fatalf("resolved timeout = %s, want 2m0s", got)
	}
}

func TestLoadShellCommandUsesArgumentDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
[recipes.test]
cmd = 'printf "%s\n" {pkg}'

[recipes.test.arguments.pkg]
default = "."
`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("test", loaded.Config.Recipes["test"], nil, nil, nil, loaded.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	output, err := recipe.CommandOutput(t.Context(), dir, nil, resolved.Main)
	if err != nil {
		t.Fatal(err)
	}
	if output != ".\n" {
		t.Fatalf("output = %q, want default argument forwarded", output)
	}
}

func TestLoadRejectsArgvCommandForms(t *testing.T) {
	for name, text := range map[string]string{
		"cmd": `
[recipes.test]
cmd = ["go", "test"]
`,
		"pre": `
[recipes.test]
cmd = "go test"
pre = [["go", "generate"]]
`,
		"post": `
[recipes.test]
cmd = "go test"
post = [["go", "generate"]]
`,
		"values": `
[recipes.test.arguments.target]
values = ["@enum", "api"]

[recipes.test]
cmd = "go test"
`,
		"var_commands": `
[var_commands]
ROOT = ["pwd"]
`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".shadowtree.toml")
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "command arrays are no longer supported") {
				t.Fatalf("Load() error = %v, want shell string error", err)
			}
		})
	}
}

func TestLoadRejectsUnsupportedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadowtree.json")
	if err := os.WriteFile(path, []byte(`{"profile":"go"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported config extension: .json") {
		t.Fatalf("Load() error = %v, want unsupported extension", err)
	}
}

func TestLoadRejectsTopLevelSyncOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
sync_out = ["generated"]

[recipes.generate]
cmd = "go generate ./..."
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "top-level sync_out is no longer supported") {
		t.Fatalf("Load() error = %v, want top-level sync_out rejection", err)
	}
}

func TestLoadRejectsRecipeProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
[recipes.benchmark]
cmd = "go test -bench . -count {count}"

[recipes.benchmark.arguments.count]
type = "int"
default = 1

[recipes.benchmark.profiles.stable.arguments]
count = 5
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil ||
		!strings.Contains(err.Error(), `recipe "benchmark" profiles are no longer supported`) ||
		!strings.Contains(err.Error(), `[recipes.benchmark.presets.<preset>.arguments]`) ||
		!strings.Contains(err.Error(), `preset=<preset>`) {
		t.Fatalf("Load() error = %v, want recipe profiles rejection", err)
	}
}

func TestLoadMergesIncludesBeforeCurrentConfig(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.shadowtree.toml")
	extra := filepath.Join(dir, "extra.shadowtree.toml")
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(base, []byte(`
profile = "node"
shell = "sh"
shell_prelude = "base_prelude"

[vars]
shared = "base"
base_only = "base"

[var_commands]
dynamic = "printf base"

[env]
SHARED = "base"
BASE_ONLY = "base"

[recipes.build]
help = "Base build."
for_each = "@enum api"
workdir = "{item}"
cmd = "base {target}"

[recipes.build.requires]
commands = ["go"]
optional_commands = ["h2load"]

[recipes.build.vars]
recipe_shared = "base"
recipe_base_only = "base"

[recipes.build.env]
RECIPE_SHARED = "base"
RECIPE_BASE_ONLY = "base"

[recipes.build.arguments.target]
help = "Base target."
default = "base"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extra, []byte(`
[vars]
shared = "extra"

[recipes.extra]
cmd = "extra"

[recipes.extra.requires]
commands = ["node"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`
include = ["./base.shadowtree.toml", "./extra.shadowtree.toml"]
profile = "go"
shell_prelude = "current_prelude"

[vars]
current_only = "current"

[env]
SHARED = "current"

[recipes.build]
help = "Current build."
cmd = "current {target} {mode}"

[recipes.build.vars]
recipe_shared = "current"

[recipes.build.env]
RECIPE_SHARED = "current"
RECIPE_CURRENT_ONLY = "current"

[recipes.build.arguments.mode]
default = "dev"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := loaded.Config
	if cfg.Profile != "go" {
		t.Fatalf("profile = %q, want go", cfg.Profile)
	}
	if cfg.Shell != "sh" {
		t.Fatalf("shell = %q, want inherited sh", cfg.Shell)
	}
	if cfg.ShellPrelude != "base_prelude\ncurrent_prelude" {
		t.Fatalf("shell_prelude = %q", cfg.ShellPrelude)
	}
	if cfg.Vars["shared"] != "extra" || cfg.Vars["base_only"] != "base" || cfg.Vars["current_only"] != "current" {
		t.Fatalf("vars = %#v", cfg.Vars)
	}
	if cfg.Env["SHARED"] != "current" || cfg.Env["BASE_ONLY"] != "base" {
		t.Fatalf("env = %#v", cfg.Env)
	}
	build := cfg.Recipes["build"]
	if build.Help != "Current build." || recipe.ScriptBody(build.Cmd) != "current {target} {mode}" {
		t.Fatalf("build = %#v", build)
	}
	if recipe.ScriptBody(build.ForEach) != "@enum api" || build.Workdir != "{item}" {
		t.Fatalf("build fan-out fields not inherited: %#v", build)
	}
	if build.Vars["recipe_shared"] != "current" || build.Vars["recipe_base_only"] != "base" {
		t.Fatalf("build vars = %#v", build.Vars)
	}
	if build.Env["RECIPE_SHARED"] != "current" || build.Env["RECIPE_BASE_ONLY"] != "base" || build.Env["RECIPE_CURRENT_ONLY"] != "current" {
		t.Fatalf("build env = %#v", build.Env)
	}
	if !slices.Equal(build.Requires.Commands, []string{"go"}) || !slices.Equal(build.Requires.OptionalCommands, []string{"h2load"}) {
		t.Fatalf("build requires = %#v", build.Requires)
	}
	if _, ok := build.Arguments["target"]; !ok {
		t.Fatalf("build arguments missing inherited target: %#v", build.Arguments)
	}
	if _, ok := build.Arguments["mode"]; !ok {
		t.Fatalf("build arguments missing current mode: %#v", build.Arguments)
	}
	if _, ok := cfg.Recipes["extra"]; !ok {
		t.Fatalf("missing recipe from later include: %#v", cfg.Recipes)
	}
	if !slices.Equal(cfg.Recipes["extra"].Requires.Commands, []string{"node"}) {
		t.Fatalf("extra requires = %#v", cfg.Recipes["extra"].Requires)
	}
	if source := loaded.Sources.Recipes["extra"]; source != CleanAbs(extra) {
		t.Fatalf("extra source = %q, want %q", source, CleanAbs(extra))
	}
	if source := loaded.Sources.Recipes["build"]; source != CleanAbs(path) {
		t.Fatalf("build source = %q, want %q", source, CleanAbs(path))
	}
	if source := loaded.Sources.Arguments["build"]["target"]; source != CleanAbs(base) {
		t.Fatalf("target source = %q, want %q", source, CleanAbs(base))
	}
}

func TestLoadIncludesAreRelativeToIncludingConfig(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "leaf.shadowtree.toml"), []byte(`
[recipes.leaf]
cmd = "leaf"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "base.shadowtree.toml"), []byte(`
include = ["./leaf.shadowtree.toml"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
include = ["./nested/base.shadowtree.toml"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Config.Recipes["leaf"]; !ok {
		t.Fatalf("missing transitive recipe: %#v", loaded.Config.Recipes)
	}
}

func TestLoadIncludeArgumentRequiredFalseOverridesRequiredTrue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "common.shadowtree.toml"), []byte(`
[recipes.build.arguments.target]
required = true

[recipes.build]
cmd = "echo {target}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
include = ["./common.shadowtree.toml"]

[recipes.build.arguments.target]
required = false
default = "."
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	arg := loaded.Config.Recipes["build"].Arguments["target"]
	if arg.Required {
		t.Fatalf("required = true, want false override")
	}
}

func TestLoadIncludeRequirementsReplaceAsBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "common.shadowtree.toml"), []byte(`
[recipes.build]
cmd = "go build"

[recipes.build.requires]
commands = ["go"]
optional_commands = ["h2load"]
go_commands = { stringer = "golang.org/x/tools/cmd/stringer@latest" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
include = ["./common.shadowtree.toml"]

[recipes.build.requires]
commands = ["docker"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	req := loaded.Config.Recipes["build"].Requires
	if !slices.Equal(req.Commands, []string{"docker"}) {
		t.Fatalf("commands = %#v", req.Commands)
	}
	if len(req.OptionalCommands) != 0 || len(req.GoCommands) != 0 {
		t.Fatalf("requires not replaced as block: %#v", req)
	}
}

func TestLoadIncludeEmptyRequirementsReplaceAsBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "common.shadowtree.toml"), []byte(`
[recipes.build]
cmd = "go build"

[recipes.build.requires]
commands = ["go"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
include = ["./common.shadowtree.toml"]

[recipes.build.requires]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if req := loaded.Config.Recipes["build"].Requires; !req.Empty() {
		t.Fatalf("requires = %#v, want empty replacement", req)
	}
}

func TestLoadLaterIncludeEmptyRequirementsReplaceEarlierInclude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.shadowtree.toml"), []byte(`
[recipes.build]
cmd = "go build"

[recipes.build.requires]
commands = ["go"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.shadowtree.toml"), []byte(`
[recipes.build]
cmd = "go build"

[recipes.build.requires]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`include = ["./a.shadowtree.toml", "./b.shadowtree.toml"]`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if req := loaded.Config.Recipes["build"].Requires; !req.Empty() {
		t.Fatalf("requires = %#v, want empty replacement from later include", req)
	}
}

func TestLoadRejectsMissingInclude(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`include = ["missing.shadowtree.toml"]`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `include "missing.shadowtree.toml"`) {
		t.Fatalf("Load() error = %v, want include context", err)
	}
}

func TestLoadRejectsIncludeCycle(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.shadowtree.toml")
	b := filepath.Join(dir, "b.shadowtree.toml")
	if err := os.WriteFile(a, []byte(`include = ["./b.shadowtree.toml"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(`include = ["./a.shadowtree.toml"]`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(a)
	if err == nil || !strings.Contains(err.Error(), "include cycle:") {
		t.Fatalf("Load() error = %v, want cycle", err)
	}
}

func TestResolveRecipesDoesNotDetectProfileWhenConfigOmitsProfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
[recipes.assets]
cmd = "npm test"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	recipes, profile, err := ResolveRecipes(t.Context(), loaded, dir, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if profile != "" {
		t.Fatalf("profile = %q, want none", profile)
	}
	if _, ok := recipes["assets"]; !ok {
		t.Fatalf("recipes missing configured recipe: %#v", recipes)
	}
	if _, ok := recipes["test"]; ok {
		t.Fatalf("recipes include Go builtin test: %#v", recipes)
	}
}

func TestResolveRecipesRejectsRecipeWithoutCommand(t *testing.T) {
	loaded := Loaded{Config: recipe.Config{Recipes: map[string]recipe.Recipe{
		"broken": {Help: "Missing command."},
	}}}

	_, _, err := ResolveRecipes(t.Context(), loaded, t.TempDir(), ResolveOptions{})
	if err == nil || !strings.Contains(err.Error(), `recipe "broken" has no cmd`) {
		t.Fatalf("ResolveRecipes() error = %v, want missing cmd", err)
	}
}

func TestResolveRecipesDetectsProfileWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	recipes, profile, err := ResolveRecipes(t.Context(), Loaded{}, dir, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if profile != recipe.GoProfile {
		t.Fatalf("profile = %q, want %q", profile, recipe.GoProfile)
	}
	if _, ok := recipes["test"]; !ok {
		t.Fatalf("recipes missing Go builtin test: %#v", recipes)
	}
}

func TestResolveRecipesDetectsNodeProfileWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"test":"vitest"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	recipes, profile, err := ResolveRecipes(t.Context(), Loaded{}, dir, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if profile != recipe.NodeProfile {
		t.Fatalf("profile = %q, want %q", profile, recipe.NodeProfile)
	}
	if _, ok := recipes["install"]; !ok {
		t.Fatalf("recipes missing Node builtin install: %#v", recipes)
	}
}

func TestResolveRecipesUsesExplicitConfigProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`profile = "go"`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	recipes, profile, err := ResolveRecipes(t.Context(), loaded, dir, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if profile != recipe.GoProfile {
		t.Fatalf("profile = %q, want %q", profile, recipe.GoProfile)
	}
	if _, ok := recipes["test"]; !ok {
		t.Fatalf("recipes missing Go builtin test: %#v", recipes)
	}
}

func TestResolveRecipesUsesOptionsProfileWithConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
[recipes.assets]
cmd = "npm test"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	recipes, profile, err := ResolveRecipes(t.Context(), loaded, dir, ResolveOptions{Profile: recipe.GoProfile})
	if err != nil {
		t.Fatal(err)
	}
	if profile != recipe.GoProfile {
		t.Fatalf("profile = %q, want %q", profile, recipe.GoProfile)
	}
	if _, ok := recipes["assets"]; !ok {
		t.Fatalf("recipes missing configured recipe: %#v", recipes)
	}
	if _, ok := recipes["test"]; !ok {
		t.Fatalf("recipes missing Go builtin test: %#v", recipes)
	}
}

func TestResolveRecipesUsesOptionsNodeProfileWithConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"test":"vitest"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
[recipes.assets]
cmd = "npm test"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	recipes, profile, err := ResolveRecipes(t.Context(), loaded, dir, ResolveOptions{Profile: recipe.NodeProfile})
	if err != nil {
		t.Fatal(err)
	}
	if profile != recipe.NodeProfile {
		t.Fatalf("profile = %q, want %q", profile, recipe.NodeProfile)
	}
	if _, ok := recipes["assets"]; !ok {
		t.Fatalf("recipes missing configured recipe: %#v", recipes)
	}
	if _, ok := recipes["install"]; !ok {
		t.Fatalf("recipes missing Node builtin install: %#v", recipes)
	}
}

func TestResolveRecipesExpandsStaticVarsWithDynamicVars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
[vars]
docs_dir = "{repo_root}/docs"

[var_commands]
repo_root = "printf /repo"

[recipes.show]
cmd = "printf %s {docs_dir}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	recipes, _, err := ResolveRecipes(t.Context(), loaded, dir, ResolveOptions{EvalDynamicVars: true})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("show", recipes["show"], nil, nil, nil, loaded.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := recipe.ScriptBody(resolved.Main); got != "printf %s /repo/docs" {
		t.Fatalf("cmd = %q, want dynamic var expanded in static var", got)
	}
}

func TestResolveRecipesKeepsDynamicVarsAvailableWithoutEvaluation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
[vars]
docs_dir = "{repo_root}/docs"

[var_commands]
repo_root = "exit 1"

[recipes.show]
cmd = "printf %s {docs_dir}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	recipes, _, err := ResolveRecipes(t.Context(), loaded, dir, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("show", recipes["show"], nil, nil, nil, loaded.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := recipe.ScriptBody(resolved.Main); got != "printf %s {repo_root}/docs" {
		t.Fatalf("cmd = %q, want dynamic var placeholder preserved", got)
	}
}

func TestResolveRecipesKeepsDynamicVarOutputOpaque(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
[vars]
message = "{meta}"

[var_commands]
meta = "printf '{\"branch\":\"{name}\"}'"

[recipes.show]
cmd = "printf %s {message}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	recipes, _, err := ResolveRecipes(t.Context(), loaded, dir, ResolveOptions{EvalDynamicVars: true})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("show", recipes["show"], nil, nil, nil, loaded.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := recipe.ScriptBody(resolved.Main); got != `printf %s {"branch":"{name}"}` {
		t.Fatalf("cmd = %q, want dynamic var output left opaque", got)
	}
}
