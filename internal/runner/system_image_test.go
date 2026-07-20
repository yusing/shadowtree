package runner

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
)

func TestResolvedSystemImageRecipeCollectsTransitiveRequirements(t *testing.T) {
	source := t.TempDir()
	configPath := filepath.Join(source, ".shadowtree.toml")
	recipes := map[string]recipe.Recipe{
		"root": {
			Cmd:       recipe.ScriptCommand("@child\n"),
			Sandboxed: new(recipe.SandboxModeSystem),
			Requires:  recipe.Requirements{SystemPackages: []string{"git"}},
		},
		"child": {
			Cmd:      recipe.Command{"@grandchild"},
			Requires: recipe.Requirements{GoCommands: map[string]string{"stringer": "golang.org/x/tools/cmd/stringer@v0.34.0"}},
		},
		"grandchild": {
			Cmd:      recipe.Command{"true"},
			Requires: recipe.Requirements{SystemPackages: []string{"ca-certificates"}, NodeCommands: map[string]string{"eslint": "eslint@9.30.0"}},
		},
	}
	resolved, err := recipe.ResolveWithOptions("root", recipes["root"], nil, nil, nil, configPath, recipe.GoProfile, recipe.ResolveOptions{Recipes: recipes})
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolvedSystemImageRecipe(t.Context(), Options{Resolved: resolved, Recipes: recipes, SourceDir: source})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Recipe.Requires.SystemPackages, []string{"git", "ca-certificates"}) {
		t.Fatalf("system packages = %#v", got.Recipe.Requires.SystemPackages)
	}
	if got.Recipe.Requires.GoCommands["stringer"] == "" || got.Recipe.Requires.NodeCommands["eslint"] == "" {
		t.Fatalf("installable requirements = %#v / %#v", got.Recipe.Requires.GoCommands, got.Recipe.Requires.NodeCommands)
	}
}

