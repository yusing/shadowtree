package completion

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/configfile"
	"github.com/yusing/shadowtree/internal/globalflag"
	"github.com/yusing/shadowtree/internal/recipe"
)

func TestCandidatesIncludeRecipesForEmptyCurrentWord(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", ""}, map[string]recipe.Recipe{
		"test": {Help: "Run tests.", Cmd: recipe.Command{"go", "test", "{pkg}", "{@}"}},
	})

	if !hasCandidate(candidates, "test") {
		t.Fatalf("candidates = %#v, want test", candidates)
	}
	if got := helpFor(candidates, "test"); got != "Run tests." {
		t.Fatalf("test help = %q", got)
	}
}

func TestCandidatesCompleteRecipePrefix(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "te"}, map[string]recipe.Recipe{
		"build": {Help: "Build binary.", Cmd: recipe.Command{"go", "build"}},
		"test":  {Help: "Run tests.", Cmd: recipe.Command{"go", "test"}},
	})

	if len(candidates) != 1 || candidates[0].Value != "test" {
		t.Fatalf("candidates = %#v, want test only", candidates)
	}
}

func TestCandidatesCompleteProfileValues(t *testing.T) {
	tests := []struct {
		name  string
		words []string
	}{
		{name: "current flag", words: []string{"shadowtree", "--profile"}},
		{name: "after space", words: []string{"shadowtree", "--profile", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := complete(t, tt.words, nil)

			if !hasCandidate(candidates, "go") || !hasCandidate(candidates, "node") {
				t.Fatalf("candidates = %#v, want go and node", candidates)
			}
		})
	}
}

func TestCandidatesSeparateExecCommandFromRunRecipe(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", ""}, map[string]recipe.Recipe{
		"run": {Help: "Run a Go command.", Cmd: recipe.Command{"go", "run"}},
	})

	if !hasCandidate(candidates, "exec") {
		t.Fatalf("candidates = %#v, want exec", candidates)
	}
	if got := helpFor(candidates, "run"); got != "Run a Go command." {
		t.Fatalf("run help = %q", got)
	}
}

func TestCandidatesCompleteRecipesAfterHelp(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "help", ""}, map[string]recipe.Recipe{
		"test": {Help: "Run tests.", Cmd: recipe.Command{"go", "test"}},
	})

	if len(candidates) != 1 || candidates[0].Value != "test" {
		t.Fatalf("candidates = %#v, want test only", candidates)
	}
}

func TestCandidatesCompleteIncludedRecipesAndArguments(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "common.shadowtree.toml"), []byte(`
[recipes.common.arguments.target]
help = "Target to build."

[recipes.common]
help = "Common recipe."
cmd = "echo {target}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`include = ["./common.shadowtree.toml"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := configfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	recipes, _, err := configfile.ResolveRecipes(t.Context(), loaded, dir, configfile.ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}

	candidates := complete(t, []string{"shadowtree", ""}, recipes)
	if !hasCandidate(candidates, "common") {
		t.Fatalf("candidates = %#v, want included common recipe", candidates)
	}
	if got := helpFor(candidates, "common"); got != "Common recipe." {
		t.Fatalf("common help = %q", got)
	}
	candidates = complete(t, []string{"shadowtree", "common", ""}, recipes)
	if !hasCandidate(candidates, "target=") {
		t.Fatalf("candidates = %#v, want included target argument", candidates)
	}
}

func TestCandidatesCompleteHelpColorOptionAfterRecipe(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "help", "test", ""}, map[string]recipe.Recipe{
		"test": {Help: "Run tests.", Cmd: recipe.Command{"go", "test"}},
	})

	if len(candidates) != 1 || candidates[0].Value != "color=false" {
		t.Fatalf("candidates = %#v, want color=false", candidates)
	}
}

func TestCandidatesCompletePartialHelpColorOption(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "help", "test", "col"}, map[string]recipe.Recipe{
		"test": {Help: "Run tests.", Cmd: recipe.Command{"go", "test"}},
	})

	if len(candidates) != 1 || candidates[0].Value != "color=false" {
		t.Fatalf("candidates = %#v, want color=false", candidates)
	}
}

func TestCandidatesSkipHelpColorOptionAfterUnknownRecipe(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "help", "missing", ""}, map[string]recipe.Recipe{
		"test": {Help: "Run tests.", Cmd: recipe.Command{"go", "test"}},
	})

	if hasCandidate(candidates, "color=false") {
		t.Fatalf("candidates = %#v, want no color=false", candidates)
	}
}

func TestCandidatesCompleteSpacedRecipeArguments(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build", ""}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {Help: "Go package to build.", Type: "string", Position: 1},
			},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "project=" {
		t.Fatalf("candidates = %#v, want project=", candidates)
	}
}

