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