func TestResolvedSystemImageRecipeCollectsCrossConfigRequirements(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(source, "tools")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".shadowtree.toml"), []byte(`
[recipes.generate]
cmd = "true"

[recipes.generate.requires]
system_packages = ["protobuf-compiler"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	root := recipe.Recipe{Cmd: recipe.Command{"@tools:generate"}, Sandboxed: new(recipe.SandboxModeSystem)}
	resolved, err := recipe.Resolve("root", root, nil, nil, nil, filepath.Join(source, ".shadowtree.toml"), "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolvedSystemImageRecipe(t.Context(), Options{Resolved: resolved, Recipes: map[string]recipe.Recipe{"root": root}, SourceDir: source})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Recipe.Requires.SystemPackages, "protobuf-compiler") {
		t.Fatalf("system packages = %#v", got.Recipe.Requires.SystemPackages)
	}
}

func TestResolvedSystemImageRecipeRejectsCrossConfigProfileMismatch(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(source, "web")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".shadowtree.toml"), []byte("profile = \"node\"\n[recipes.build]\ncmd = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := recipe.Recipe{Cmd: recipe.Command{"@web:build"}, Sandboxed: new(recipe.SandboxModeSystem)}
	resolved, err := recipe.Resolve("root", root, nil, nil, nil, filepath.Join(source, ".shadowtree.toml"), recipe.GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolvedSystemImageRecipe(t.Context(), Options{Resolved: resolved, Recipes: map[string]recipe.Recipe{"root": root}, SourceDir: source})
	if err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("resolvedSystemImageRecipe error = %v, want profile incompatibility", err)
	}
}

func TestResolvedSystemImageRecipeRejectsIncompatibleNestedContract(t *testing.T) {
	root := recipe.Recipe{Cmd: recipe.Command{"@child"}, Sandboxed: new(recipe.SandboxModeSystem)}
	for name, child := range map[string]recipe.Recipe{
		"host": {Cmd: recipe.Command{"true"}, Sandboxed: new(recipe.SandboxModeHost)},
		"base": {Cmd: recipe.Command{"true"}, Sandboxed: new(recipe.SandboxModeSystem), System: &recipe.SystemConfig{BaseImage: "debian:12.11-slim"}},
	} {
		t.Run(name, func(t *testing.T) {
			resolved, err := recipe.ResolveWithOptions("root", root, nil, nil, nil, "/project/.shadowtree.toml", "", recipe.ResolveOptions{Recipes: map[string]recipe.Recipe{"root": root, "child": child}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolvedSystemImageRecipe(t.Context(), Options{Resolved: resolved, Recipes: map[string]recipe.Recipe{"root": root, "child": child}, SourceDir: "/project"})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("resolvedSystemImageRecipe error = %v, want %s incompatibility", err, name)
			}
		})
	}
}

func TestResolvedSystemImageRecipeRejectsConflictingInstallableRequirements(t *testing.T) {
	root := recipe.Recipe{Cmd: recipe.Command{"@child"}, Sandboxed: new(recipe.SandboxModeSystem), Requires: recipe.Requirements{GoCommands: map[string]string{"tool": "example.com/tool@v1.2.3"}}}
	child := recipe.Recipe{Cmd: recipe.Command{"true"}, Requires: recipe.Requirements{GoCommands: map[string]string{"tool": "example.com/tool@v1.2.4"}}}
	resolved, err := recipe.ResolveWithOptions("root", root, nil, nil, nil, "/project/.shadowtree.toml", "", recipe.ResolveOptions{Recipes: map[string]recipe.Recipe{"root": root, "child": child}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolvedSystemImageRecipe(t.Context(), Options{Resolved: resolved, Recipes: map[string]recipe.Recipe{"root": root, "child": child}, SourceDir: "/project"})
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("resolvedSystemImageRecipe error = %v, want conflicting requirement rejection", err)
	}
}

func TestResolvedSystemImageRecipeCollectsParameterizedReferenceVariants(t *testing.T) {
	root := recipe.Recipe{Cmd: recipe.ScriptCommand("@choose[target=one]\n@choose[target=two]\n"), Sandboxed: new(recipe.SandboxModeSystem)}
	choose := recipe.Recipe{
		Cmd:       recipe.Command{"@{target}"},
		Arguments: map[string]recipe.Argument{"target": {Required: true}},
	}
	recipes := map[string]recipe.Recipe{
		"root":   root,
		"choose": choose,
		"one":    {Cmd: recipe.Command{"true"}, Requires: recipe.Requirements{SystemPackages: []string{"first-package"}}},
		"two":    {Cmd: recipe.Command{"true"}, Requires: recipe.Requirements{SystemPackages: []string{"second-package"}}},
	}
	resolved, err := recipe.ResolveWithOptions("root", root, nil, nil, nil, "/project/.shadowtree.toml", "", recipe.ResolveOptions{Recipes: recipes})
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolvedSystemImageRecipe(t.Context(), Options{Resolved: resolved, Recipes: recipes, SourceDir: "/project"})
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"first-package", "second-package"} {
		if !slices.Contains(got.Recipe.Requires.SystemPackages, pkg) {
			t.Fatalf("system packages = %#v, missing %q", got.Recipe.Requires.SystemPackages, pkg)
		}
	}
}

func TestResolvedSystemImageRecipeReportsReferenceCycleEdges(t *testing.T) {
	source := t.TempDir()
	configPath := filepath.Join(source, ".shadowtree.toml")
	prelude := "ensure_webui_dist() {\n  @build-webui\n}"
	recipes := map[string]recipe.Recipe{
		"tcp-echo-test": {
			Sandboxed:    new(recipe.SandboxModeSystem),
			ShellPrelude: prelude,
			Cmd:          recipe.ScriptCommand("bun --bun scripts/tcp_echo_test.ts"),
		},
		"build-webui": {
			ShellPrelude: prelude,
			Cmd:          recipe.ScriptCommand("bun run build"),
		},
	}
	resolved, err := recipe.ResolveWithOptions("tcp-echo-test", recipes["tcp-echo-test"], nil, nil, nil, configPath, "", recipe.ResolveOptions{Recipes: recipes})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolvedSystemImageRecipe(t.Context(), Options{Resolved: resolved, Recipes: recipes, SourceDir: source})
	if err == nil {
		t.Fatal("resolvedSystemImageRecipe succeeded, want cycle")
	}
	want := `error: system image recipe reference cycle

  --> ` + filepath.ToSlash(configPath) + `
   |
   | recipe "tcp-echo-test" · cmd inherits shell_prelude
   | expanded shell_prelude:2:3
   |   @build-webui
   |   ^^^^^^^^^^^^ references recipe "build-webui"

  ::: ` + filepath.ToSlash(configPath) + `
   |
   | recipe "build-webui" · cmd inherits shell_prelude
   | expanded shell_prelude:2:3
   |   @build-webui
   |   ^^^^^^^^^^^^ references recipe "build-webui"
   |                cycle closes here`
	if err.Error() != want {
		t.Fatalf("cycle error:\n%s\n\nwant:\n%s", err, want)
	}
}

func TestResolvedSystemImageRecipeDoesNotConfuseSameRecipeNameAcrossConfigs(t *testing.T) {
	source := t.TempDir()
	childDir := filepath.Join(source, "child")
	if err := os.Mkdir(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, ".shadowtree.toml"), []byte(`
[recipes.build]
cmd = "true"

[recipes.build.requires]
system_packages = ["ca-certificates"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	root := recipe.Recipe{Cmd: recipe.Command{"@child:build"}, Sandboxed: new(recipe.SandboxModeSystem)}
	configPath := filepath.Join(source, ".shadowtree.toml")
	resolved, err := recipe.Resolve("build", root, nil, nil, nil, configPath, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolvedSystemImageRecipe(t.Context(), Options{Resolved: resolved, Recipes: map[string]recipe.Recipe{"build": root}, SourceDir: source})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Recipe.Requires.SystemPackages, "ca-certificates") {
		t.Fatalf("system packages = %v, want cross-config requirement", got.Recipe.Requires.SystemPackages)
	}
}

func TestResolvedSystemImageRecipeCollectsForEachShellPreludeRequirements(t *testing.T) {
	source := t.TempDir()
	configPath := filepath.Join(source, ".shadowtree.toml")
	recipes := map[string]recipe.Recipe{
		"root": {
			Sandboxed:    new(recipe.SandboxModeSystem),
			ShellPrelude: "@prepare-items",
			ForEach:      recipe.ScriptCommand("printf '%s\\n' item"),
			Cmd:          recipe.Command{"true"},
		},
		"prepare-items": {
			Cmd:      recipe.Command{"true"},
			Requires: recipe.Requirements{SystemPackages: []string{"for-each-package"}},
		},
	}
	resolved, err := recipe.ResolveWithOptions("root", recipes["root"], nil, nil, nil, configPath, "", recipe.ResolveOptions{Recipes: recipes})
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolvedSystemImageRecipe(t.Context(), Options{Resolved: resolved, Recipes: recipes, SourceDir: source})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Recipe.Requires.SystemPackages, "for-each-package") {
		t.Fatalf("system packages = %v, want for_each prelude requirement", got.Recipe.Requires.SystemPackages)
	}
}