func TestCandidatesCompleteRecipePresetArgument(t *testing.T) {
	rec := recipe.Recipe{
		Cmd: recipe.Command{"benchmark"},
		Arguments: map[string]recipe.Argument{
			"connections": {Type: "int"},
		},
		Presets: map[string]recipe.RecipePreset{
			"stable": {},
			"stress": {},
		},
	}

	candidates := complete(t, []string{"shadowtree", "benchmark", ""}, map[string]recipe.Recipe{"benchmark": rec})
	if !hasCandidate(candidates, "preset=") || !hasCandidate(candidates, "connections=") {
		t.Fatalf("candidates = %#v, want preset and typed argument names", candidates)
	}
	if got := helpFor(candidates, "preset="); got != "recipe preset" {
		t.Fatalf("preset help = %q", got)
	}

	candidates = complete(t, []string{"shadowtree", "benchmark", "preset=st"}, map[string]recipe.Recipe{"benchmark": rec})
	if len(candidates) != 2 || candidates[0].Value != "preset=stable" || candidates[1].Value != "preset=stress" {
		t.Fatalf("candidates = %#v, want preset values", candidates)
	}

	candidates = complete(t, []string{"shadowtree", "benchmark[preset=st"}, map[string]recipe.Recipe{"benchmark": rec})
	if len(candidates) != 2 || candidates[0].Value != "benchmark[preset=stable" || candidates[1].Value != "benchmark[preset=stress" {
		t.Fatalf("candidates = %#v, want bracket preset values", candidates)
	}
}

func TestCandidatesPreferSpacedArgumentNameOverPositionalPath(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "bin"))
	candidates := completeWithOptions(t, []string{"shadowtree", "build", "bin"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"binary":  {Help: "Output binary name.", Type: "string"},
				"project": {Help: "Go package to build.", Type: "rel_path", Position: 1},
			},
		},
	}, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0].Value != "binary=" {
		t.Fatalf("candidates = %#v, want binary=", candidates)
	}
}

func TestCandidatesCompleteBracketRecipeArguments(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build["}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {Help: "Go package to build.", Type: "string", Position: 1},
			},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "build[project=" {
		t.Fatalf("candidates = %#v, want build[project=", candidates)
	}
}

func TestCandidatesPreferBracketArgumentNameOverPositionalPath(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "bin"))
	candidates := completeWithOptions(t, []string{"shadowtree", "build[bin"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"binary":  {Help: "Output binary name.", Type: "string"},
				"project": {Help: "Go package to build.", Type: "rel_path", Position: 1},
			},
		},
	}, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0].Value != "build[binary=" {
		t.Fatalf("candidates = %#v, want build[binary=", candidates)
	}
}

func TestCandidatesCompleteBracketRecipeArgumentPrefix(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build[proj"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"binary":  {Help: "Output binary name.", Type: "string"},
				"project": {Help: "Go package to build.", Type: "string", Position: 1},
			},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "build[project=" {
		t.Fatalf("candidates = %#v, want build[project=", candidates)
	}
}

func TestCandidatesCompleteSplitBracketRecipeArgumentPrefix(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build[", "proj"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"binary":  {Help: "Output binary name.", Type: "string"},
				"project": {Help: "Go package to build.", Type: "string", Position: 1},
			},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "build[project=" {
		t.Fatalf("candidates = %#v, want build[project=", candidates)
	}
}

func TestCandidatesCompleteBoolArgumentValues(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "test", "race="}, map[string]recipe.Recipe{
		"test": {
			Cmd: recipe.Command{"go", "test"},
			Arguments: map[string]recipe.Argument{
				"race": {Type: "bool"},
			},
		},
	})

	if len(candidates) != 2 || candidates[0].Value != "race=true" || candidates[1].Value != "race=false" {
		t.Fatalf("candidates = %#v, want race bool values", candidates)
	}
}

func TestCandidatesCompleteDynamicArgumentValues(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build[project=s"}, map[string]recipe.Recipe{
		"build": {
			Cmd:          recipe.Command{"go", "build"},
			ShellPrelude: "project_values() { printf 'sip/scheduler\\tScheduler daemon\\ntools/agi\\tFastAGI server\\n'; }",
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand("project_values"),
				},
			},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "build[project=sip/scheduler" || candidates[0].Help != "Scheduler daemon" {
		t.Fatalf("candidates = %#v, want sip scheduler value", candidates)
	}
}

func TestCandidatesCompleteDynamicArgumentValuesExpandPreludePlaceholders(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build[project=c"}, map[string]recipe.Recipe{
		"build": {
			Cmd:          recipe.Command{"go", "build"},
			ShellPrelude: "project_values() { printf '%s\\tfrom command\\n' \"{project}\"; }",
			Vars:         map[string]string{"project": "cmd/api"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand("project_values"),
				},
			},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "build[project=cmd/api" || candidates[0].Help != "from command" {
		t.Fatalf("candidates = %#v, want cmd/api value", candidates)
	}
}

func TestCandidatesCompleteEnumArgumentValues(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build", "project=a"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand(`@enum api worker "admin ui"`),
				},
			},
		},
	})

	if len(candidates) != 2 || candidates[0].Value != "project=api" || candidates[1].Value != "project=admin ui" {
		t.Fatalf("candidates = %#v, want filtered enum values", candidates)
	}
}

