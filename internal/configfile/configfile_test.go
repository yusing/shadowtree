package configfile

import (
	"os"
	"path/filepath"
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
cmd = "go test {pkg} {@}"
pre = ["go generate ./..."]

[recipes.test.arguments.pkg]
type = "rel_path"
default = "."

[recipes.test.arguments.count]
help = "Repeat count."
type = "int"
default = 1
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
	if got := recipe.ScriptBody(loaded.Config.Recipes["test"].Pre[0]); got != "go generate ./..." {
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
	if got := loaded.Config.Recipes["test"].Arguments["count"].Default; got == nil {
		t.Fatal("count default is nil")
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
	for _, name := range recipe.BuiltinNames(recipe.GoProfile) {
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
	output, err := recipe.CommandOutput(t.Context(), filepath.Dir(path), nil, pre[0])
	if err != nil {
		t.Fatal(err)
	}
	if output != "123\n" {
		t.Fatalf("pre[0] output = %q, want echo output", output)
	}
	if len(pre[1]) != 1 || pre[1][0] != "@foo" {
		t.Fatalf("pre[1] = %#v, want recipe reference", pre[1])
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