func TestResolvedSystemImageRecipeReferenceDiagnosticsRetainOrigin(t *testing.T) {
	for _, test := range []struct {
		name string
		rec  recipe.Recipe
		want []string
	}{
		{
			name: "malformed script",
			rec:  recipe.Recipe{Cmd: recipe.ScriptCommand("if then"), Sandboxed: new(recipe.SandboxModeSystem)},
			want: []string{`recipe "root"`, "cmd system image references: script:"},
		},
		{
			name: "unknown reference",
			rec:  recipe.Recipe{Cmd: recipe.ScriptCommand("printf ready\\n\n  @future-recipe"), Sandboxed: new(recipe.SandboxModeSystem)},
			want: []string{`recipe "root"`, "cmd script line 2:3 references unknown recipe @future-recipe"},
		},
		{
			name: "future shell",
			rec:  recipe.Recipe{Shell: "future-shell", Cmd: recipe.ScriptCommand("printf ready\n@child"), Sandboxed: new(recipe.SandboxModeSystem)},
			want: []string{`recipe "root"`, `cmd system image references: script: script recipe references require shell sh or bash, got "future-shell"`},
		},
		{
			name: "for_each prelude reference",
			rec:  recipe.Recipe{ShellPrelude: "  @future-recipe", ForEach: recipe.ScriptCommand("printf item"), Cmd: recipe.Command{"true"}, Sandboxed: new(recipe.SandboxModeSystem)},
			want: []string{`recipe "root"`, "for_each shell_prelude line 1:3 references unknown recipe @future-recipe"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := t.TempDir()
			configPath := filepath.Join(source, ".shadowtree.toml")
			resolved, err := recipe.Resolve("root", test.rec, nil, nil, nil, configPath, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolvedSystemImageRecipe(t.Context(), Options{Resolved: resolved, Recipes: map[string]recipe.Recipe{"root": test.rec}, SourceDir: source})
			if err == nil {
				t.Fatal("resolvedSystemImageRecipe succeeded, want diagnostic")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error missing %q:\n%s", want, err)
				}
			}
		})
	}
}

func TestImageRecipeReferencesDescribeInvocationOrigin(t *testing.T) {
	prelude := "prepare() {\n  @prelude-ref\n}"
	for _, test := range []struct {
		name    string
		command recipe.Command
		prelude string
		wantRef string
		want    string
	}{
		{name: "direct", command: recipe.Command{"@direct-ref"}, wantRef: "direct-ref", want: "directly"},
		{name: "retry", command: recipe.Command{"@retry[count=2]", "@retry-ref"}, wantRef: "retry-ref", want: "through @retry"},
		{name: "script", command: recipe.ScriptCommand("printf ready\n  @script-ref"), wantRef: "script-ref", want: "script line 2:3"},
		{name: "prelude", command: recipe.CommandWithRecipeReference(recipe.ScriptCommand("printf ready"), "sh", prelude), prelude: prelude, wantRef: "prelude-ref", want: "shell_prelude line 2:3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			refs, err := imageRecipeReferences(test.command, test.prelude)
			if err != nil {
				t.Fatal(err)
			}
			if len(refs) != 1 || refs[0].target.Name != test.wantRef || refs[0].origin.description() != test.want {
				t.Fatalf("references = %#v, want @%s from %q", refs, test.wantRef, test.want)
			}
		})
	}

	refs, err := imageRecipeReferences(recipe.Command{"printf", "unrelated"}, "")
	if err != nil || len(refs) != 0 {
		t.Fatalf("unrelated command references = %#v, error = %v", refs, err)
	}
}