func TestCandidatesCompleteEnumArgumentValuesWithHelp(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build", "project=a"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand("@enum all='all modules' api='API service'"),
				},
			},
		},
	})

	if len(candidates) != 2 ||
		candidates[0] != (Candidate{Value: "project=all", Help: "all modules"}) ||
		candidates[1] != (Candidate{Value: "project=api", Help: "API service"}) {
		t.Fatalf("candidates = %#v, want enum values with help", candidates)
	}
}

func TestCandidatesCompleteGoModulesBuiltinArgumentValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdirAll(t, filepath.Join(dir, "services", "api"))
	if err := os.WriteFile(filepath.Join(dir, "services", "api", "go.mod"), []byte("module example.com/api\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	candidates := completeWithOptions(t, []string{"shadowtree", "test", "target=s"}, map[string]recipe.Recipe{
		"test": {
			Cmd: recipe.Command{"go", "test"},
			Arguments: map[string]recipe.Argument{
				"target": {
					Type:   "string",
					Values: recipe.ScriptCommand("@go-modules"),
				},
			},
		},
	}, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0] != (Candidate{Value: "target=services/api", Help: "example.com/api"}) {
		t.Fatalf("candidates = %#v, want services/api module value", candidates)
	}
}

func TestCandidatesCompleteComposedBuiltinArgumentValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdirAll(t, filepath.Join(dir, "services", "api"))
	if err := os.WriteFile(filepath.Join(dir, "services", "api", "go.mod"), []byte("module example.com/api\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	candidates := completeWithOptions(t, []string{"shadowtree", "test", "target=a"}, map[string]recipe.Recipe{
		"test": {
			Cmd: recipe.Command{"go", "test"},
			Arguments: map[string]recipe.Argument{
				"target": {
					Type:   "string",
					Values: recipe.ScriptCommand("@go-modules; @enum all='all modules'"),
				},
			},
		},
	}, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0] != (Candidate{Value: "target=all", Help: "all modules"}) {
		t.Fatalf("candidates = %#v, want composed all value", candidates)
	}
}

func TestCandidatesCompleteGoPackagesBuiltinArgumentValues(t *testing.T) {
	dir := t.TempDir()
	writeTextFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	mkdirAll(t, filepath.Join(dir, "services", "api"))
	writeTextFile(t, filepath.Join(dir, "services", "api", "api.go"), "package api\n")

	recipes := map[string]recipe.Recipe{
		"test": {
			Cmd: recipe.Command{"go", "test", "{pkg}"},
			Arguments: map[string]recipe.Argument{
				"pkg": {
					Type:   "rel_path",
					Values: recipe.ScriptCommand("@go-packages"),
				},
			},
		},
	}
	candidates := completeWithOptions(t, []string{"shadowtree", "test", "pkg=./s"}, recipes, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0] != (Candidate{Value: "pkg=./services/api", Help: "example.com/root/services/api"}) {
		t.Fatalf("candidates = %#v, want services/api package value", candidates)
	}
}

func TestCandidatesCompleteGoPackagesBuiltinArgumentValuesAcrossWorkspaceModules(t *testing.T) {
	dir := t.TempDir()
	writeTextFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	writeTextFile(t, filepath.Join(dir, "go.work"), "go 1.26\n\nuse (\n\t.\n\t./services/api\n)\n")
	mkdirAll(t, filepath.Join(dir, "services", "api", "internal", "handler"))
	writeTextFile(t, filepath.Join(dir, "services", "api", "go.mod"), "module example.com/api\n")
	writeTextFile(t, filepath.Join(dir, "services", "api", "internal", "handler", "handler.go"), "package handler\n")

	recipes := map[string]recipe.Recipe{
		"test": {
			Cmd: recipe.Command{"go", "test", "{pkg}"},
			Arguments: map[string]recipe.Argument{
				"pkg": {
					Type:   "rel_path",
					Values: recipe.ScriptCommand("@go-packages"),
				},
			},
		},
	}
	candidates := completeWithOptions(t, []string{"shadowtree", "test", "pkg=./services/"}, recipes, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0] != (Candidate{Value: "pkg=./services/api/internal/handler", Help: "example.com/api/internal/handler"}) {
		t.Fatalf("candidates = %#v, want nested module package value", candidates)
	}
}

func TestCandidatesDoNotInventGoPackagesAllPackagesValue(t *testing.T) {
	dir := t.TempDir()
	writeTextFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	mkdirAll(t, filepath.Join(dir, "internal", "lib"))
	writeTextFile(t, filepath.Join(dir, "internal", "lib", "lib.go"), "package lib\n")

	recipes := map[string]recipe.Recipe{
		"test": {
			Cmd: recipe.Command{"go", "test", "{pkg}"},
			Arguments: map[string]recipe.Argument{
				"pkg": {
					Type:   "rel_path",
					Values: recipe.ScriptCommand("@go-packages"),
				},
			},
		},
	}
	candidates := completeWithOptions(t, []string{"shadowtree", "test", "pkg=./"}, recipes, Options{Dir: dir})

	if hasCandidate(candidates, "pkg=./...") {
		t.Fatalf("candidates = %#v, want no synthetic all-packages value", candidates)
	}
	if len(candidates) != 1 || candidates[0] != (Candidate{Value: "pkg=./internal/lib", Help: "example.com/root/internal/lib"}) {
		t.Fatalf("candidates = %#v, want real package value", candidates)
	}
}

func TestCandidatesCompleteGoMainPackagesBuiltinArgumentValues(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "cmd", "api"))
	writeTextFile(t, filepath.Join(dir, "cmd", "api", "main.go"), "// Package main builds the API.\npackage main\n")

	candidates := completeWithOptions(t, []string{"shadowtree", "build", "project=./cmd/"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand("@go-main-packages"),
				},
			},
		},
	}, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0] != (Candidate{Value: "project=./cmd/api", Help: "Package main builds the API."}) {
		t.Fatalf("candidates = %#v, want cmd/api main package value", candidates)
	}
}

func TestCandidatesCompleteGoBuiltinRunCommandArgumentValues(t *testing.T) {
	dir := t.TempDir()
	writeTextFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	mkdirAll(t, filepath.Join(dir, "services", "api"))
	writeTextFile(t, filepath.Join(dir, "services", "api", "go.mod"), "module example.com/api\n")
	mkdirAll(t, filepath.Join(dir, "cmd", "api"))
	writeTextFile(t, filepath.Join(dir, "cmd", "api", "main.go"), "// Package main builds the API.\npackage main\n")

	recipes := recipe.Builtins(recipe.GoProfile, recipe.BuiltinOptions{Dir: dir})
	candidates := completeWithOptions(t, []string{"shadowtree", "run", "command=./cmd/"}, recipes, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0] != (Candidate{Value: "command=./cmd/api", Help: "Package main builds the API."}) {
		t.Fatalf("candidates = %#v, want cmd/api main package value", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "run", "cwd=s"}, recipes, Options{Dir: dir})
	if len(candidates) != 1 || candidates[0] != (Candidate{Value: "cwd=services/api", Help: "example.com/api"}) {
		t.Fatalf("candidates = %#v, want services/api cwd value", candidates)
	}
}

func TestCandidatesCompleteGoBuiltinPositionals(t *testing.T) {
	dir := t.TempDir()
	writeTextFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	writeTextFile(t, filepath.Join(dir, "root.go"), "package root\n")
	mkdirAll(t, filepath.Join(dir, "cmd", "api"))
	writeTextFile(t, filepath.Join(dir, "cmd", "api", "main.go"), "package main\n")
	mkdirAll(t, filepath.Join(dir, "internal", "lib"))
	writeTextFile(t, filepath.Join(dir, "internal", "lib", "lib.go"), "package lib\n")

	recipes := recipe.Builtins(recipe.GoProfile, recipe.BuiltinOptions{Dir: dir})
	candidates := completeWithOptions(t, []string{"shadowtree", "build", ""}, recipes, Options{Dir: dir})
	if !hasCandidate(candidates, "./cmd/api") || hasCandidate(candidates, "./internal/lib") {
		t.Fatalf("build candidates = %#v, want main package positional value", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "vet", ""}, recipes, Options{Dir: dir})
	if !hasCandidate(candidates, "./internal/lib") || hasCandidate(candidates, "pkg=") {
		t.Fatalf("vet candidates = %#v, want package positional value", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "fmt", ""}, recipes, Options{Dir: dir})
	if !hasCandidate(candidates, "./internal/lib") || !hasCandidate(candidates, "root.go") {
		t.Fatalf("fmt candidates = %#v, want package and file positional values", candidates)
	}
}

func TestCandidatesCompleteGoFmtTargetValuesCommand(t *testing.T) {
	dir := t.TempDir()
	writeTextFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	writeTextFile(t, filepath.Join(dir, "main.go"), "package main\n")
	mkdirAll(t, filepath.Join(dir, "internal", "lib"))
	writeTextFile(t, filepath.Join(dir, "internal", "lib", "lib.go"), "package lib\n")

	recipes := map[string]recipe.Recipe{
		"fmt": {
			Cmd: recipe.Command{"go", "fmt", "{target}"},
			Arguments: map[string]recipe.Argument{
				"target": {
					Type:   "rel_path",
					Values: recipe.ScriptCommand(recipe.GoFmtTargetValuesCommand),
				},
			},
		},
	}
	candidates := completeWithOptions(t, []string{"shadowtree", "fmt", "target=./internal/"}, recipes, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0] != (Candidate{Value: "target=./internal/lib", Help: "example.com/root/internal/lib"}) {
		t.Fatalf("candidates = %#v, want internal/lib package target", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "fmt", "target=m"}, recipes, Options{Dir: dir})
	if len(candidates) != 1 || candidates[0] != (Candidate{Value: "target=main.go", Help: "file"}) {
		t.Fatalf("candidates = %#v, want main.go file target", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "fmt", "target=./"}, recipes, Options{Dir: dir})
	if hasCandidate(candidates, "target=./...") {
		t.Fatalf("candidates = %#v, want no synthetic ./... target", candidates)
	}
}

func TestCandidatesCompleteGoFmtTargetValuesCommandWithoutModule(t *testing.T) {
	dir := t.TempDir()
	writeTextFile(t, filepath.Join(dir, "main.go"), "package main\n")

	recipes := map[string]recipe.Recipe{
		"fmt": {
			Cmd: recipe.Command{"go", "fmt", "{target}"},
			Arguments: map[string]recipe.Argument{
				"target": {
					Type:   "rel_path",
					Values: recipe.ScriptCommand(recipe.GoFmtTargetValuesCommand),
				},
			},
		},
	}
	candidates := completeWithOptions(t, []string{"shadowtree", "fmt", "target=m"}, recipes, Options{Dir: dir})
	if len(candidates) != 1 || candidates[0] != (Candidate{Value: "target=main.go", Help: "file"}) {
		t.Fatalf("candidates = %#v, want main.go file target", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "fmt", "target=./"}, recipes, Options{Dir: dir})
	if hasCandidate(candidates, "target=./...") {
		t.Fatalf("candidates = %#v, want no synthetic ./... target", candidates)
	}
}

func TestCandidatesCompleteRecipeBuiltinArgumentValues(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "run", "target=b"}, map[string]recipe.Recipe{
		"build": {Help: "Build binary.", Cmd: recipe.Command{"go", "build"}},
		"run": {
			Cmd: recipe.Command{"shadowtree"},
			Arguments: map[string]recipe.Argument{
				"target": {
					Type:   "string",
					Values: recipe.ScriptCommand("@recipes"),
				},
			},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "target=build" || candidates[0].Help != "Build binary." {
		t.Fatalf("candidates = %#v, want build recipe value", candidates)
	}
}

func TestCandidatesCompleteVarsBuiltinArgumentValues(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build", "target=m"}, map[string]recipe.Recipe{
		"build": {
			Cmd:  recipe.Command{"go", "build"},
			Vars: map[string]string{"project": "api"},
			Arguments: map[string]recipe.Argument{
				"mode": {Help: "Build mode.", Type: "string"},
				"target": {
					Type:   "string",
					Values: recipe.ScriptCommand("@vars"),
				},
			},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "target=mode" || candidates[0].Help != "Build mode." {
		t.Fatalf("candidates = %#v, want mode argument value", candidates)
	}
}

func TestCandidatesCompleteLinesBuiltinArgumentValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "targets.txt"), []byte("api\tAPI service\nworker\tWorker service\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidates := completeWithOptions(t, []string{"shadowtree", "build", "project=a"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand("@lines targets.txt"),
				},
			},
		},
	}, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0].Value != "project=api" || candidates[0].Help != "API service" {
		t.Fatalf("candidates = %#v, want api line value", candidates)
	}
}

func TestCandidatesCompleteGlobBuiltinArgumentValues(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "cmd", "api"))
	writeFile(t, filepath.Join(dir, "cmd", "worker"))
	candidates := completeWithOptions(t, []string{"shadowtree", "build", "project=cmd/a"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand(`@glob "cmd/*"`),
				},
			},
		},
	}, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0].Value != "project=cmd/api/" || candidates[0].Help != "directory" {
		t.Fatalf("candidates = %#v, want cmd/api directory value", candidates)
	}
}

func TestCandidatesCompleteDynamicArgumentValuesWithRecipeEnv(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build", "project=api"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Env: map[string]string{"PROJECTS": "api\tAPI service\nweb\tWeb service\n"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand("printf '%s' \"$PROJECTS\""),
				},
			},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "project=api" || candidates[0].Help != "API service" {
		t.Fatalf("candidates = %#v, want api value from recipe env", candidates)
	}
}

func TestCandidatesCompleteDynamicArgumentValuesFromRecipeReference(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build", "project=api"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand("@projects"),
				},
			},
		},
		"projects": {
			Cmd: recipe.Command{"printf", "api\tAPI service\nweb\tWeb service\n"},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "project=api" || candidates[0].Help != "API service" {
		t.Fatalf("candidates = %#v, want api value from recipe reference", candidates)
	}
}

func TestCandidatesCompleteSpacedDynamicArgumentValuesContainingSlash(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build", "project=sip/"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand("printf 'sip/scheduler\\tScheduler daemon\\nsip/snmptrap\\tSNMP trap daemon\\n'"),
				},
			},
		},
	})

	if len(candidates) != 2 || candidates[0].Value != "project=sip/scheduler" || candidates[1].Value != "project=sip/snmptrap" {
		t.Fatalf("candidates = %#v, want slash-containing project values", candidates)
	}
}

func TestCandidatesCompleteNamedPathArgumentValues(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "cmd", "shadowtree"))
	mkdirAll(t, filepath.Join(dir, "cmd", "shadowtree-lsp"))
	writeFile(t, filepath.Join(dir, "README.md"))

	recipes := map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "path"},
			},
		},
	}
	candidates := completeWithOptions(t, []string{"shadowtree", "open", "file=cmd"}, recipes, Options{Dir: dir})
	if len(candidates) != 1 || candidates[0].Value != "file=cmd/" || candidates[0].Help != "directory" {
		t.Fatalf("candidates = %#v, want cmd directory", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "open", "file=cmd/"}, recipes, Options{Dir: dir})
	if !hasCandidate(candidates, "file=cmd/shadowtree/") || !hasCandidate(candidates, "file=cmd/shadowtree-lsp/") {
		t.Fatalf("candidates = %#v, want cmd children", candidates)
	}

	absolutePrefix := "file=" + filepath.Join(dir, "c")
	candidates = completeWithOptions(t, []string{"shadowtree", "open", absolutePrefix}, recipes, Options{Dir: t.TempDir()})
	if len(candidates) != 1 || candidates[0].Value != "file="+filepath.Join(dir, "cmd")+"/" {
		t.Fatalf("candidates = %#v, want absolute cmd directory", candidates)
	}
}

func TestCandidatesCompletePositionalPathArgumentValues(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "cmd", "shadowtree"))
	mkdirAll(t, filepath.Join(dir, "cmd", "shadowtree-lsp"))

	candidates := completeWithOptions(t, []string{"shadowtree", "open", "cmd"}, map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "rel_path", Position: 1},
			},
		},
	}, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0].Value != "cmd/" {
		t.Fatalf("candidates = %#v, want cmd/", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "open", "cmd/"}, map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "rel_path", Position: 1},
			},
		},
	}, Options{Dir: dir})
	if !hasCandidate(candidates, "cmd/shadowtree/") || !hasCandidate(candidates, "cmd/shadowtree-lsp/") {
		t.Fatalf("candidates = %#v, want cmd children", candidates)
	}
}

func TestCandidatesCompletePathHomeButNotRelativePathHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mkdirAll(t, filepath.Join(home, "projects"))

	pathRecipes := map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "path"},
			},
		},
	}
	candidates := completeWithOptions(t, []string{"shadowtree", "open", "file=~/"}, pathRecipes, Options{Dir: t.TempDir()})
	if len(candidates) != 1 || candidates[0].Value != "file=~/projects/" {
		t.Fatalf("candidates = %#v, want home child", candidates)
	}

	relPathRecipes := map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "rel_path"},
			},
		},
	}
	candidates = completeWithOptions(t, []string{"shadowtree", "open", "file=~/"}, relPathRecipes, Options{Dir: t.TempDir()})
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want no rel_path home completion", candidates)
	}
}

func TestCandidatesFilterPathArgumentValuesByKind(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "bin"))
	writeFile(t, filepath.Join(dir, "bin", "tool"))
	if err := os.Chmod(filepath.Join(dir, "bin", "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "bin", "notes.txt"))
	mkdirAll(t, filepath.Join(dir, "bin", "nested"))

	candidates := completeWithOptions(t, []string{"shadowtree", "run-bin", "bin/"}, map[string]recipe.Recipe{
		"run-bin": {
			Cmd: recipe.Command{"sh", "-c"},
			Arguments: map[string]recipe.Argument{
				"bin": {Type: "rel_path", PathKind: "executable", Position: 1},
			},
		},
	}, Options{Dir: dir})
	if !hasCandidate(candidates, "bin/tool") || !hasCandidate(candidates, "bin/nested/") {
		t.Fatalf("candidates = %#v, want executable and traversal directory", candidates)
	}
	if hasCandidate(candidates, "bin/notes.txt") {
		t.Fatalf("candidates = %#v, want non-executable file filtered out", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "cd", "bin/"}, map[string]recipe.Recipe{
		"cd": {
			Cmd: recipe.Command{"cd"},
			Arguments: map[string]recipe.Argument{
				"dir": {Type: "rel_path", PathKind: "dir", Position: 1},
			},
		},
	}, Options{Dir: dir})
	if len(candidates) != 1 || candidates[0].Value != "bin/nested/" {
		t.Fatalf("candidates = %#v, want only nested directory", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "open", "bin/"}, map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "rel_path", PathKind: "file", Position: 1},
			},
		},
	}, Options{Dir: dir})
	if !hasCandidate(candidates, "bin/tool") || !hasCandidate(candidates, "bin/notes.txt") || !hasCandidate(candidates, "bin/nested/") {
		t.Fatalf("candidates = %#v, want files and traversal directory", candidates)
	}
}

func TestCandidatesRejectUnsupportedShell(t *testing.T) {
	_, err := Candidates(t.Context(), "tcsh", []string{"shadowtree", ""}, nil, Options{})
	if err == nil {
		t.Fatal("Candidates succeeded for unsupported shell")
	}
}

func TestCandidatesCompleteBashFlags(t *testing.T) {
	candidates, err := Candidates(t.Context(), "bash", []string{"shadowtree", "--p"}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if !hasCandidate(candidates, "--profile") || !hasCandidate(candidates, "--print") {
		t.Fatalf("candidates = %#v, want --profile and --print", candidates)
	}
	if hasCandidate(candidates, "--config") {
		t.Fatalf("candidates = %#v, want prefix-filtered flags", candidates)
	}
}

func TestStaticCandidatesCompleteFlags(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			candidates, ok, err := StaticCandidates(shell, []string{"shadowtree", "--p"})
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatal("StaticCandidates returned ok=false")
			}

			if !hasCandidate(candidates, "--profile") || !hasCandidate(candidates, "--print") {
				t.Fatalf("candidates = %#v, want --profile and --print", candidates)
			}
			if hasCandidate(candidates, "--config") {
				t.Fatalf("candidates = %#v, want prefix-filtered flags", candidates)
			}
		})
	}
}

func TestStaticCandidatesSkipFlagsAfterCommand(t *testing.T) {
	candidates, ok, err := StaticCandidates("bash", []string{"shadowtree", "build", "--p"})
	if err != nil {
		t.Fatal(err)
	}
	if ok || len(candidates) != 0 {
		t.Fatalf("candidates = %#v, ok = %t, want no static candidates", candidates, ok)
	}
}

func TestCandidatesDoNotCompleteParenthesizedArguments(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build(proj"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {Help: "Go package to build.", Type: "string", Position: 1},
			},
		},
	})

	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want no parenthesized completion", candidates)
	}
}

func TestBashScriptCallsInternalCompletion(t *testing.T) {
	var out bytes.Buffer
	if err := Script(&out, "bash"); err != nil {
		t.Fatal(err)
	}

	script := out.String()
	if !strings.Contains(script, `"$command_name" __complete bash "$COMP_POINT" "$COMP_LINE" "$2"`) {
		t.Fatalf("bash script = %q, want internal bash completion callback", script)
	}
	if !strings.Contains(script, "complete -F _shadowtree_complete") {
		t.Fatalf("bash script = %q, want complete function registration", script)
	}
	if !strings.Contains(script, "compopt -o nospace 2>/dev/null || true; break") {
		t.Fatalf("bash script = %q, want one compopt call per completion attempt", script)
	}
}

func TestZshScriptCallsInternalCompletion(t *testing.T) {
	var out bytes.Buffer
	if err := Script(&out, "zsh"); err != nil {
		t.Fatal(err)
	}

	script := out.String()
	if !strings.Contains(script, `"$command_name" __complete zsh "${words[@]}"`) {
		t.Fatalf("zsh script = %q, want internal zsh completion callback", script)
	}
	if !strings.Contains(script, "compdef _shadowtree shadowtree") {
		t.Fatalf("zsh script = %q, want compdef function registration", script)
	}
	if !strings.Contains(script, "autoload -Uz compinit") {
		t.Fatalf("zsh script = %q, want compinit fallback before compdef", script)
	}
	if !strings.Contains(script, `_describe -t commands 'shadowtree candidate' space_records`) {
		t.Fatalf("zsh script = %q, want zsh description-aware completion", script)
	}
	if !strings.Contains(script, `record=${record//:/\\:}`) {
		t.Fatalf("zsh script = %q, want escaped colons in candidate values", script)
	}
	if !strings.Contains(script, "_describe -t commands 'shadowtree candidate' nospace_records -S ''") {
		t.Fatalf("zsh script = %q, want no-space completions for argument names and directories", script)
	}
}

func TestFishScriptGuardsStaticGlobalCompletions(t *testing.T) {
	var out bytes.Buffer
	if err := Script(&out, "fish"); err != nil {
		t.Fatal(err)
	}

	script := out.String()
	if !strings.Contains(script, "function __shadowtree_global_options") {
		t.Fatalf("fish script = %q, want global option guard function", script)
	}
	var valueFlags []string
	for _, spec := range globalflag.All() {
		if spec.TakesValue() {
			valueFlags = append(valueFlags, "--"+spec.Name)
		}
	}
	if !strings.Contains(script, "case "+strings.Join(valueFlags, " ")) {
		t.Fatalf("fish script = %q, want one value-taking flag case", script)
	}
	if !strings.Contains(script, "set skip_next 1") {
		t.Fatalf("fish script = %q, want value-taking flag boundary handling", script)
	}
	if !strings.Contains(script, "case '*'") || !strings.Contains(script, "return 1") {
		t.Fatalf("fish script = %q, want positional boundary handling", script)
	}
	if !strings.Contains(script, "complete -c shadowtree -n __shadowtree_global_options -l profile") {
		t.Fatalf("fish script = %q, want guarded global option registration", script)
	}
}

func TestFishScriptCompletesGlobalsAfterGlobalFlagValues(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish not installed")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "shadowtree.fish")
	var script bytes.Buffer
	if err := Script(&script, "fish"); err != nil {
		t.Fatal(err)
	}
	writeTextFile(t, scriptPath, script.String())
	binDir := filepath.Join(dir, "bin")
	mkdirAll(t, binDir)
	shadowtreePath := filepath.Join(binDir, "shadowtree")
	writeTextFile(t, shadowtreePath, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(shadowtreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, line := range []string{
		"shadowtree --config .shadowtree.toml --p",
		"shadowtree --profile go --p",
		"shadowtree --sync-out dist --p",
	} {
		t.Run(line, func(t *testing.T) {
			cmd := exec.Command(fish, "--no-config", "-c", `source "$SCRIPT_PATH"; complete -C "$COMPLETE_LINE"`)
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SCRIPT_PATH="+scriptPath,
				"COMPLETE_LINE="+line,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("fish complete failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "--profile") || !strings.Contains(string(out), "--print") {
				t.Fatalf("fish complete output = %q, want profile and print globals", out)
			}
		})
	}
}

func TestBashWordsPreserveCompletionShape(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		point int
		want  []string
	}{
		{
			name: "trailing space",
			line: "shadowtree build ",
			want: []string{"shadowtree", "build", ""},
		},
		{
			name: "equals",
			line: "shadowtree build race=",
			want: []string{"shadowtree", "build", "race="},
		},
		{
			name: "escaped space",
			line: `shadowtree open file=My\ Project`,
			want: []string{"shadowtree", "open", "file=My Project"},
		},
		{
			name:  "cursor point",
			line:  "shadowtree te ignored",
			point: len("shadowtree te"),
			want:  []string{"shadowtree", "te"},
		},
		{
			name: "open quote",
			line: `shadowtree open file="My Project`,
			want: []string{"shadowtree", "open", `file="My Project`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			point := tt.point
			if point == 0 {
				point = len(tt.line)
			}
			if got := BashWords(tt.line, point); !slices.Equal(got, tt.want) {
				t.Fatalf("BashWords(%q, %d) = %#v, want %#v", tt.line, point, got, tt.want)
			}
		})
	}
}

func TestBashReplacementCandidatesTrimValuePrefix(t *testing.T) {
	candidates := BashReplacementCandidates(
		[]string{"shadowtree", "test", "race=f"},
		"f",
		[]Candidate{{Value: "race=false", Help: "bool"}},
	)
	if len(candidates) != 1 || candidates[0].Value != "false" {
		t.Fatalf("candidates = %#v, want false replacement", candidates)
	}
}

func TestBashReplacementCandidatesTrimGroupedValuePrefix(t *testing.T) {
	candidates := BashReplacementCandidates(
		[]string{"shadowtree", "build[project=s"},
		"s",
		[]Candidate{{Value: "build[project=sip/scheduler", Help: "Scheduler"}},
	)
	if len(candidates) != 1 || candidates[0].Value != "sip/scheduler" {
		t.Fatalf("candidates = %#v, want sip/scheduler replacement", candidates)
	}
}

func TestBashReplacementCandidatesKeepArgumentNames(t *testing.T) {
	candidates := BashReplacementCandidates(
		[]string{"shadowtree", "test", "ra"},
		"ra",
		[]Candidate{{Value: "race=", Help: "bool"}},
	)
	if len(candidates) != 1 || candidates[0].Value != "race=" {
		t.Fatalf("candidates = %#v, want full argument name replacement", candidates)
	}
}

func TestFishCandidatesSanitizeNewlines(t *testing.T) {
	var out bytes.Buffer
	err := WriteCandidates(&out, "fish", []Candidate{{
		Value: "install",
		Help:  "sh -c set -eu\ninstall -d bin",
	}})
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("line count = %d output=%q", len(lines), out.String())
	}
	if strings.Contains(out.String(), "set -eu\ninstall") {
		t.Fatalf("output contains raw newline: %q", out.String())
	}
}

func TestTabCandidatesPreserveValueWhitespace(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			var out bytes.Buffer
			err := WriteCandidates(&out, shell, []Candidate{{
				Value: "file=My Project/",
				Help:  "directory\nchild",
			}})
			if err != nil {
				t.Fatal(err)
			}

			want := "file=My Project/\tdirectory child\n"
			if out.String() != want {
				t.Fatalf("output = %q, want %q", out.String(), want)
			}
		})
	}
}

func TestFishPathCandidatesEscapeValueWhitespace(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "My Project"))

	candidates := completeWithOptions(t, []string{"shadowtree", "open", "file=My"}, map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "path"},
			},
		},
	}, Options{Dir: dir})

	var out bytes.Buffer
	if err := WriteCandidates(&out, "fish", candidates); err != nil {
		t.Fatal(err)
	}

	want := "file=My\\ Project/\tdirectory\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func complete(t *testing.T, words []string, recipes map[string]recipe.Recipe) []Candidate {
	t.Helper()
	return completeWithOptions(t, words, recipes, Options{})
}

func completeWithOptions(t *testing.T, words []string, recipes map[string]recipe.Recipe, opts Options) []Candidate {
	t.Helper()
	candidates, err := Candidates(t.Context(), "fish", words, recipes, opts)
	if err != nil {
		t.Fatal(err)
	}
	return candidates
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTextFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasCandidate(candidates []Candidate, value string) bool {
	for _, candidate := range candidates {
		if candidate.Value == value {
			return true
		}
	}
	return false
}

func helpFor(candidates []Candidate, value string) string {
	for _, candidate := range candidates {
		if candidate.Value == value {
			return candidate.Help
		}
	}
	return ""
}
