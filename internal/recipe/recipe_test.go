package recipe

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func applyGlobals(t *testing.T, recipes map[string]Recipe, vars map[string]string, shell, shellPrelude string) map[string]Recipe {
	t.Helper()
	out, err := ApplyGlobalsExpanded(recipes, vars, nil, shell, shellPrelude)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func stageCommands(commands ...Command) StageCommands {
	out := make(StageCommands, 0, len(commands))
	for _, command := range commands {
		out = append(out, StageCommand{Cmd: command})
	}
	return out
}

func TestEnumSetValues(t *testing.T) {
	enumSets := map[string]Command{
		"service": ScriptCommand("@enum api='API service' worker='Background worker'"),
	}
	values, ok, err := BuiltinValues(ScriptCommand("@enum_set service"), ValueBuiltinOptions{EnumSets: enumSets})
	if err != nil || !ok {
		t.Fatalf("BuiltinValues() = %v, %v, want values", err, ok)
	}
	if got := []string{values[0].Value, values[1].Value}; !slices.Equal(got, []string{"api", "worker"}) {
		t.Fatalf("values = %v", got)
	}
	prefixedValues, ok, err := BuiltinValues(ScriptCommand("@enum_set service"), ValueBuiltinOptions{EnumSets: enumSets, ValuePrefix: "wo"})
	if err != nil || !ok {
		t.Fatalf("BuiltinValues() with prefix = %v, %v, want values", err, ok)
	}
	if got := []string{prefixedValues[0].Value}; !slices.Equal(got, []string{"worker"}) {
		t.Fatalf("prefixed values = %v", got)
	}

	cfg := Config{EnumSets: enumSets, Recipes: map[string]Recipe{
		"deploy": {
			Arguments: map[string]Argument{"target": {Values: ScriptCommand("@enum_set service")}},
			ForEach:   ScriptCommand("@enum_set service"),
		},
	}}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEnumSetReferences(cfg); err != nil {
		t.Fatal(err)
	}
	if err := ValidateArgumentAcceptedValue("target", cfg.Recipes["deploy"].Arguments["target"], "web", ValueBuiltinOptions{EnumSets: enumSets}); err == nil {
		t.Fatal("ValidateArgumentAcceptedValue() accepted invalid enum set value")
	}
}

func TestEnumSetValidation(t *testing.T) {
	for _, command := range []Command{ScriptCommand("@lines values.txt"), ScriptCommand("@enum")} {
		if err := ValidateConfig(Config{EnumSets: map[string]Command{"service": command}}); err == nil {
			t.Fatalf("ValidateConfig(%q) succeeded", ScriptBody(command))
		}
	}
	cfg := Config{Recipes: map[string]Recipe{
		"deploy": {Arguments: map[string]Argument{"target": {Values: ScriptCommand("@enum_set missing")}}},
	}}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("ValidateConfig() = %v, want unresolved reference accepted", err)
	}
	if err := ValidateEnumSetReferences(cfg); err == nil {
		t.Fatal("ValidateEnumSetReferences() accepted unknown enum set")
	}
}

func TestMergeRecipeOverridesOnlySpecifiedFields(t *testing.T) {
	base := Recipe{
		Cmd:       Command{"go", "test", "{pkg}", "{@}"},
		Sandboxed: new(SandboxModeWorkspace),
		Log:       "base.log",
		LogStages: []string{LogStageCmd},
		LogTee:    new(false),
	}
	override := Recipe{
		Pre:       stageCommands(Command{"go", "generate", "./..."}),
		Sandboxed: new(SandboxModeHost),
		Log:       "override.log",
		LogStages: []string{LogStagePre, LogStagePost},
	}

	got := MergeRecipe(base, override)
	if !slices.Equal(got.Cmd, Command{"go", "test", "{pkg}", "{@}"}) {
		t.Fatalf("Cmd = %#v", got.Cmd)
	}
	if len(got.Pre) != 1 || !slices.Equal(got.Pre[0].Cmd, Command{"go", "generate", "./..."}) {
		t.Fatalf("Pre = %#v", got.Pre)
	}
	if got.Sandboxed == nil || *got.Sandboxed != SandboxModeHost {
		t.Fatalf("Sandboxed = %#v, want false", got.Sandboxed)
	}
	if got.Log != "override.log" {
		t.Fatalf("Log = %q", got.Log)
	}
	if !slices.Equal(got.LogStages, []string{LogStagePre, LogStagePost}) {
		t.Fatalf("LogStages = %#v", got.LogStages)
	}
	if got.LogTee == nil || *got.LogTee {
		t.Fatalf("LogTee = %#v, want false inherited from base", got.LogTee)
	}
}

func TestMergeRecipeReplacesRequirements(t *testing.T) {
	base := Recipe{
		Cmd: Command{"go", "test"},
		Requires: Requirements{
			Commands:         []string{"go"},
			OptionalCommands: []string{"h2load"},
		},
	}
	override := Recipe{
		Requires: Requirements{
			Commands:   []string{"docker"},
			GoCommands: map[string]string{"stringer": "golang.org/x/tools/cmd/stringer@latest"},
		},
	}

	got := MergeRecipe(base, override)

	if !slices.Equal(got.Requires.Commands, []string{"docker"}) {
		t.Fatalf("commands = %#v", got.Requires.Commands)
	}
	if len(got.Requires.OptionalCommands) != 0 {
		t.Fatalf("optional_commands = %#v", got.Requires.OptionalCommands)
	}
	if got.Requires.GoCommands["stringer"] != "golang.org/x/tools/cmd/stringer@latest" {
		t.Fatalf("go_commands = %#v", got.Requires.GoCommands)
	}
}

func TestValidateConfigAcceptsRequirements(t *testing.T) {
	cfg := Config{Recipes: map[string]Recipe{
		"benchmark": {
			Cmd: Command{"go", "test"},
			Requires: Requirements{
				Commands:         []string{"docker", "openssl", "go"},
				OptionalCommands: []string{"h2load"},
				GoCommands:       map[string]string{"stringer": "golang.org/x/tools/cmd/stringer@latest"},
				NodeCommands: map[string]string{
					"eslint":                "eslint@^9",
					"openapi-generator-cli": "@openapitools/openapi-generator-cli@latest",
					"playwright":            "@playwright/test@latest",
				},
			},
		},
	}}

	if err := ValidateConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidateConfigAcceptsVarsArgumentValuesDefaultsAndPresets(t *testing.T) {
	cfg := Config{Recipes: map[string]Recipe{
		"render": {
			Cmd:  Command{"render", "{name}"},
			Vars: map[string]string{"component": "api"},
			Arguments: map[string]Argument{
				"name": {Default: "component", Values: ScriptCommand("@vars")},
			},
			Presets: map[string]RecipePreset{
				"stable": {Arguments: map[string]any{"name": "component"}},
			},
		},
	}}

	if err := ValidateConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidateConfigRejectsInvalidRequirements(t *testing.T) {
	tests := []struct {
		name string
		req  Requirements
		want string
	}{
		{
			name: "empty required",
			req:  Requirements{Commands: []string{""}},
			want: "commands[0] must be a non-empty executable name without surrounding whitespace",
		},
		{
			name: "trimmed optional",
			req:  Requirements{OptionalCommands: []string{" h2load"}},
			want: "optional_commands[0] must be a non-empty executable name without surrounding whitespace",
		},
		{
			name: "path required",
			req:  Requirements{Commands: []string{"./tools/docker"}},
			want: "commands[0] must be an executable name, not a path",
		},
		{
			name: "path optional",
			req:  Requirements{OptionalCommands: []string{`tools\h2load`}},
			want: "optional_commands[0] must be an executable name, not a path",
		},
		{
			name: "duplicate required",
			req:  Requirements{Commands: []string{"docker", "docker"}},
			want: `commands[1] duplicates commands[0] "docker"`,
		},
		{
			name: "required optional overlap",
			req: Requirements{
				Commands:         []string{"docker"},
				OptionalCommands: []string{"docker"},
			},
			want: `optional_commands overlaps required tool "docker" from commands`,
		},
		{
			name: "path go command key",
			req:  Requirements{GoCommands: map[string]string{"tools/stringer": "example.com/tool@latest"}},
			want: "go_commands must be an executable name, not a path",
		},
		{
			name: "reserved node command key",
			req:  Requirements{NodeCommands: map[string]string{RunIDPlaceholder: "eslint@^9"}},
			want: `node_commands key "run_id" is reserved`,
		},
		{
			name: "trimmed node command key",
			req:  Requirements{NodeCommands: map[string]string{" eslint": "eslint@^9"}},
			want: "node_commands must be a non-empty executable name without surrounding whitespace",
		},
		{
			name: "empty go package",
			req:  Requirements{GoCommands: map[string]string{"stringer": ""}},
			want: "go_commands.stringer must be a non-empty package string without surrounding whitespace",
		},
		{
			name: "trimmed node package",
			req:  Requirements{NodeCommands: map[string]string{"eslint": " eslint@^9"}},
			want: "node_commands.eslint must be a non-empty package string without surrounding whitespace",
		},
		{
			name: "duplicate across required kinds",
			req: Requirements{
				Commands:   []string{"stringer"},
				GoCommands: map[string]string{"stringer": "golang.org/x/tools/cmd/stringer@latest"},
			},
			want: `required tool "stringer" declared in both commands and go_commands`,
		},
		{
			name: "duplicate go and node",
			req: Requirements{
				GoCommands:   map[string]string{"eslint": "example.com/eslint@latest"},
				NodeCommands: map[string]string{"eslint": "eslint@^9"},
			},
			want: `required tool "eslint" declared in both go_commands and node_commands`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Recipes: map[string]Recipe{"test": {Cmd: Command{"true"}, Requires: tt.req}}}
			err := ValidateConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateConfig error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestResolveExpandsRunIDEverywhere(t *testing.T) {
	recipes := applyGlobals(t, map[string]Recipe{
		"test": {
			Cmd:          ScriptCommand("printf '%s' '{marker} {local} {run_id}'"),
			Pre:          stageCommands(ScriptCommand("printf '%s' '{run_id}'")),
			Post:         stageCommands(ScriptCommand("printf '%s' '{run_id}'")),
			ForEach:      ScriptCommand("@enum {run_id}"),
			ShellPrelude: "RUN_ID={run_id}",
			Workdir:      "work/{run_id}/{item}",
			SyncOut:      []string{"out/{run_id}"},
			Log:          "logs/{run_id}.log",
			Vars: map[string]string{
				"local": "recipe-{run_id}",
			},
			Env: map[string]string{
				"RUN_ID": "{run_id}",
			},
		},
	}, map[string]string{
		"marker": "global-{run_id}",
	}, "", "")

	got, err := ResolveWithOptions("test", recipes["test"], nil, nil, map[string]string{
		"GLOBAL_RUN_ID": "{run_id}",
	}, "", "", ResolveOptions{RunID: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("RunID = %q", got.RunID)
	}
	for _, value := range []string{
		ScriptBody(got.Main),
		ScriptBody(got.Recipe.Pre[0].Cmd),
		ScriptBody(got.Recipe.Post[0].Cmd),
		ScriptBody(got.Recipe.ForEach),
		got.Recipe.ShellPrelude,
		got.Recipe.Workdir,
		got.SyncOut[0],
		got.LogPath,
		got.Recipe.Env["RUN_ID"],
		got.GlobalEnv["GLOBAL_RUN_ID"],
	} {
		if strings.Contains(value, "{run_id}") || !strings.Contains(value, got.RunID) {
			t.Fatalf("value %q did not expand run_id %q", value, got.RunID)
		}
	}
}

func TestResolveUntypedRecipeRejectsUnexpectedCLIArgsWithoutVariadicPlaceholder(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"go", "test"},
	}

	_, err := Resolve("test", rec, []string{"./internal/..."}, nil, nil, "", GoProfile)
	if err == nil || !strings.Contains(err.Error(), `unexpected positional argument "./internal/..."`) {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestRecipeReferenceSplitsNameAndArgs(t *testing.T) {
	ref, ok := ParseRecipeReference(Command{"@gen-swagger", "service=api"})
	if !ok {
		t.Fatal("RecipeReference did not detect @ command")
	}
	if ref.Name != "gen-swagger" {
		t.Fatalf("name = %q", ref.Name)
	}
	if !slices.Equal(ref.Args, []string{"service=api"}) {
		t.Fatalf("args = %#v", ref.Args)
	}
}

func TestRecipeReferenceSplitsBracketStyleArguments(t *testing.T) {
	ref, ok := ParseRecipeReference(Command{"@build[component=godoxy, mode=dev]"})
	if !ok {
		t.Fatal("RecipeReference did not detect @ command")
	}
	if ref.Name != "build" {
		t.Fatalf("name = %q", ref.Name)
	}
	if !slices.Equal(ref.Args, []string{"component=godoxy", "mode=dev"}) {
		t.Fatalf("args = %#v", ref.Args)
	}
}

func TestRecipeReferenceSplitsCrossConfigTarget(t *testing.T) {
	ref, ok := ParseRecipeReference(Command{"@webui:gen-schema[mode=dev]", "force=true"})
	if !ok {
		t.Fatal("ParseRecipeReference did not detect @ command")
	}
	if ref.Path != "webui" {
		t.Fatalf("Path = %q", ref.Path)
	}
	if ref.Name != "gen-schema" {
		t.Fatalf("Name = %q", ref.Name)
	}
	if !slices.Equal(ref.Args, []string{"mode=dev", "force=true"}) {
		t.Fatalf("Args = %#v", ref.Args)
	}
}

func TestBuiltinValuesParsesEnumScriptQuotedValues(t *testing.T) {
	values, ok, err := BuiltinValues(ScriptCommand(`@enum a b "c d" e f`), ValueBuiltinOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @enum")
	}
	if !slices.Equal(values, []ValueCandidate{
		{Value: "a"},
		{Value: "b"},
		{Value: "c d"},
		{Value: "e"},
		{Value: "f"},
	}) {
		t.Fatalf("values = %#v", values)
	}
}

func TestBuiltinValuesParsesEnumValueHelp(t *testing.T) {
	values, ok, err := BuiltinValues(ScriptCommand("@enum all='all modules' api='API service'"), ValueBuiltinOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @enum")
	}
	want := []ValueCandidate{
		{Value: "all", Help: "all modules"},
		{Value: "api", Help: "API service"},
	}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestBuiltinValuesKeepsEnumLiteralEqualsValues(t *testing.T) {
	values, ok, err := BuiltinValues(ScriptCommand("@enum GOOS=linux GOOS=darwin"), ValueBuiltinOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @enum")
	}
	want := []ValueCandidate{
		{Value: "GOOS=linux"},
		{Value: "GOOS=darwin"},
	}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestBuiltinValuesConcatenatesScriptBuiltins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	mkdirAll(t, filepath.Join(dir, "services", "api"))
	writeFile(t, filepath.Join(dir, "services", "api", "go.mod"), "module example.com/api\n")

	values, ok, err := BuiltinValues(
		ScriptCommand("@go-modules; @enum all='all modules'"),
		ValueBuiltinOptions{Context: t.Context(), Dir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect script builtins")
	}
	want := []ValueCandidate{
		{Value: ".", Help: "example.com/root"},
		{Value: "services/api", Help: "example.com/api"},
		{Value: "all", Help: "all modules"},
	}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestBuiltinValuesReadsLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "targets.txt"), []byte("api\tAPI service\nworker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	values, ok, err := BuiltinValues(ScriptCommand("@lines targets.txt"), ValueBuiltinOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @lines")
	}
	if len(values) != 2 || values[0] != (ValueCandidate{Value: "api", Help: "API service"}) || values[1] != (ValueCandidate{Value: "worker"}) {
		t.Fatalf("values = %#v", values)
	}
}

func TestBuiltinValuesReadsLinesWithPrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "targets.txt"), []byte("api\tAPI service\nworker\tWorker service\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	values, ok, err := BuiltinValues(ScriptCommand("@lines targets.txt"), ValueBuiltinOptions{Dir: dir, ValuePrefix: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @lines")
	}
	if len(values) != 1 || values[0] != (ValueCandidate{Value: "worker", Help: "Worker service"}) {
		t.Fatalf("values = %#v", values)
	}
}

func TestBuiltinValuesGlobsFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmd", "worker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	values, ok, err := BuiltinValues(ScriptCommand(`@glob "cmd/*"`), ValueBuiltinOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @glob")
	}
	if len(values) != 2 ||
		values[0] != (ValueCandidate{Value: "cmd/api/", Help: "directory"}) ||
		values[1] != (ValueCandidate{Value: "cmd/worker", Help: "file"}) {
		t.Fatalf("values = %#v", values)
	}
}

func TestBuiltinValuesGlobsDirectoriesWithSlashPrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "api"), 0o755); err != nil {
		t.Fatal(err)
	}

	values, ok, err := BuiltinValues(ScriptCommand(`@glob "cmd/*"`), ValueBuiltinOptions{Dir: dir, ValuePrefix: "cmd/api/"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @glob")
	}
	if len(values) != 1 || values[0] != (ValueCandidate{Value: "cmd/api/", Help: "directory"}) {
		t.Fatalf("values = %#v", values)
	}
}

func TestBuiltinValuesDiscoversGoModules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	mkdirAll(t, filepath.Join(dir, "services", "api"))
	writeFile(t, filepath.Join(dir, "services", "api", "go.mod"), "module example.com/api\n")
	mkdirAll(t, filepath.Join(dir, "vendor", "ignored"))
	writeFile(t, filepath.Join(dir, "vendor", "ignored", "go.mod"), "module example.com/ignored\n")

	values, ok, err := BuiltinValues(
		ScriptCommand("@go-modules"),
		ValueBuiltinOptions{Context: t.Context(), Dir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @go-modules")
	}
	want := []ValueCandidate{
		{Value: ".", Help: "example.com/root"},
		{Value: "services/api", Help: "example.com/api"},
	}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestBuiltinValuesDiscoversGoModulesWithPrefix(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	mkdirAll(t, filepath.Join(dir, "services", "api"))
	writeFile(t, filepath.Join(dir, "services", "api", "go.mod"), "module example.com/api\n")

	values, ok, err := BuiltinValues(
		ScriptCommand("@go-modules"),
		ValueBuiltinOptions{Context: t.Context(), Dir: dir, ValuePrefix: "services/"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @go-modules")
	}
	want := []ValueCandidate{
		{Value: "services/api", Help: "example.com/api"},
	}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestBuiltinValuesDiscoversGoMainPackages(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "cmd", "api"))
	writeFile(t, filepath.Join(dir, "cmd", "api", "main.go"), "package main\n")
	mkdirAll(t, filepath.Join(dir, "tools", "worker"))
	writeFile(t, filepath.Join(dir, "tools", "worker", "main.go"), "// Package main runs the worker.\npackage main\n")
	mkdirAll(t, filepath.Join(dir, "internal", "lib"))
	writeFile(t, filepath.Join(dir, "internal", "lib", "lib.go"), "package lib\n")
	mkdirAll(t, filepath.Join(dir, "node_modules", "ignored"))
	writeFile(t, filepath.Join(dir, "node_modules", "ignored", "main.go"), "package main\n")

	values, ok, err := BuiltinValues(ScriptCommand("@go-main-packages"), ValueBuiltinOptions{Context: t.Context(), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @go-main-packages")
	}
	want := []ValueCandidate{
		{Value: "./cmd/api", Help: "./cmd/api"},
		{Value: "./tools/worker", Help: "Package main runs the worker."},
	}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestBuiltinValuesDiscoversGoMainPackagesWithPrefixAndBrokenFile(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "cmd", "api"))
	writeFile(t, filepath.Join(dir, "cmd", "api", "broken.go"), "package")
	writeFile(t, filepath.Join(dir, "cmd", "api", "main.go"), "package main\n")
	mkdirAll(t, filepath.Join(dir, "tools", "worker"))
	writeFile(t, filepath.Join(dir, "tools", "worker", "main.go"), "package main\n")

	values, ok, err := BuiltinValues(
		ScriptCommand("@go-main-packages"),
		ValueBuiltinOptions{Context: t.Context(), Dir: dir, ValuePrefix: "./cmd/"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @go-main-packages")
	}
	want := []ValueCandidate{
		{Value: "./cmd/api", Help: "./cmd/api"},
	}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestBuiltinValuesDiscoversGoPackages(t *testing.T) {
	dir := t.TempDir()
	writeRootGoPackagesFixture(t, dir)

	values, ok, err := BuiltinValues(ScriptCommand("@go-packages"), ValueBuiltinOptions{Context: t.Context(), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @go-packages")
	}
	want := []ValueCandidate{
		{Value: "./cmd/api", Help: "example.com/root/cmd/api"},
		{Value: "./internal/lib", Help: "example.com/root/internal/lib"},
	}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestBuiltinValuesDiscoversGoPackagesWithPrefix(t *testing.T) {
	dir := t.TempDir()
	writeRootGoPackagesFixture(t, dir)

	values, ok, err := BuiltinValues(
		ScriptCommand("@go-packages"),
		ValueBuiltinOptions{Context: t.Context(), Dir: dir, ValuePrefix: "./internal/"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @go-packages")
	}
	want := []ValueCandidate{{Value: "./internal/lib", Help: "example.com/root/internal/lib"}}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestBuiltinValuesDiscoversGoPackagesWithBrokenSibling(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	mkdirAll(t, filepath.Join(dir, "good"))
	writeFile(t, filepath.Join(dir, "good", "good.go"), "package good\n")
	mkdirAll(t, filepath.Join(dir, "broken"))
	writeFile(t, filepath.Join(dir, "broken", "broken.go"), "package\n")

	values, ok, err := BuiltinValues(ScriptCommand("@go-packages"), ValueBuiltinOptions{Context: t.Context(), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @go-packages")
	}
	want := []ValueCandidate{{Value: "./good", Help: "example.com/root/good"}}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestBuiltinValuesSkipsNestedGoPackagesWithoutWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	mkdirAll(t, filepath.Join(dir, "internal", "rootlib"))
	writeFile(t, filepath.Join(dir, "internal", "rootlib", "rootlib.go"), "package rootlib\n")
	mkdirAll(t, filepath.Join(dir, "services", "api", "internal", "handler"))
	writeFile(t, filepath.Join(dir, "services", "api", "go.mod"), "module example.com/api\n")
	writeFile(t, filepath.Join(dir, "services", "api", "internal", "handler", "handler.go"), "package handler\n")

	values, ok, err := BuiltinValues(ScriptCommand("@go-packages"), ValueBuiltinOptions{Context: t.Context(), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @go-packages")
	}
	want := []ValueCandidate{
		{Value: "./internal/rootlib", Help: "example.com/root/internal/rootlib"},
	}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestBuiltinValuesDiscoversGoPackagesAcrossWorkspaceModules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	writeFile(t, filepath.Join(dir, "go.work"), "go 1.26\n\nuse (\n\t.\n\t./services/api\n)\n")
	mkdirAll(t, filepath.Join(dir, "internal", "rootlib"))
	writeFile(t, filepath.Join(dir, "internal", "rootlib", "rootlib.go"), "package rootlib\n")
	mkdirAll(t, filepath.Join(dir, "services", "api", "internal", "handler"))
	writeFile(t, filepath.Join(dir, "services", "api", "go.mod"), "module example.com/api\n")
	writeFile(t, filepath.Join(dir, "services", "api", "internal", "handler", "handler.go"), "package handler\n")
	mkdirAll(t, filepath.Join(dir, "services", "worker"))
	writeFile(t, filepath.Join(dir, "services", "worker", "go.mod"), "module example.com/worker\n")
	writeFile(t, filepath.Join(dir, "services", "worker", "worker.go"), "package worker\n")

	values, ok, err := BuiltinValues(ScriptCommand("@go-packages"), ValueBuiltinOptions{Context: t.Context(), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @go-packages")
	}
	want := []ValueCandidate{
		{Value: "./internal/rootlib", Help: "example.com/root/internal/rootlib"},
		{Value: "./services/api/internal/handler", Help: "example.com/api/internal/handler"},
	}
	if !slices.Equal(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestGoListPackagePatternNarrowsDirectoryPrefixes(t *testing.T) {
	for prefix, want := range map[string]string{
		"":                  "./...",
		".":                 "./...",
		"./":                "./...",
		"./internal":        "./...",
		"./internal/":       "./internal/...",
		"./internal/hand":   "./internal/...",
		"./internal/pkg/ha": "./internal/pkg/...",
	} {
		if got := goListPackagePattern(prefix); got != want {
			t.Fatalf("goListPackagePattern(%q) = %q, want %q", prefix, got, want)
		}
	}
}

func TestBuiltinValuesListsRecipes(t *testing.T) {
	values, ok, err := BuiltinValues(ScriptCommand("@recipes"), ValueBuiltinOptions{
		Recipes: map[string]Recipe{
			"build": {Help: "Build binary.", Cmd: Command{"go", "build"}},
			"test":  {Cmd: Command{"go", "test"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @recipes")
	}
	if len(values) != 2 ||
		values[0] != (ValueCandidate{Value: "build", Help: "Build binary."}) ||
		values[1] != (ValueCandidate{Value: "test", Help: "go test"}) {
		t.Fatalf("values = %#v", values)
	}
}

func TestBuiltinValuesListsVarsAndArguments(t *testing.T) {
	values, ok, err := BuiltinValues(ScriptCommand("@vars"), ValueBuiltinOptions{
		Recipe: Recipe{
			Vars: map[string]string{"project": "api"},
			Arguments: map[string]Argument{
				"mode": {Help: "Build mode.", Type: "string"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("BuiltinValues did not detect @vars")
	}
	if len(values) != 3 ||
		values[0] != (ValueCandidate{Value: "mode", Help: "Build mode."}) ||
		values[1] != (ValueCandidate{Value: "project", Help: "placeholder"}) ||
		values[2] != (ValueCandidate{Value: RunIDPlaceholder, Help: "run identifier"}) {
		t.Fatalf("values = %#v", values)
	}
}

func TestValidateValueBuiltinRejectsInvalidForms(t *testing.T) {
	tests := []struct {
		name    string
		command Command
	}{
		{name: "glob missing arg", command: ScriptCommand("@glob")},
		{name: "lines extra arg", command: ScriptCommand("@lines a b")},
		{name: "go modules extra arg", command: ScriptCommand("@go-modules x")},
		{name: "go main packages extra arg", command: ScriptCommand("@go-main-packages x")},
		{name: "go packages extra arg", command: ScriptCommand("@go-packages x")},
		{name: "recipes extra arg", command: ScriptCommand("@recipes x")},
		{name: "vars extra arg", command: ScriptCommand("@vars x")},
		{name: "non literal word", command: ScriptCommand(`@enum "$x"`)},
		{name: "multiple statements", command: ScriptCommand("@enum api; echo worker")},
		{name: "pipeline", command: ScriptCommand("@enum api | cat")},
		{name: "assignment", command: ScriptCommand("target=api @enum api")},
		{name: "redirect", command: ScriptCommand("@enum api > targets.txt")},
		{name: "background", command: ScriptCommand("@enum api &")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok, err := ValidateValueBuiltin(tt.command)
			if !ok {
				t.Fatalf("ValidateValueBuiltin did not detect builtin")
			}
			if err == nil {
				t.Fatal("ValidateValueBuiltin accepted invalid form")
			}
		})
	}
}

func TestValidateCommandRejectsEmptyRecipeReference(t *testing.T) {
	if err := ValidateCommand(Command{"@"}); err == nil {
		t.Fatal("ValidateCommand accepted empty recipe reference")
	}
}

func TestValidateCommandRejectsEmptyCrossConfigRecipeName(t *testing.T) {
	if err := ValidateCommand(Command{"@webui:"}); err == nil {
		t.Fatal("ValidateCommand accepted empty cross-config recipe name")
	}
}

func TestValidateCommandAcceptsBracketStyleRecipeReference(t *testing.T) {
	if err := ValidateCommand(Command{"@build[component=godoxy, mode=dev]"}); err != nil {
		t.Fatalf("ValidateCommand rejected bracket-style recipe reference: %v", err)
	}
}

func TestResolveDefaultsToSandboxed(t *testing.T) {
	got, err := Resolve("test", Recipe{Cmd: Command{"go", "test"}}, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxMode != SandboxModeWorkspace {
		t.Fatalf("SandboxMode = %q, want %q", got.SandboxMode, SandboxModeWorkspace)
	}
}

func TestResolvePreservesUnsandboxedRecipe(t *testing.T) {
	got, err := Resolve("tidy", Recipe{Cmd: Command{"go", "mod", "tidy"}, Sandboxed: new(SandboxModeHost)}, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxMode != SandboxModeHost {
		t.Fatalf("SandboxMode = %q, want %q", got.SandboxMode, SandboxModeHost)
	}
}

func TestSandboxModeUnmarshalAcceptsOnlyThreeValueContract(t *testing.T) {
	for name, tt := range map[string]struct {
		value   any
		want    SandboxMode
		wantErr string
	}{
		"workspace":      {value: true, want: SandboxModeWorkspace},
		"host":           {value: false, want: SandboxModeHost},
		"system":         {value: "system", want: SandboxModeSystem},
		"unknown string": {value: "docker", wantErr: `sandboxed string must be "system"`},
		"integer":        {value: int64(1), wantErr: "sandboxed must be true, false"},
	} {
		t.Run(name, func(t *testing.T) {
			var got SandboxMode
			err := got.UnmarshalTOML(tt.value)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("UnmarshalTOML(%#v) error = %v, want %q", tt.value, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("UnmarshalTOML(%#v) = %q, %v; want %q", tt.value, got, err, tt.want)
			}
		})
	}
}

func TestSystemImageSettingsRequireEffectiveSystemMode(t *testing.T) {
	_, err := Resolve("test", Recipe{Cmd: Command{"true"}, System: &SystemConfig{BaseImage: "ubuntu:24.04"}}, nil, nil, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), `require sandboxed = "system"`) {
		t.Fatalf("Resolve() error = %v, want system-mode requirement", err)
	}
	resolved, err := Resolve("test", Recipe{
		Cmd:       Command{"true"},
		Sandboxed: new(SandboxModeSystem),
		System:    &SystemConfig{BaseImage: "ubuntu:24.04"},
	}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Recipe.System == nil || resolved.Recipe.System.BaseImage != "ubuntu:24.04" {
		t.Fatalf("system = %#v", resolved.Recipe.System)
	}
}

func TestMergeRecipePreservesAndOverridesSystemImageSettings(t *testing.T) {
	base := Recipe{Sandboxed: new(SandboxModeSystem), System: &SystemConfig{BaseImage: "ubuntu:24.04"}}
	if got := MergeRecipe(base, Recipe{}).System.BaseImage; got != "ubuntu:24.04" {
		t.Fatalf("inherited base = %q", got)
	}
	if got := MergeRecipe(base, Recipe{System: &SystemConfig{BaseImage: "debian:12.11-slim"}}).System.BaseImage; got != "debian:12.11-slim" {
		t.Fatalf("overridden base = %q", got)
	}
}

func TestValidateRequirementsRejectsInvalidSystemPackages(t *testing.T) {
	for _, packages := range [][]string{{""}, {"lib ssl"}, {"git", "git"}} {
		err := ValidateConfig(Config{Recipes: map[string]Recipe{"test": {Cmd: Command{"true"}, Requires: Requirements{SystemPackages: packages}}}})
		if err == nil || !strings.Contains(err.Error(), "system_packages") {
			t.Fatalf("ValidateConfig(%q) error = %v", packages, err)
		}
	}
}

func TestResolveGoToolchainUsesExactToolchainAndPinnedDirectiveDefault(t *testing.T) {
	for name, content := range map[string]string{
		"toolchain": "module example.com/app\n\ngo 1.26\ntoolchain go1.26.7\n",
		"directive": "module example.com/app\n\ngo 1.26\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "go.mod")
			writeFile(t, path, content)
			got, err := ResolveGoToolchain(dir, dir)
			if err != nil {
				t.Fatal(err)
			}
			want := DefaultGoToolchain
			if name == "toolchain" {
				want = "1.26.7"
			}
			if got.Version != want || !strings.HasPrefix(got.Provenance, path+"#") {
				t.Fatalf("ResolveGoToolchain = %#v, want %s from %s", got, want, path)
			}
		})
	}
}

func TestResolveGoToolchainRejectsUnsupportedUnpinnedDirective(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.25\n")
	_, err := ResolveGoToolchain(dir, dir)
	if err == nil || !strings.Contains(err.Error(), "system.base_image") {
		t.Fatalf("ResolveGoToolchain error = %v, want pinned-base guidance", err)
	}
}

func TestResolveNodePackageManagerUsesAncestorDeclarationBeforeLockfile(t *testing.T) {
	root := t.TempDir()
	member := filepath.Join(root, "packages", "app")
	if err := os.MkdirAll(member, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "package.json"), `{"packageManager":"pnpm@10.12.1"}`)
	writeFile(t, filepath.Join(member, "package.json"), `{}`)
	writeFile(t, filepath.Join(member, "yarn.lock"), "lock")
	got, err := ResolveNodePackageManager(member)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "pnpm" || got.Version != "10.12.1" || got.ProjectDir != root || !got.Declared {
		t.Fatalf("ResolveNodePackageManager = %#v", got)
	}
}

func TestResolveNodePackageManagerRejectsNonExactDeclaration(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"packageManager":"pnpm@^10"}`)
	_, err := ResolveNodePackageManager(dir)
	if err == nil || !strings.Contains(err.Error(), "exact version") {
		t.Fatalf("ResolveNodePackageManager error = %v, want exact version rejection", err)
	}
}

func TestResolveNodePackageManagerPreservesExactVersionIdentity(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"packageManager":"PnPm@10.12.1-RC.1+Build.7"}`)
	got, err := ResolveNodePackageManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "pnpm" || got.Version != "10.12.1-RC.1+Build.7" || got.Identity != "pnpm@10.12.1-RC.1+Build.7" {
		t.Fatalf("ResolveNodePackageManager = %#v, want normalized name and exact version identity", got)
	}
}

func TestResolvePreservesSystemSandboxModeAndSyncOut(t *testing.T) {
	got, err := Resolve("build", Recipe{
		Cmd:       Command{"go", "build"},
		Sandboxed: new(SandboxModeSystem),
		SyncOut:   []string{"bin/app"},
	}, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxMode != SandboxModeSystem {
		t.Fatalf("SandboxMode = %q, want %q", got.SandboxMode, SandboxModeSystem)
	}
	if !slices.Equal(got.SyncOut, []string{"bin/app"}) {
		t.Fatalf("SyncOut = %#v, want system sandbox sync-out retained", got.SyncOut)
	}
}

func TestMergeRecipeOverridesSystemSandboxMode(t *testing.T) {
	base := Recipe{Sandboxed: new(SandboxModeHost)}
	if got := RecipeSandboxMode(MergeRecipe(base, Recipe{})); got != SandboxModeHost {
		t.Fatalf("omitted override mode = %q, want inherited host", got)
	}
	if got := RecipeSandboxMode(MergeRecipe(base, Recipe{Sandboxed: new(SandboxModeSystem)})); got != SandboxModeSystem {
		t.Fatalf("system override mode = %q, want system", got)
	}
}

func TestResolveUnsandboxedIgnoresSyncOut(t *testing.T) {
	got, err := Resolve(
		"tidy",
		Recipe{
			Cmd:       Command{"go", "mod", "tidy"},
			Sandboxed: new(SandboxModeHost),
			SyncOut:   []string{"{missing}"},
		},
		nil,
		[]string{"{also_missing}"},
		nil,
		"",
		GoProfile,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncOut != nil {
		t.Fatalf("SyncOut = %#v, want nil", got.SyncOut)
	}
}

func TestResolveAllowsForEachItemPlaceholders(t *testing.T) {
	got, err := Resolve(
		"lint",
		Recipe{
			ForEach: ScriptCommand("@enum a b"),
			Workdir: "{item}",
			Cmd:     ScriptCommand(`printf '%s:%s:%s' "{item_index}" "{item}" "{item_help}"`),
		},
		nil,
		nil,
		nil,
		"",
		GoProfile,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Recipe.Workdir != "{item}" {
		t.Fatalf("Workdir = %q, want {item}", got.Recipe.Workdir)
	}
	if body := ScriptBody(got.Main); !strings.Contains(body, "{item_index}") || !strings.Contains(body, "{item_help}") {
		t.Fatalf("main body = %q, want item placeholders preserved", body)
	}
}

func TestResolveRejectsForEachItemPlaceholderInSyncOut(t *testing.T) {
	_, err := Resolve(
		"lint",
		Recipe{
			ForEach: ScriptCommand("@enum a b"),
			Cmd:     ScriptCommand("true"),
			SyncOut: []string{"out/{item}.txt"},
		},
		nil,
		nil,
		nil,
		"",
		GoProfile,
	)
	if err == nil || !strings.Contains(err.Error(), "sync_out: missing value for {item}") {
		t.Fatalf("err = %v, want sync_out item placeholder rejection", err)
	}
}

func TestValidateConfigRejectsInvalidForEachBuiltin(t *testing.T) {
	err := ValidateConfig(Config{Recipes: map[string]Recipe{
		"lint": {
			ForEach: ScriptCommand("@go-modules x"),
			Cmd:     ScriptCommand("true"),
		},
	}})
	if err == nil || !strings.Contains(err.Error(), `recipe "lint" for_each: @go-modules does not take arguments`) {
		t.Fatalf("err = %v, want invalid for_each builtin", err)
	}
}

func TestBuiltinTidySandboxedCanBeOverridden(t *testing.T) {
	builtins := Builtins(GoProfile, BuiltinOptions{})
	if RecipeSandboxMode(builtins["tidy"]) != SandboxModeHost {
		t.Fatal("built-in tidy is sandboxed, want unsandboxed")
	}

	merged := MergeRecipe(builtins["tidy"], Recipe{Help: "Custom tidy."})
	if RecipeSandboxMode(merged) != SandboxModeHost {
		t.Fatal("partial override reset tidy sandboxing")
	}

	merged = MergeRecipe(merged, Recipe{Sandboxed: new(SandboxModeWorkspace)})
	if RecipeSandboxMode(merged) != SandboxModeWorkspace {
		t.Fatal("explicit override failed to reset tidy sandboxing")
	}
}

func TestBuiltinTidyRunsWorkspaceSyncPostHook(t *testing.T) {
	tidy := Builtins(GoProfile, BuiltinOptions{})["tidy"]
	if !slices.Equal(tidy.Cmd, Command{"go", "mod", "tidy"}) {
		t.Fatalf("tidy Cmd = %#v, want go mod tidy", tidy.Cmd)
	}
	if len(tidy.Post) != 1 {
		t.Fatalf("tidy Post = %#v, want one workspace sync post command", tidy.Post)
	}
	if body := ScriptBody(tidy.Post[0].Cmd); body != "if test -f go.work; then go work sync; fi" {
		t.Fatalf("tidy post script = %q, want conditional go work sync", body)
	}
	if len(tidy.ForEach) != 0 {
		t.Fatalf("tidy ForEach = %#v, want leaf recipe", tidy.ForEach)
	}
	_, domain, source, err := SelectAll("tidy", tidy)
	if err != nil {
		t.Fatal(err)
	}
	if domain != "modules" || source != GoModuleTargets {
		t.Fatalf("tidy all = domain %q source %q", domain, source)
	}
}

func TestGoBuiltinsHostMutatingRecipesAreUnsandboxed(t *testing.T) {
	builtins := go1264Builtins(t)
	for _, name := range []string{"fix", "fmt", "tidy"} {
		if RecipeSandboxMode(builtins[name]) != SandboxModeHost {
			t.Fatalf("built-in %s is sandboxed, want unsandboxed", name)
		}
	}
}

func TestGoBuiltinFixRequiresGoAfter126(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n\ngo 1.26\n")

	if _, ok := Builtins(GoProfile, BuiltinOptions{Context: t.Context(), Dir: dir})["fix"]; ok {
		t.Fatal("built-in fix exists for go 1.26, want absent")
	}

	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n\ngo 1.26.4\n")

	fix, ok := Builtins(GoProfile, BuiltinOptions{Context: t.Context(), Dir: dir})["fix"]
	if !ok {
		t.Fatal("built-in fix is missing for go 1.26.4")
	}
	if !slices.Equal(fix.Cmd, Command{"go", "fix", "{pkg}", "{@}"}) {
		t.Fatalf("fix command = %#v", fix.Cmd)
	}
}

func TestMostCommonGoDirectiveVersion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n\ngo 1.27\n")
	mkdirAll(t, filepath.Join(dir, "services", "api"))
	writeFile(t, filepath.Join(dir, "services", "api", "go.mod"), "module example.com/api\n\ngo 1.26.4\n")
	mkdirAll(t, filepath.Join(dir, "services", "worker"))
	writeFile(t, filepath.Join(dir, "services", "worker", "go.mod"), "module example.com/worker\n\ngo 1.26.4\n")

	if got := mostCommonGoDirectiveVersion(t.Context(), dir); got != "1.26.4" {
		t.Fatalf("mostCommonGoDirectiveVersion = %q, want 1.26.4", got)
	}

	empty := t.TempDir()
	if got := mostCommonGoDirectiveVersion(t.Context(), empty); got != "unknown" {
		t.Fatalf("mostCommonGoDirectiveVersion(empty) = %q, want unknown", got)
	}
}

func TestGoBuiltinsIncludeWorkflowRecipes(t *testing.T) {
	builtins := Builtins(GoProfile, BuiltinOptions{})
	for _, name := range []string{"check", "fmt", "run"} {
		if _, ok := builtins[name]; !ok {
			t.Fatalf("built-in %q is missing", name)
		}
	}
	if RecipeSandboxMode(builtins["fmt"]) != SandboxModeHost {
		t.Fatal("built-in fmt is sandboxed, want unsandboxed")
	}
	if !slices.Equal(builtins["fmt"].Cmd, Command{"go", "fmt", "{target}", "{@}"}) {
		t.Fatalf("fmt command = %#v", builtins["fmt"].Cmd)
	}
	target := builtins["fmt"].Arguments["target"]
	if target.Type != "rel_path" || target.Position != 1 || target.Default != "./..." {
		t.Fatalf("fmt target argument = %#v", target)
	}
	if values := ScriptBody(target.Values); values != GoFmtTargetValuesCommand {
		t.Fatalf("fmt target values = %q", values)
	}
	check := builtins["check"]
	if body := ScriptBody(check.Cmd); body != "set -e; @vet {@}; @test {@}" {
		t.Fatalf("check script = %q", body)
	}

	run := builtins["run"]
	if !slices.Equal(run.Cmd, Command{"go", "-C", "{cwd}", "run", "{command}", "{@}"}) {
		t.Fatalf("run command = %#v", run.Cmd)
	}
	cwd := run.Arguments["cwd"]
	if cwd.Type != "rel_path" || cwd.Default != "." {
		t.Fatalf("run cwd argument = %#v", cwd)
	}
	if values := ScriptBody(cwd.Values); values != GoModuleValuesCommand {
		t.Fatalf("run cwd values = %q", values)
	}
	command := run.Arguments["command"]
	if command.Type != "rel_path" || command.Position != 1 || !command.Required {
		t.Fatalf("run command argument = %#v", command)
	}
	if values := ScriptBody(command.Values); values != GoRunCommandValuesCommand {
		t.Fatalf("run command values = %q", values)
	}
}

func TestGoBuiltinRunPassesCwdToGoC(t *testing.T) {
	run := Builtins(GoProfile, BuiltinOptions{})["run"]

	got, err := Resolve("run", run, []string{"./cmd/api", "cwd=services/api", "--", "-tags=integration"}, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"go", "-C", "services/api", "run", "./cmd/api", "-tags=integration"}) {
		t.Fatalf("main = %#v", got.Main)
	}
}

func TestGoBuiltinsAreLeafRecipesWithExplicitAllPlans(t *testing.T) {
	builtins := go1264Builtins(t)
	for _, name := range []string{"build", "fix", "fmt", "generate", "install", "lint", "test", "test-race", "tidy", "vet"} {
		rec := builtins[name]
		if len(rec.ForEach) != 0 {
			t.Fatalf("%s for_each = %#v, want leaf recipe", name, rec.ForEach)
		}
		if rec.Workdir != "" {
			t.Fatalf("%s workdir = %q, want root", name, rec.Workdir)
		}
		if _, _, supported := AllSupport(rec); !supported {
			t.Fatalf("%s does not support --all", name)
		}
	}
}

func TestGoBuiltinInstallUsesStrippedLdflagsByDefault(t *testing.T) {
	install := Builtins(GoProfile, BuiltinOptions{})["install"]
	ldflags := install.Arguments["ldflags"]
	if ldflags.Default != "-s -w" {
		t.Fatalf("install ldflags default = %q, want -s -w", ldflags.Default)
	}

	got, err := Resolve("install", install, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"go", "install", "-ldflags=-s -w", "./..."}) {
		t.Fatalf("install command = %#v", got.Main)
	}
}

func TestGoBuiltinsExposePackageArgumentCompletions(t *testing.T) {
	builtins := go1264Builtins(t)
	for _, name := range []string{"fix", "generate", "lint", "test", "test-race", "vet"} {
		arg := builtins[name].Arguments["pkg"]
		if arg.Type != "rel_path" || arg.Position != 1 || arg.Default != "./..." {
			t.Fatalf("%s pkg argument = %#v", name, arg)
		}
		if values := ScriptBody(arg.Values); values != GoPackageValuesCommand {
			t.Fatalf("%s pkg values = %q", name, values)
		}
	}
}

func TestGoBuildAndInstallBuiltinsCompleteMainPackages(t *testing.T) {
	builtins := go1264Builtins(t)
	for _, name := range []string{"build", "install"} {
		arg := builtins[name].Arguments["pkg"]
		if arg.Type != "rel_path" || arg.Position != 1 || arg.Default != "./..." {
			t.Fatalf("%s pkg argument = %#v", name, arg)
		}
		if values := ScriptBody(arg.Values); values != GoMainPackageValuesCommand {
			t.Fatalf("%s pkg values = %q", name, values)
		}
	}
}

func TestSelectAllUsesRecipeSpecificTargetDomains(t *testing.T) {
	builtins := go1264Builtins(t)
	tests := []struct {
		name   string
		domain string
		source TargetSource
	}{
		{name: "build", domain: "main packages", source: GoMainPackageTargets},
		{name: "fmt", domain: "packages", source: GoPackageTargets},
		{name: "tidy", domain: "modules", source: GoModuleTargets},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			all, domain, source, err := SelectAll(test.name, builtins[test.name])
			if err != nil {
				t.Fatal(err)
			}
			if domain != test.domain || source != test.source {
				t.Fatalf("domain = %q source = %q", domain, source)
			}
			if _, exists := all.Arguments["pkg"]; exists {
				t.Fatalf("all arguments retain primary target: %#v", all.Arguments)
			}
		})
	}
	if _, _, _, err := SelectAll("run", builtins["run"]); err == nil || !strings.Contains(err.Error(), "process policy") {
		t.Fatalf("SelectAll(run) error = %v", err)
	}
}

func TestProfileBuiltinsDeclareAllSupportDecision(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"scripts":{"test":"node --test"}}`)
	for profile, builtins := range map[string]map[string]Recipe{
		GoProfile:   go1264Builtins(t),
		NodeProfile: Builtins(NodeProfile, BuiltinOptions{Dir: dir}),
	} {
		for name, rec := range builtins {
			if rec.all == nil {
				t.Fatalf("profile %s recipe %s has no --all decision", profile, name)
			}
		}
	}
}

func TestResolveAllRejectsExplicitTargetButAllowsLiteralPassthrough(t *testing.T) {
	all, domain, source, err := SelectAll("test", Builtins(GoProfile, BuiltinOptions{})["test"])
	if err != nil {
		t.Fatal(err)
	}
	opts := ResolveOptions{Scope: ScopeAll, TargetDomain: domain, TargetSource: source}
	if _, err := ResolveWithOptions("test", all, []string{"./internal/recipe"}, nil, nil, "", GoProfile, opts); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("explicit target error = %v", err)
	}
	if _, err := ResolveWithOptions("test", all, []string{"unknown=value"}, nil, nil, "", GoProfile, opts); err == nil || !strings.Contains(err.Error(), `unknown argument "unknown"`) {
		t.Fatalf("unknown argument error = %v", err)
	}
	if _, err := ResolveWithOptions("test", all, []string{"-run", "TestName"}, nil, nil, "", GoProfile, opts); err == nil || !strings.Contains(err.Error(), "use -- before passthrough arguments with separate values") {
		t.Fatalf("separate passthrough value error = %v", err)
	}
	resolved, err := ResolveWithOptions("test", all, []string{"--", "-run", "TestName"}, nil, nil, "", GoProfile, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resolved.Main, Command{"go", "test", "{item}", "-run", "TestName"}) {
		t.Fatalf("main = %#v", resolved.Main)
	}
}

func TestResolveExecutionTargetsRebasesNestedMainPackages(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	mkdirAll(t, filepath.Join(dir, "cmd", "root"))
	writeFile(t, filepath.Join(dir, "cmd", "root", "main.go"), "package main\nfunc main() {}\n")
	mkdirAll(t, filepath.Join(dir, "services", "api", "cmd", "server"))
	writeFile(t, filepath.Join(dir, "services", "api", "go.mod"), "module example.com/api\n")
	writeFile(t, filepath.Join(dir, "services", "api", "cmd", "server", "main.go"), "package main\nfunc main() {}\n")

	targets, err := ResolveExecutionTargets(t.Context(), GoMainPackageTargets, dir, os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []ExecutionTarget{
		{Label: "./cmd/root", Value: "./cmd/root", Workdir: "."},
		{Label: "./services/api/cmd/server", Value: "./cmd/server", Workdir: "services/api"},
	}
	if !slices.Equal(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestResolveExecutionTargetsHonorsMainPackageBuildConstraints(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOFLAGS", "")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/project\n")
	mkdirAll(t, filepath.Join(dir, "cmd", "enabled"))
	writeFile(t, filepath.Join(dir, "cmd", "enabled", "main.go"), "package main\nfunc main() {}\n")
	mkdirAll(t, filepath.Join(dir, "cmd", "disabled"))
	writeFile(t, filepath.Join(dir, "cmd", "disabled", "main.go"), "//go:build shadowtree_disabled\n\npackage main\nfunc main() {}\n")

	targets, err := ResolveExecutionTargets(t.Context(), GoMainPackageTargets, dir, os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []ExecutionTarget{{Label: "./cmd/enabled", Value: "./cmd/enabled", Workdir: "."}}
	if !slices.Equal(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestResolveExecutionTargetsUsesInvocationBuildContext(t *testing.T) {
	tests := []struct {
		name      string
		goFlags   string
		buildArgs []string
	}{
		{name: "resolved environment", goFlags: "-tags=tools"},
		{name: "separate forwarded tag", buildArgs: []string{"-tags", "tools"}},
		{name: "joined forwarded tag", buildArgs: []string{"-tags=tools"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("GOFLAGS", tt.goFlags)
			writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/project\n")
			mkdirAll(t, filepath.Join(dir, "cmd", "tools"))
			writeFile(t, filepath.Join(dir, "cmd", "tools", "main.go"), "//go:build tools\n\npackage main\nfunc main() {}\n")

			targets, err := ResolveExecutionTargets(t.Context(), GoMainPackageTargets, dir, os.Environ(), tt.buildArgs)
			if err != nil {
				t.Fatal(err)
			}
			want := []ExecutionTarget{{Label: "./cmd/tools", Value: "./cmd/tools", Workdir: "."}}
			if !slices.Equal(targets, want) {
				t.Fatalf("targets = %#v, want %#v", targets, want)
			}
		})
	}
}

type cancelDuringWalkContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (ctx *cancelDuringWalkContext) Err() error {
	if ctx.remaining == 0 {
		ctx.cancel()
	} else {
		ctx.remaining--
	}
	return ctx.Context.Err()
}

func TestResolveExecutionTargetsCancelsDuringModuleWalk(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/project\n")
	mkdirAll(t, filepath.Join(dir, "nested"))
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &cancelDuringWalkContext{Context: base, cancel: cancel, remaining: 2}

	_, err := ResolveExecutionTargets(ctx, GoPackageTargets, dir, os.Environ(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestGoBuiltinOverrideCanReplaceWorkdir(t *testing.T) {
	merged := MergeRecipe(Builtins(GoProfile, BuiltinOptions{})["test"], Recipe{
		Workdir: ".",
		Cmd:     Command{"go", "test", "{pkg}"},
		Arguments: map[string]Argument{
			"pkg": {Type: "rel_path", Required: true, Values: ScriptCommand(GoPackageValuesCommand)},
		},
	})
	if merged.Workdir != "." {
		t.Fatalf("workdir = %q, want .", merged.Workdir)
	}
}

func TestMergeRecipesDoesNotInheritBuiltinForEachAndWorkdir(t *testing.T) {
	merged, err := MergeRecipes(Builtins(GoProfile, BuiltinOptions{}), map[string]Recipe{
		"test": {Help: "Run custom tests.", Cmd: Command{"go", "test", "{pkg}"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := merged["test"]
	if len(got.ForEach) != 0 {
		t.Fatalf("ForEach = %#v, want empty", got.ForEach)
	}
	if got.Workdir != "" {
		t.Fatalf("Workdir = %q, want empty", got.Workdir)
	}
}

func TestMergeRecipesAllowsExplicitForEachAndWorkdirOverride(t *testing.T) {
	merged, err := MergeRecipes(Builtins(GoProfile, BuiltinOptions{}), map[string]Recipe{
		"test": {
			ForEach: Command{"go", "list", "-f", "{{.Dir}}", "./..."},
			Workdir: ".",
			Cmd:     Command{"go", "test", "{pkg}"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := merged["test"]
	if !slices.Equal(got.ForEach, Command{"go", "list", "-f", "{{.Dir}}", "./..."}) {
		t.Fatalf("ForEach = %#v", got.ForEach)
	}
	if got.Workdir != "." {
		t.Fatalf("workdir = %q, want .", got.Workdir)
	}
}

func TestMergeRecipesRejectsReservedNames(t *testing.T) {
	if _, err := MergeRecipes(nil, map[string]Recipe{"exec": {Cmd: Command{"go"}}}); err == nil {
		t.Fatal("MergeRecipes succeeded with reserved name")
	}
}

func TestMergeRecipesRejectsBuiltinReferenceNames(t *testing.T) {
	for _, name := range BuiltinReferenceNames() {
		if _, err := MergeRecipes(nil, map[string]Recipe{name: {Cmd: Command{"go"}}}); err == nil {
			t.Fatalf("MergeRecipes succeeded with builtin reference name %q", name)
		}
	}
}

func TestMergeRecipesAllowsRunOverride(t *testing.T) {
	merged, err := MergeRecipes(Builtins(GoProfile, BuiltinOptions{}), map[string]Recipe{
		"run": {Help: "Run custom command."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := merged["run"].Help; got != "Run custom command." {
		t.Fatalf("run Help = %q", got)
	}
}

func TestHelpSummarizesMultilineScript(t *testing.T) {
	got := Help(Recipe{
		Pre: stageCommands(Command{"go", "build"}),
		Cmd: Command{"sh", "-c", "set -eu\ninstall -d \"$bindir\"\n"},
	})

	if got != "sh -c <script>  +1 pre" {
		t.Fatalf("Help = %q", got)
	}
}

func TestHelpSummarizesSingleLineScript(t *testing.T) {
	got := Help(Recipe{
		Cmd: ScriptCommand("go test ./..."),
	})

	if got != "go test ./..." {
		t.Fatalf("Help = %q", got)
	}
}

func TestHelpUsesConfiguredHelp(t *testing.T) {
	got := Help(Recipe{
		Help: "Run tests\ninside a shadow workspace.",
		Cmd:  Command{"go", "test", "{pkg}", "{@}"},
	})

	if got != "Run tests inside a shadow workspace." {
		t.Fatalf("Help = %q", got)
	}
}

func TestMergeRecipeKeepsBaseHelpUnlessOverridden(t *testing.T) {
	got := MergeRecipe(
		Recipe{Help: "Run tests.", Cmd: Command{"go", "test"}},
		Recipe{Pre: stageCommands(Command{"go", "generate", "./..."})},
	)
	if got.Help != "Run tests." {
		t.Fatalf("Help = %q", got.Help)
	}

	got = MergeRecipe(got, Recipe{Help: "Run race tests."})
	if got.Help != "Run race tests." {
		t.Fatalf("Help = %q", got.Help)
	}
}

func TestMergeArgumentOverridesRanges(t *testing.T) {
	got := MergeArgument(
		Argument{Type: "int", Min: 1, Max: 8, Default: 4},
		Argument{Min: 2, Max: 16},
	)
	if got.Min != 2 || got.Max != 16 || got.Default != 4 {
		t.Fatalf("argument = %#v", got)
	}
}

func TestResolveTypedArgumentsByNameAndPosition(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"go", "build", "-o", "bin/{binary}", "{project}", "{@}"},
		Arguments: map[string]Argument{
			"project": {Type: "string", Position: 1, Default: "./cmd/default"},
			"binary":  {Type: "string", Default: "shadowtree"},
		},
		SyncOut: []string{"bin/{binary}"},
	}

	got, err := Resolve("build", rec, []string{"./cmd/other", "binary=other"}, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"go", "build", "-o", "bin/other", "./cmd/other"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
	if !slices.Equal(got.SyncOut, []string{"bin/other"}) {
		t.Fatalf("SyncOut = %#v", got.SyncOut)
	}
}

func TestResolveTypedArgumentsUsesDefaults(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"go", "build", "{project}", "{@}"},
		Arguments: map[string]Argument{
			"project": {Type: "string", Default: "./cmd/shadowtree"},
		},
	}

	got, err := Resolve("build", rec, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"go", "build", "./cmd/shadowtree"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveTypedArgumentsAppliesPresetDefaults(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"vegeta", "-connections", "{connections}", "-requests", "{requests}", "-runs", "{runs}", "{@}"},
		Arguments: map[string]Argument{
			"connections": {Type: "int", Default: 32},
			"requests":    {Type: "int", Default: 1000},
			"runs":        {Type: "int", Default: 1},
		},
		Presets: map[string]RecipePreset{
			"stable": {
				Arguments: map[string]any{
					"connections": int64(64),
					"requests":    int64(20000),
					"runs":        int64(5),
				},
			},
		},
	}

	got, err := Resolve("benchmark", rec, []string{"preset=stable", "runs=7", "--", "-label=stable"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := Command{"vegeta", "-connections", "64", "-requests", "20000", "-runs", "7", "-label=stable"}
	if !slices.Equal(got.Main, want) {
		t.Fatalf("Main = %#v, want %#v", got.Main, want)
	}
}

func TestResolveTypedArgumentsRejectsUnknownPreset(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"run", "{connections}"},
		Arguments: map[string]Argument{
			"connections": {Type: "int", Default: 32},
		},
		Presets: map[string]RecipePreset{
			"stable": {},
		},
	}

	_, err := Resolve("benchmark", rec, []string{"preset=smoke"}, nil, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), `unknown preset "smoke"`) {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveUntypedRecipeSplicesVariadicArgs(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"go", "test", "./...", "{@}"},
	}

	got, err := Resolve("test", rec, []string{"-run=TestResolve", "-count=1"}, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"go", "test", "./...", "-run=TestResolve", "-count=1"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveTypedRecipeSplicesVariadicArgs(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"go", "test", "{pkg}", "{@}"},
		Arguments: map[string]Argument{
			"pkg": {Type: "rel_path", Position: 1, Default: "./..."},
		},
	}

	got, err := Resolve("test", rec, []string{"./internal/recipe", "-run=TestResolve", "-count=1"}, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"go", "test", "./internal/recipe", "-run=TestResolve", "-count=1"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveTypedRecipeKeepsKnownArgsAfterVariadicOptIn(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"go", "test", "{pkg}", "{@}"},
		Arguments: map[string]Argument{
			"pkg": {Type: "rel_path", Position: 1, Default: "./..."},
		},
	}

	got, err := Resolve("test", rec, []string{"pkg=./internal/runner", "--", "pkg=literal"}, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"go", "test", "./internal/runner", "pkg=literal"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveTypedRecipeRejectsUnknownNamedArgWithVariadicArgs(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"go", "test", "{pkg}", "{@}"},
		Arguments: map[string]Argument{
			"pkg": {Type: "rel_path", Position: 1, Default: "./..."},
		},
	}

	_, err := Resolve("test", rec, []string{"./internal/recipe", "count=1"}, nil, nil, "", GoProfile)
	if err == nil || !strings.Contains(err.Error(), `unknown argument "count"`) {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveTypedRecipeSplicesFlagBeforePosition(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"go", "test", "{pkg}", "{@}"},
		Arguments: map[string]Argument{
			"pkg": {Type: "rel_path", Position: 1, Default: "./..."},
		},
	}

	got, err := Resolve("test", rec, []string{"-race", "./internal/recipe"}, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"go", "test", "./internal/recipe", "-race"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveRejectsEmbeddedVariadicArgsPlaceholder(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"go", "test", "./...", "-run={@}"},
	}

	_, err := Resolve("test", rec, []string{"TestResolve"}, nil, nil, "", GoProfile)
	if err == nil || !strings.Contains(err.Error(), "{@} must be a whole argument item") {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveExpandsVariadicArgsPlaceholderInScriptCommand(t *testing.T) {
	rec := Recipe{
		Cmd: ScriptCommand(`printf '%s\n' {@}`),
	}

	got, err := Resolve("script", rec, []string{"value one", "two"}, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if ScriptBody(got.Main) != "printf '%s\\n' 'value one' two" {
		t.Fatalf("script body = %q", ScriptBody(got.Main))
	}
}

func TestResolveEscapesQuotedPlaceholdersInScriptCommand(t *testing.T) {
	rec := Recipe{
		Cmd: ScriptCommand(`printf "%s\n" "{value}" '{value}' --flag="{value}"`),
		Arguments: map[string]Argument{
			"value": {Default: `alpha" ' beta $HOME \tail`},
		},
	}

	got, err := Resolve("script", rec, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	want := `printf "%s\n" "alpha\" ' beta \$HOME \\tail" 'alpha" '\'' beta $HOME \tail' --flag="alpha\" ' beta \$HOME \\tail"`
	if ScriptBody(got.Main) != want {
		t.Fatalf("script body = %q, want %q", ScriptBody(got.Main), want)
	}
}

func TestResolveEscapesQuotedPlaceholdersInCommandSubstitution(t *testing.T) {
	rec := Recipe{
		Cmd: ScriptCommand(`printf "%s\n" "$(printf "{value}")"`),
		Arguments: map[string]Argument{
			"value": {Default: `ok"; touch injected; printf "`},
		},
	}

	got, err := Resolve("script", rec, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	want := `printf "%s\n" "$(printf "ok\"; touch injected; printf \"")"`
	if ScriptBody(got.Main) != want {
		t.Fatalf("script body = %q, want %q", ScriptBody(got.Main), want)
	}
}

func TestResolveExpandsExplicitPlaceholderModesInScriptCommand(t *testing.T) {
	rec := Recipe{
		Cmd: ScriptCommand(`printf '%s\n' {word:shell} "https://{host:dq}" "{raw:raw}"`),
		Arguments: map[string]Argument{
			"word": {Default: `alpha beta; $HOME`},
			"host": {Default: `api"$(uname)\tail`},
			"raw":  {Default: `raw"value`},
		},
	}

	got, err := Resolve("script", rec, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	want := `printf '%s\n' ` + shellQuote(`alpha beta; $HOME`) + ` "https://api\"\$(uname)\\tail" "raw"value"`
	if ScriptBody(got.Main) != want {
		t.Fatalf("script body = %q, want %q", ScriptBody(got.Main), want)
	}
}

func TestResolveRejectsInvalidExplicitPlaceholderModesInScriptCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
		want string
	}{
		{name: "shell in double quotes", cmd: ScriptCommand(`printf "%s\n" "{value:shell}"`), want: "{value:shell} must not be inside quotes"},
		{name: "dq outside quotes", cmd: ScriptCommand(`printf '%s\n' {value:dq}`), want: "{value:dq} must be inside double quotes"},
		{name: "dq in single quotes", cmd: ScriptCommand(`printf '%s\n' '{value:dq}'`), want: "{value:dq} must be inside double quotes"},
		{name: "unsupported", cmd: ScriptCommand(`printf '%s\n' {value:json}`), want: `{value:json} uses unsupported placeholder mode "json"`},
		{name: "variadic mode", cmd: ScriptCommand(`printf '%s\n' {@:shell}`), want: "{@:shell} does not support placeholder modes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := Recipe{
				Cmd: tt.cmd,
				Arguments: map[string]Argument{
					"value": {Default: "alpha"},
				},
			}
			_, err := Resolve("script", rec, nil, nil, nil, "", GoProfile)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Resolve error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestResolveExpandsRawModeInNonShellStrings(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"printf", "%s", "{value:raw}"},
		Env: map[string]string{
			"VALUE": "{value:raw}",
		},
		Arguments: map[string]Argument{
			"value": {Default: "alpha beta"},
		},
	}

	got, err := Resolve("script", rec, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"printf", "%s", "alpha beta"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
	if got.Recipe.Env["VALUE"] != "alpha beta" {
		t.Fatalf("env VALUE = %q", got.Recipe.Env["VALUE"])
	}
}

func TestResolveRejectsShellModesInNonShellStrings(t *testing.T) {
	tests := []struct {
		name string
		rec  Recipe
		want string
	}{
		{
			name: "argv shell",
			rec: Recipe{
				Cmd: Command{"printf", "%s", "{value:shell}"},
				Arguments: map[string]Argument{
					"value": {Default: "alpha"},
				},
			},
			want: "{value:shell} mode is supported only in shell commands",
		},
		{
			name: "env dq",
			rec: Recipe{
				Cmd: Command{"true"},
				Env: map[string]string{
					"VALUE": "{value:dq}",
				},
				Arguments: map[string]Argument{
					"value": {Default: "alpha"},
				},
			},
			want: "{value:dq} mode is supported only in shell commands",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve("script", tt.rec, nil, nil, nil, "", GoProfile)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Resolve error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestResolveExpandsShellPreludePlaceholders(t *testing.T) {
	rec := applyGlobals(t, map[string]Recipe{
		"script": {
			ShellPrelude: `NAME="{name}"`,
			Cmd:          ScriptCommand(`printf '%s/%s\n' "$ROOT" "$NAME"`),
			Arguments: map[string]Argument{
				"name": {Default: `alpha" $HOME`},
			},
		},
	}, map[string]string{
		"root": "src path",
	}, "sh", `set_root() { ROOT='{root}'; }
set_root`)["script"]

	got, err := Resolve("script", rec, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	want := `set_root() { ROOT='src path'; }
set_root
NAME="alpha\" \$HOME"
printf '%s/%s\n' "$ROOT" "$NAME"`
	if ScriptBody(got.Main) != want {
		t.Fatalf("script body = %q, want %q", ScriptBody(got.Main), want)
	}
	if got.Recipe.ShellPrelude != `set_root() { ROOT='src path'; }
set_root
NAME="alpha\" \$HOME"` {
		t.Fatalf("ShellPrelude = %q", got.Recipe.ShellPrelude)
	}
}

func TestResolveRejectsMissingShellPreludePlaceholder(t *testing.T) {
	rec := Recipe{
		ShellPrelude: "VALUE={missing}",
		Cmd:          ScriptCommand("true"),
	}

	_, err := Resolve("script", rec, nil, nil, nil, "", GoProfile)
	if err == nil || !strings.Contains(err.Error(), "recipe \"script\" shell_prelude: missing value for {missing}") {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveRejectsInvalidShellPreludeVariadicMode(t *testing.T) {
	rec := Recipe{
		ShellPrelude: "set -- {@:shell}",
		Cmd:          ScriptCommand("true"),
	}

	_, err := Resolve("script", rec, nil, nil, nil, "", GoProfile)
	if err == nil || !strings.Contains(err.Error(), "recipe \"script\" shell_prelude: {@:shell} does not support placeholder modes") {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveLeavesUnusedShellPreludePlaceholders(t *testing.T) {
	rec := Recipe{
		ShellPrelude: "VALUE={missing}",
		Cmd:          Command{"go", "test"},
	}

	got, err := Resolve("test", rec, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"go", "test"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
	if got.Recipe.ShellPrelude != "VALUE={missing}" {
		t.Fatalf("ShellPrelude = %q", got.Recipe.ShellPrelude)
	}
}

func TestCommandWithRecipeReferenceExpandedPrelude(t *testing.T) {
	prelude := `project_values() { printf '%s\n' "{project}"; }`
	if !containsScriptPlaceholder(prelude) {
		t.Fatal("containsScriptPlaceholder = false, want true")
	}
	expanded, err := expandScriptPlaceholders(prelude, map[string]string{"project": "cmd/api"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if expanded != `project_values() { printf '%s\n' "cmd/api"; }` {
		t.Fatalf("expanded prelude = %q", expanded)
	}
	got, err := CommandWithRecipeReferenceExpandedPrelude(
		ScriptCommand("project_values"),
		"",
		prelude,
		map[string]string{"project": "cmd/api"},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `project_values() { printf '%s\n' "cmd/api"; }
project_values`
	if ScriptBody(got) != want {
		t.Fatalf("script body = %q, want %q", ScriptBody(got), want)
	}
}

func TestResolveDoesNotInheritOuterQuotesInsideCommandSubstitution(t *testing.T) {
	rec := Recipe{
		Cmd: ScriptCommand(`printf "%s\n" "$(printf {value})"`),
		Arguments: map[string]Argument{
			"value": {Default: `raw"value`},
		},
	}

	got, err := Resolve("script", rec, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	want := `printf "%s\n" "$(printf raw"value)"`
	if ScriptBody(got.Main) != want {
		t.Fatalf("script body = %q, want %q", ScriptBody(got.Main), want)
	}
}

func TestResolveRejectsEmbeddedVariadicArgsPlaceholderInScriptCommand(t *testing.T) {
	rec := Recipe{
		Cmd: ScriptCommand(`printf prefix{@}suffix`),
	}

	_, err := Resolve("script", rec, []string{"value"}, nil, nil, "", GoProfile)
	if err == nil || !strings.Contains(err.Error(), "{@} must be a whole shell word in cmd") {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveRejectsVariadicArgsPlaceholderInSyncOut(t *testing.T) {
	rec := Recipe{
		Cmd:     Command{"go", "test", "{@}"},
		SyncOut: []string{"{@}"},
	}

	_, err := Resolve("test", rec, []string{"out.txt"}, nil, nil, "", GoProfile)
	if err == nil || !strings.Contains(err.Error(), "{@} is not supported in sync_out") {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveRejectsVariadicArgsPlaceholderInUnsandboxedSyncOut(t *testing.T) {
	rec := Recipe{
		Cmd:       Command{"go", "test", "{@}"},
		Sandboxed: new(SandboxModeHost),
		SyncOut:   []string{"{@}"},
	}

	_, err := Resolve("test", rec, []string{"out.txt"}, nil, nil, "", GoProfile)
	if err == nil || !strings.Contains(err.Error(), "{@} is not supported in sync_out") {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveRejectsVariadicArgsPlaceholderInGlobalSyncOut(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"go", "test", "{@}"},
	}

	_, err := Resolve("test", rec, []string{"out.txt"}, []string{"{@}"}, nil, "", GoProfile)
	if err == nil || !strings.Contains(err.Error(), "{@} is not supported in sync_out") {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveExpandsGlobalVarsAndArgumentValues(t *testing.T) {
	rec := applyGlobals(t, map[string]Recipe{
		"build": {
			Cmd: Command{"go", "build", "-ldflags={go_ldflags}", "{project}", "{@}"},
			Arguments: map[string]Argument{
				"project": {Default: "./cmd/default"},
			},
		},
	}, map[string]string{"go_ldflags": "-s -w"}, "", "")["build"]

	got, err := Resolve("build", rec, []string{"project=./cmd/shadowtree"}, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"go", "build", "-ldflags=-s -w", "./cmd/shadowtree"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveExpandsVarsInVars(t *testing.T) {
	rec := applyGlobals(t, map[string]Recipe{
		"docs": {
			Vars: map[string]string{
				"docs_dir": "{webui_dir}/wiki",
			},
			Cmd: Command{"printf", "%s", "{repo_url}", "{docs_dir}"},
		},
	}, map[string]string{
		"repo_url":  "https://github.com/yusing/godoxy",
		"repo_root": "/src/godoxy",
		"webui_dir": "{repo_root}/webui",
	}, "", "")["docs"]

	got, err := Resolve("docs", rec, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"printf", "%s", "https://github.com/yusing/godoxy", "/src/godoxy/webui/wiki"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
	if got.Recipe.Vars["docs_dir"] != "/src/godoxy/webui/wiki" {
		t.Fatalf("docs_dir = %q", got.Recipe.Vars["docs_dir"])
	}
}

func TestResolveExpandsGlobalEnv(t *testing.T) {
	rec := applyGlobals(t, map[string]Recipe{
		"docs": {
			Cmd: Command{"printf", "%s", "$DOCS_DIR"},
		},
	}, map[string]string{
		"docs_dir":  "{repo_root}/docs",
		"repo_root": "/src/shadowtree",
		"repo_url":  "https://github.com/yusing/shadowtree",
	}, "", "")["docs"]

	got, err := Resolve("docs", rec, nil, nil, map[string]string{
		"DOCS_DIR": "{docs_dir}",
		"REPO_URL": "{repo_url}",
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.GlobalEnv["DOCS_DIR"] != "/src/shadowtree/docs" {
		t.Fatalf("GlobalEnv[DOCS_DIR] = %q", got.GlobalEnv["DOCS_DIR"])
	}
	if got.GlobalEnv["REPO_URL"] != "https://github.com/yusing/shadowtree" {
		t.Fatalf("GlobalEnv[REPO_URL] = %q", got.GlobalEnv["REPO_URL"])
	}
}

func TestResolveExpandsRecipeEnvWithArguments(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"printf", "%s", "$PROJECT_DIR"},
		Arguments: map[string]Argument{
			"project": {Default: "shadowtree"},
		},
		Env: map[string]string{
			"PROJECT_DIR": "/src/{project}",
		},
	}

	got, err := Resolve("docs", rec, []string{"project=godoxy"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Recipe.Env["PROJECT_DIR"] != "/src/godoxy" {
		t.Fatalf("Recipe.Env[PROJECT_DIR] = %q", got.Recipe.Env["PROJECT_DIR"])
	}
}

func TestResolveEnvLeavesVariadicArgsPlaceholderLiteral(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"printf", "%s", "$LITERAL"},
		Env: map[string]string{
			"LITERAL": "{@}",
		},
	}

	got, err := Resolve("docs", rec, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Recipe.Env["LITERAL"] != "{@}" {
		t.Fatalf("Recipe.Env[LITERAL] = %q", got.Recipe.Env["LITERAL"])
	}
}

func TestResolveRejectsEnvMissingPlaceholder(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"true"},
		Env: map[string]string{
			"DOCS_DIR": "{missing}/docs",
		},
	}

	_, err := Resolve("docs", rec, nil, nil, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), `recipe "docs" env: DOCS_DIR: missing value for {missing}`) {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveRejectsVarsMissingPlaceholder(t *testing.T) {
	rec := Recipe{
		Vars: map[string]string{
			"docs_dir": "{webui_dir}/wiki",
		},
		Cmd: Command{"printf", "%s", "{docs_dir}"},
	}

	_, err := Resolve("docs", rec, nil, nil, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), `recipe "docs" vars: docs_dir: missing value for {webui_dir}`) {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveRejectsRecursiveVars(t *testing.T) {
	rec := Recipe{
		Vars: map[string]string{
			"docs_dir":  "{webui_dir}/wiki",
			"webui_dir": "{docs_dir}",
		},
		Cmd: Command{"printf", "%s", "{docs_dir}"},
	}

	_, err := Resolve("docs", rec, nil, nil, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), `recipe "docs" vars: docs_dir: recursive reference to {docs_dir}`) {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestResolveShellScriptUsesDefaultShellAndPrelude(t *testing.T) {
	rec := applyGlobals(t, map[string]Recipe{
		"script": {
			Cmd: ScriptCommand("say_hi"),
		},
	}, nil, "", "say_hi() { printf hi; }")["script"]

	got, err := Resolve("script", rec, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{scriptCommand, "sh", "say_hi() { printf hi; }\nsay_hi", "shadowtree"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveShellScriptUsesConfiguredShell(t *testing.T) {
	rec := applyGlobals(t, map[string]Recipe{
		"script": {
			Cmd: ScriptCommand("[[ -n ${BASH_VERSION:-} ]] && printf ok"),
		},
	}, nil, "bash", "")["script"]

	got, err := Resolve("script", rec, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{scriptCommand, "bash", "[[ -n ${BASH_VERSION:-} ]] && printf ok", "shadowtree"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveMainKeepsShellBuiltinAsScript(t *testing.T) {
	rec := Recipe{Cmd: ScriptCommand("cd /")}

	got, err := Resolve("script", rec, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{scriptCommand, "sh", "cd /", "shadowtree"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestExpandPlaceholdersIgnoresShellParameterExpansion(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"sh", "-c", `: "${PHP_DIR:=/var/www/frsip}"; printf {project}`},
		Arguments: map[string]Argument{
			"project": {Default: "api"},
		},
	}

	got, err := Resolve("script", rec, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Main[2] != `: "${PHP_DIR:=/var/www/frsip}"; printf api` {
		t.Fatalf("script = %q", got.Main[2])
	}
}

func TestValidateConfigRejectsInvalidVarKeys(t *testing.T) {
	for name, cfg := range map[string]Config{
		"static": {
			Vars: map[string]string{"go-ldflags": "-s -w"},
		},
		"dynamic": {
			VarCommands: map[string]Command{"1name": ScriptCommand("printf value")},
		},
		"recipe": {
			Recipes: map[string]Recipe{
				"build": {
					Cmd:  Command{"go", "build"},
					Vars: map[string]string{"bad name": "value"},
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateConfig(cfg); err == nil {
				t.Fatal("ValidateConfig succeeded with invalid var key")
			}
		})
	}
}

func TestValidateConfigRejectsReservedRunIDDeclarations(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{
			name: "top vars",
			cfg:  Config{Vars: map[string]string{RunIDPlaceholder: "custom"}},
		},
		{
			name: "var commands",
			cfg:  Config{VarCommands: map[string]Command{RunIDPlaceholder: ScriptCommand("printf custom")}},
		},
		{
			name: "recipe vars",
			cfg: Config{Recipes: map[string]Recipe{
				"test": {Cmd: Command{"true"}, Vars: map[string]string{RunIDPlaceholder: "custom"}},
			}},
		},
		{
			name: "arguments",
			cfg: Config{Recipes: map[string]Recipe{
				"test": {
					Cmd:       Command{"true"},
					Arguments: map[string]Argument{RunIDPlaceholder: {}},
				},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateConfig(tc.cfg); err == nil || !strings.Contains(err.Error(), RunIDPlaceholder) {
				t.Fatalf("ValidateConfig() error = %v, want reserved run_id error", err)
			}
		})
	}
}

func TestResolvePreservesPostStageStatusPlaceholders(t *testing.T) {
	got, err := Resolve("test", Recipe{
		Cmd:  Command{"true"},
		Post: stageCommands(ScriptCommand(`printf '%s\n' "{status:pre}" "{status:cmd}"`)),
	}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	body := ScriptBody(got.Recipe.Post[0].Cmd)
	if !strings.Contains(body, "{status:pre}") || !strings.Contains(body, "{status:cmd}") {
		t.Fatalf("post body = %q, want status placeholders preserved", body)
	}
}

func TestResolvePreservesCmdPreStatusPlaceholder(t *testing.T) {
	got, err := Resolve("test", Recipe{
		Cmd: ScriptCommand(`printf '%s\n' "{status:pre}"`),
	}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if body := ScriptBody(got.Main); !strings.Contains(body, "{status:pre}") {
		t.Fatalf("cmd body = %q, want pre status placeholder preserved", body)
	}
}

func TestResolveRejectsUnavailableStageStatusPlaceholders(t *testing.T) {
	tests := []struct {
		name string
		rec  Recipe
		want string
	}{
		{
			name: "cmd status in cmd",
			rec:  Recipe{Cmd: ScriptCommand(`printf '%s\n' "{status:cmd}"`)},
			want: "{status:cmd} is supported only in post",
		},
		{
			name: "cmd status in pre",
			rec: Recipe{
				Pre: stageCommands(ScriptCommand(`printf '%s\n' "{status:cmd}"`)),
				Cmd: Command{"true"},
			},
			want: "{status:cmd} is supported only in post",
		},
		{
			name: "pre status in pre",
			rec: Recipe{
				Pre: stageCommands(ScriptCommand(`printf '%s\n' "{status:pre}"`)),
				Cmd: Command{"true"},
			},
			want: "{status:pre} is supported only in cmd or post",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve("test", tt.rec, nil, nil, nil, "", "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Resolve() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestResolveRejectsInvalidPostStageStatusPlaceholder(t *testing.T) {
	_, err := Resolve("test", Recipe{
		Cmd:  Command{"true"},
		Post: stageCommands(ScriptCommand(`printf '%s\n' "{status:post}"`)),
	}, nil, nil, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), "{status:post} supports only pre or cmd") {
		t.Fatalf("Resolve() error = %v, want invalid status placeholder", err)
	}
}

func TestValidateConfigRejectsInvalidLogSettings(t *testing.T) {
	cases := []struct {
		name     string
		rec      Recipe
		want     string
		wantPath []string
	}{
		{
			name:     "log stages without log",
			rec:      Recipe{Cmd: Command{"true"}, LogStages: []string{LogStageCmd}},
			want:     "log_stages requires log",
			wantPath: []string{"recipes", "test", "log_stages"},
		},
		{
			name:     "log tee without log",
			rec:      Recipe{Cmd: Command{"true"}, LogTee: new(false)},
			want:     "log_tee requires log",
			wantPath: []string{"recipes", "test", "log_tee"},
		},
		{
			name:     "bad stage",
			rec:      Recipe{Cmd: Command{"true"}, Log: "run.log", LogStages: []string{"cleanup"}},
			want:     `unsupported stage "cleanup"`,
			wantPath: []string{"recipes", "test", "log_stages"},
		},
		{
			name:     "empty stages",
			rec:      Recipe{Cmd: Command{"true"}, Log: "run.log", LogStages: []string{}},
			want:     "log_stages must not be empty",
			wantPath: []string{"recipes", "test", "log_stages"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(Config{Recipes: map[string]Recipe{"test": tc.rec}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateConfig() error = %v, want %q", err, tc.want)
			}
			pathErr, ok := errors.AsType[*ConfigPathError](err)
			if !ok {
				t.Fatalf("ValidateConfig() error = %T %[1]v, want ConfigPathError", err)
			}
			if got := pathErr.ConfigPath(); !slices.Equal(got, tc.wantPath) {
				t.Fatalf("ConfigPath() = %#v, want %#v", got, tc.wantPath)
			}
			if got := pathErr.Target(); got != ConfigErrorTargetValue {
				t.Fatalf("Target() = %q, want value", got)
			}
		})
	}
}

func TestValidateConfigStageErrorsIncludePath(t *testing.T) {
	cases := []struct {
		name     string
		rec      Recipe
		want     string
		wantPath []string
	}{
		{
			name:     "pre variadic args placeholder",
			rec:      Recipe{Cmd: Command{"true"}, Pre: stageCommands(ScriptCommand("echo {@}"))},
			want:     "pre[0]: {@} is supported only in cmd",
			wantPath: []string{"recipes", "test", "pre", "0", "cmd"},
		},
		{
			name:     "post variadic args placeholder",
			rec:      Recipe{Cmd: Command{"true"}, Post: stageCommands(ScriptCommand("echo {@}"))},
			want:     "post[0]: {@} is supported only in cmd",
			wantPath: []string{"recipes", "test", "post", "0", "cmd"},
		},
		{
			name: "pre timeout",
			rec: Recipe{
				Cmd: Command{"true"},
				Pre: StageCommands{{
					Cmd:     ScriptCommand("echo pre"),
					Timeout: "0s",
				}},
			},
			want:     "pre[0]: timeout must be greater than zero",
			wantPath: []string{"recipes", "test", "pre", "0", "timeout"},
		},
		{
			name: "post timeout",
			rec: Recipe{
				Cmd: Command{"true"},
				Post: StageCommands{{
					Cmd:     ScriptCommand("echo post"),
					Timeout: "0s",
				}},
			},
			want:     "post[0]: timeout must be greater than zero",
			wantPath: []string{"recipes", "test", "post", "0", "timeout"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(Config{Recipes: map[string]Recipe{"test": tc.rec}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateConfig() error = %v, want %q", err, tc.want)
			}
			pathErr, ok := errors.AsType[*ConfigPathError](err)
			if !ok {
				t.Fatalf("ValidateConfig() error = %T %[1]v, want ConfigPathError", err)
			}
			if got := pathErr.ConfigPath(); !slices.Equal(got, tc.wantPath) {
				t.Fatalf("ConfigPath() = %#v, want %#v", got, tc.wantPath)
			}
			if got := pathErr.Target(); got != ConfigErrorTargetValue {
				t.Fatalf("Target() = %q, want value", got)
			}
		})
	}
}

func TestEvalVarCommandsUsesConfiguredShellAndPrelude(t *testing.T) {
	got, err := EvalVarCommands(
		t.Context(),
		"",
		nil,
		map[string]Command{"value": ScriptCommand("print_value")},
		"bash",
		"print_value() { [[ -n ${BASH_VERSION:-} ]] && printf ' ok\\n'; }",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got["value"] != "ok" {
		t.Fatalf("value = %q", got["value"])
	}
}

func TestResolveTypedArgumentsValidatesTypes(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"echo", "{count}", "{enabled}", "{@}"},
		Arguments: map[string]Argument{
			"count":   {Type: "int", Required: true},
			"enabled": {Type: "bool", Default: true},
		},
	}

	if _, err := Resolve("typed", rec, []string{"count=abc"}, nil, nil, "", ""); err == nil {
		t.Fatal("Resolve succeeded with invalid int")
	}
	got, err := Resolve("typed", rec, []string{"count=3", "enabled=false"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"echo", "3", "false"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveArgumentRanges(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"bench", "{workers}", "{ratio}", "{duration}", "{timeout}"},
		Arguments: map[string]Argument{
			"workers":  {Type: "int", Min: 1, Max: 8, Default: 4},
			"ratio":    {Type: "float", Min: 0.25, Max: 2.5, Default: 1.5},
			"duration": {Type: "duration", Min: "100ms", Max: "10s", Default: "1s"},
			"timeout":  {Type: "duration:seconds", Min: "1s", Max: "1m", Default: "30s"},
		},
	}

	got, err := Resolve("bench", rec, []string{
		"workers=8",
		"ratio=0.5",
		"duration=1500ms",
		"timeout=45s",
	}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"bench", "8", "0.5", "1500ms", "45"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveArgumentRangesRejectOutOfRangeValues(t *testing.T) {
	cases := []struct {
		name string
		arg  Argument
		raw  string
		want string
	}{
		{name: "int below min", arg: Argument{Type: "int", Min: 1}, raw: "0", want: `workers: want >= 1, got "0"`},
		{name: "int above max", arg: Argument{Type: "int", Max: 8}, raw: "9", want: `workers: want <= 8, got "9"`},
		{name: "float below min", arg: Argument{Type: "float", Min: 0.25}, raw: "0.1", want: `workers: want >= 0.25, got "0.1"`},
		{name: "float above max", arg: Argument{Type: "float", Max: 2.5}, raw: "3", want: `workers: want <= 2.5, got "3"`},
		{name: "float nan", arg: Argument{Type: "float", Min: 0, Max: 1}, raw: "NaN", want: `workers: want float, got "NaN"`},
		{name: "duration below min", arg: Argument{Type: "duration", Min: "1s"}, raw: "500ms", want: `workers: want >= 1s, got "500ms"`},
		{name: "duration above max", arg: Argument{Type: "duration:seconds", Max: "1m"}, raw: "90s", want: `workers: want <= 1m, got "90s"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := Recipe{
				Cmd:       Command{"bench", "{workers}"},
				Arguments: map[string]Argument{"workers": tc.arg},
			}
			_, err := Resolve("bench", rec, []string{"workers=" + tc.raw}, nil, nil, "", "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Resolve() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestResolveArgumentValuesRejectInvalidEnumValues(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"deploy", "{target}", "{stage}"},
		Arguments: map[string]Argument{
			"target": {Position: 1, Required: true, Values: ScriptCommand("@enum api worker")},
			"stage":  {Required: true, Values: ScriptCommand("@enum dev prod")},
		},
	}

	if _, err := Resolve("deploy", rec, []string{"api", "stage=prod"}, nil, nil, "", ""); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve("deploy", rec, []string{"docs", "stage=prod"}, nil, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), `target: invalid value "docs"`) {
		t.Fatalf("Resolve() positional error = %v, want invalid target", err)
	}
	_, err = Resolve("deploy", rec, []string{"api", "stage=qa"}, nil, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), `stage: invalid value "qa"`) {
		t.Fatalf("Resolve() named error = %v, want invalid stage", err)
	}
	_, err = Resolve("deploy", rec, []string{"api", "stage="}, nil, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), `stage: invalid value ""`) {
		t.Fatalf("Resolve() empty error = %v, want invalid stage", err)
	}
}

func TestResolveArgumentValuesRejectInvalidDefaultValues(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"deploy", "{target}"},
		Arguments: map[string]Argument{
			"target": {Default: "docs", Values: ScriptCommand("@enum api worker")},
		},
	}

	_, err := Resolve("deploy", rec, nil, nil, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), `target default: target: invalid value "docs"`) {
		t.Fatalf("Resolve() error = %v, want invalid default", err)
	}
}

func TestResolveArgumentValuesWarnsForOverriddenInvalidDefault(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"deploy", "{target}"},
		Arguments: map[string]Argument{
			"target": {Default: "", Values: ScriptCommand("@enum api worker")},
		},
	}

	resolved, err := Resolve("deploy", rec, []string{"target=api"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolved.Warnings, []string{`target default ignored: target: invalid value ""`}; !slices.Equal(got, want) {
		t.Fatalf("Warnings = %q, want %q", got, want)
	}
}

func TestResolveArgumentValuesRejectInvalidRecipeValues(t *testing.T) {
	rec := Recipe{
		Cmd:       Command{"shadowtree", "{target}"},
		Arguments: map[string]Argument{"target": {Values: ScriptCommand("@recipes")}},
	}
	recipes := map[string]Recipe{
		"build": rec,
		"test":  {Cmd: Command{"go", "test"}},
	}

	if _, err := ResolveWithOptions("build", rec, []string{"target=test"}, nil, nil, "", "", ResolveOptions{Recipes: recipes}); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveWithOptions("build", rec, []string{"target=deploy"}, nil, nil, "", "", ResolveOptions{Recipes: recipes})
	if err == nil || !strings.Contains(err.Error(), `target: invalid value "deploy"`) {
		t.Fatalf("ResolveWithOptions() error = %v, want invalid target", err)
	}
}

func TestResolveArgumentValuesAcceptVarsDefaultValues(t *testing.T) {
	rec := Recipe{
		Cmd:  Command{"render", "{name}"},
		Vars: map[string]string{"component": "api"},
		Arguments: map[string]Argument{
			"name": {Default: "component", Values: ScriptCommand("@vars")},
		},
	}

	got, err := Resolve("render", rec, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"render", "component"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveDurationArguments(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"bench", "-duration", "{duration}", "-timeout", "{timeout}", "{@}"},
		Arguments: map[string]Argument{
			"duration": {Type: "duration", Default: "10s"},
			"timeout":  {Type: "duration:seconds", Default: "1m30s"},
		},
	}

	got, err := Resolve("bench", rec, []string{"duration=1500ms", "timeout=2h"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"bench", "-duration", "1500ms", "-timeout", "7200"}) {
		t.Fatalf("Main = %#v", got.Main)
	}

	got, err = Resolve("bench", rec, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"bench", "-duration", "10s", "-timeout", "90"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveDurationPresetArguments(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"bench", "{duration}", "{timeout}"},
		Arguments: map[string]Argument{
			"duration": {Type: "duration", Default: "10s", Min: "1s", Max: "2m"},
			"timeout":  {Type: "duration:seconds", Default: "30s", Min: "1s", Max: "3m"},
		},
		Presets: map[string]RecipePreset{
			"stable": {
				Arguments: map[string]any{
					"duration": "1m",
					"timeout":  "2m",
				},
			},
		},
	}

	got, err := Resolve("bench", rec, []string{"preset=stable"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"bench", "1m", "120"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestResolveDurationArgumentsRejectInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		arg  Argument
		raw  string
		want string
	}{
		{name: "missing unit", arg: Argument{Type: "duration"}, raw: "10", want: `want duration, got "10"`},
		{name: "invalid text", arg: Argument{Type: "duration"}, raw: "soon", want: `want duration, got "soon"`},
		{name: "fractional seconds", arg: Argument{Type: "duration:seconds"}, raw: "1500ms", want: `want whole-second duration, got "1500ms"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := Recipe{
				Cmd:       Command{"bench", "{timeout}"},
				Arguments: map[string]Argument{"timeout": tc.arg},
			}
			_, err := Resolve("bench", rec, []string{"timeout=" + tc.raw}, nil, nil, "", "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Resolve() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestResolvePathArgumentsValidatesRelativePath(t *testing.T) {
	rec := Recipe{
		Cmd: Command{"echo", "{source}", "{target}", "{@}"},
		Arguments: map[string]Argument{
			"source": {Type: "path", Required: true},
			"target": {Type: "rel_path", Required: true},
		},
	}

	got, err := Resolve("copy", rec, []string{"source=/tmp/input", "target=cmd/shadowtree"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"echo", "/tmp/input", "cmd/shadowtree"}) {
		t.Fatalf("Main = %#v", got.Main)
	}
	if _, err := Resolve("copy", rec, []string{"source=cmd", "target=/tmp/output"}, nil, nil, "", ""); err == nil {
		t.Fatal("Resolve succeeded with absolute rel_path")
	}
	if _, err := Resolve("copy", rec, []string{"source=cmd", "target=~/output"}, nil, nil, "", ""); err == nil {
		t.Fatal("Resolve succeeded with home rel_path")
	}
}

func TestResolvePathArgumentExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	rec := Recipe{
		Cmd: Command{"echo", "{source}", "{@}"},
		Arguments: map[string]Argument{
			"source": {Type: "path", Required: true},
		},
	}

	got, err := Resolve("open", rec, []string{"source=~/project"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"echo", filepath.Join(home, "project")}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestValidateArgumentsValidatesPathKind(t *testing.T) {
	if err := ValidateArguments(map[string]Argument{
		"target": {Type: "path", PathKind: "file"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateArguments(map[string]Argument{
		"target": {Type: "string", PathKind: "file"},
	}); err == nil {
		t.Fatal("ValidateArguments succeeded with path_kind on string argument")
	}
	if err := ValidateArguments(map[string]Argument{
		"target": {Type: "path", PathKind: "socket"},
	}); err == nil {
		t.Fatal("ValidateArguments succeeded with invalid path_kind")
	}
}

func TestValidateArgumentsValidatesRanges(t *testing.T) {
	cases := []struct {
		name string
		arg  Argument
		want string
	}{
		{name: "unsupported type", arg: Argument{Type: "string", Min: 1}, want: "target: min requires type int, float, duration, or duration:seconds"},
		{name: "invalid int min", arg: Argument{Type: "int", Min: "one"}, want: `target min: want int, got "one"`},
		{name: "invalid float min", arg: Argument{Type: "float", Min: "NaN"}, want: `target min: want float, got "NaN"`},
		{name: "invalid duration max", arg: Argument{Type: "duration", Max: "1000"}, want: `target max: want duration, got "1000"`},
		{name: "fractional seconds bound", arg: Argument{Type: "duration:seconds", Min: "1500ms"}, want: `target min: want whole-second duration, got "1500ms"`},
		{name: "max below min", arg: Argument{Type: "int", Min: 10, Max: 1}, want: "target: max must be greater than or equal to min"},
		{name: "default below min", arg: Argument{Type: "int", Min: 1, Default: 0}, want: `target default: target: want >= 1, got "0"`},
		{name: "default nan", arg: Argument{Type: "float", Min: 0, Max: 1, Default: math.NaN()}, want: `target default: target: want float, got "NaN"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateArguments(map[string]Argument{"target": tc.arg})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateArguments() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateArgumentsRejectsArgvValues(t *testing.T) {
	err := ValidateArguments(map[string]Argument{
		"target": {Values: Command{"@enum", "api"}},
	})
	if err == nil || !strings.Contains(err.Error(), "target values: values must be a shell string") {
		t.Fatalf("ValidateArguments() error = %v, want shell string error", err)
	}
}

func TestValidateArgumentsRejectsEmptyArgvValues(t *testing.T) {
	err := ValidateArguments(map[string]Argument{
		"target": {Values: Command{}},
	})
	if err == nil || !strings.Contains(err.Error(), "target values: values must be a shell string") {
		t.Fatalf("ValidateArguments() error = %v, want shell string error", err)
	}
}

func TestValidateConfigErrorIncludesPath(t *testing.T) {
	err := ValidateConfig(Config{Recipes: map[string]Recipe{
		"benchmark": {
			Cmd: Command{"bench"},
			Arguments: map[string]Argument{
				"duration": {Type: "duration", Default: "1000"},
			},
		},
	}})
	var pathErr *ConfigPathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("ValidateConfig() error = %T %[1]v, want ConfigPathError", err)
	}
	if !slices.Equal(pathErr.ConfigPath(), []string{"recipes", "benchmark", "arguments", "duration", "default"}) {
		t.Fatalf("path = %#v", pathErr.ConfigPath())
	}
	if pathErr.Target() != ConfigErrorTargetValue {
		t.Fatalf("target = %q, want value", pathErr.Target())
	}
	if got := pathErr.Error(); got != `recipe "benchmark" arguments: duration default: duration: want duration, got "1000"` {
		t.Fatalf("Error() = %q", got)
	}
	if unwrapped := pathErr.Unwrap(); unwrapped == nil || unwrapped.Error() != `recipe "benchmark" arguments: duration default: duration: want duration, got "1000"` {
		t.Fatalf("Unwrap() = %v", unwrapped)
	}

	path := pathErr.ConfigPath()
	path[0] = "mutated"
	if pathErr.ConfigPath()[0] != "recipes" {
		t.Fatalf("ConfigPath returned mutable internal slice: %#v", pathErr.ConfigPath())
	}
}

func TestValidateConfigPresetErrorsIncludeKeyAndTableTargets(t *testing.T) {
	err := ValidateConfig(Config{Recipes: map[string]Recipe{
		"benchmark": {
			Cmd:       Command{"bench"},
			Arguments: map[string]Argument{"connections": {Type: "int"}},
			Presets: map[string]RecipePreset{
				"stable": {Arguments: map[string]any{"requests": 20000}},
			},
		},
	}})
	var pathErr *ConfigPathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("ValidateConfig() error = %T %[1]v, want ConfigPathError", err)
	}
	want := []string{"recipes", "benchmark", "presets", "stable", "arguments", "requests"}
	if !slices.Equal(pathErr.ConfigPath(), want) {
		t.Fatalf("path = %#v, want %#v", pathErr.ConfigPath(), want)
	}
	if pathErr.Target() != ConfigErrorTargetKey {
		t.Fatalf("target = %q, want key", pathErr.Target())
	}

	err = ValidateConfig(Config{Recipes: map[string]Recipe{
		"benchmark": {
			Cmd:       Command{"bench"},
			Arguments: map[string]Argument{PresetArgumentName: {Type: "string"}},
			Presets:   map[string]RecipePreset{"stable": {}},
		},
	}})
	if !errors.As(err, &pathErr) {
		t.Fatalf("ValidateConfig() error = %T %[1]v, want ConfigPathError", err)
	}
	want = []string{"recipes", "benchmark", "arguments", PresetArgumentName}
	if !slices.Equal(pathErr.ConfigPath(), want) {
		t.Fatalf("path = %#v, want %#v", pathErr.ConfigPath(), want)
	}
	if pathErr.Target() != ConfigErrorTargetTable {
		t.Fatalf("target = %q, want table", pathErr.Target())
	}
}

func TestValidateConfigRejectsBuiltinReferenceRecipeName(t *testing.T) {
	for _, name := range BuiltinReferenceNames() {
		err := ValidateConfig(Config{
			Recipes: map[string]Recipe{
				name: {Cmd: Command{"go"}},
			},
		})
		if err == nil {
			t.Fatalf("ValidateConfig succeeded with builtin reference recipe name %q", name)
		}
	}
}

func TestValidateConfigRejectsUnsupportedProfile(t *testing.T) {
	err := ValidateConfig(Config{Profile: "python"})
	if err == nil || err.Error() != "unsupported profile: python" {
		t.Fatalf("ValidateConfig() error = %v, want unsupported profile error", err)
	}
	pathErr, ok := errors.AsType[*ConfigPathError](err)
	if !ok {
		t.Fatalf("ValidateConfig() error = %T %[1]v, want ConfigPathError", err)
	}
	if got := pathErr.ConfigPath(); !slices.Equal(got, []string{"profile"}) {
		t.Fatalf("ConfigPath() = %#v, want profile", got)
	}
	if got := pathErr.Target(); got != ConfigErrorTargetValue {
		t.Fatalf("Target() = %q, want value", got)
	}
}

func TestRustBuiltinsProvideCargoCheck(t *testing.T) {
	if !SupportsProfile(RustProfile) {
		t.Fatalf("SupportsProfile(%q) = false", RustProfile)
	}
	rec, ok := Builtins(RustProfile, BuiltinOptions{})["check"]
	if !ok {
		t.Fatal("Rust built-ins missing check")
	}
	if !slices.Equal(rec.Cmd, Command{"cargo", "+1.96.0", "check", "{@}"}) {
		t.Fatalf("check command = %#v, want cargo check with trailing arguments", rec.Cmd)
	}
}

func TestRustBuiltinsProvideHostWorkflowAndExplicitWorkspaceAggregate(t *testing.T) {
	recipes := Builtins(RustProfile, BuiltinOptions{})
	for name, command := range map[string]Command{
		"check":  {"cargo", "+1.96.0", "check", "{@}"},
		"test":   {"cargo", "+1.96.0", "test", "{@}"},
		"build":  {"cargo", "+1.96.0", "build", "{@}"},
		"run":    {"cargo", "+1.96.0", "run", "{@}"},
		"fmt":    {"cargo", "+1.96.0", "fmt", "{@}"},
		"clippy": {"cargo", "+1.96.0", "clippy", "{@}"},
	} {
		rec, ok := recipes[name]
		if !ok {
			t.Fatalf("Rust built-ins missing %s", name)
		}
		if !slices.Equal(rec.Cmd, command) {
			t.Fatalf("%s command = %#v, want %#v", name, rec.Cmd, command)
		}
	}

	all, domain, source, err := SelectAll("check", recipes["check"])
	if err != nil {
		t.Fatal(err)
	}
	if domain != "workspace" || source != RustWorkspaceTargets {
		t.Fatalf("check --all = domain %q source %q", domain, source)
	}
	if !slices.Equal(all.Cmd, Command{"cargo", "+1.96.0", "check", "--workspace", "{@}"}) {
		t.Fatalf("check --all command = %#v", all.Cmd)
	}
	if _, _, _, err := SelectAll("run", recipes["run"]); err == nil || !strings.Contains(err.Error(), "multiple binaries") {
		t.Fatalf("run --all error = %v, want explicit multiple-binary guidance", err)
	}
	for _, name := range []string{"fmt", "clippy"} {
		body := ScriptBody(recipes[name].Pre[0].Cmd)
		if !strings.Contains(body, "install the ") || !strings.Contains(body, "component") {
			t.Fatalf("%s component diagnostic = %q, want install guidance", name, body)
		}
	}
}

func TestResolveRustProjectOwnsWorkspaceToolchainAndCacheContract(t *testing.T) {
	root := t.TempDir()
	member := filepath.Join(root, "crates", "app")
	mkdirAll(t, member)
	mkdirAll(t, filepath.Join(root, ".cargo"))
	writeFile(t, filepath.Join(root, "Cargo.toml"), "[workspace]\nmembers = [\"crates/app\"]\n")
	writeFile(t, filepath.Join(root, "Cargo.lock"), "# lock\n")
	writeFile(t, filepath.Join(root, RustToolchainTOML), "[toolchain]\nchannel = \"1.96.0-x86_64-unknown-linux-gnu\"\n")
	writeFile(t, filepath.Join(root, ".cargo", "config.toml"), "[build]\ntarget = \"wasm32-wasip2\"\n")
	writeFile(t, filepath.Join(member, "Cargo.toml"), "[package]\nname = \"app\"\nversion = \"0.1.0\"\n")

	metadata := cargoMetadata{WorkspaceRoot: root, TargetDirectory: filepath.Join(root, "target")}
	metadata.Packages = append(metadata.Packages, struct {
		ManifestPath string `json:"manifest_path"`
	}{ManifestPath: filepath.Join(member, "Cargo.toml")})
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "rustc"), "#!/bin/sh\nprintf '%s\\n' 'release: 1.96.0' 'commit-hash: abc123' 'host: x86_64-unknown-linux-gnu'\n")
	writeExecutable(t, filepath.Join(bin, "cargo"), "#!/bin/sh\nprintf '%s\\n' "+shellQuote(string(metadataJSON))+"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cargoHome := filepath.Join(root, ".cargo-home")
	env := append(os.Environ(), "CARGO_HOME="+cargoHome)

	project, err := ResolveRustProject(t.Context(), member, env, []string{"--target=thumbv7em-none-eabihf"})
	if err != nil {
		t.Fatal(err)
	}
	if project.WorkspaceRoot != root || project.RootManifest != filepath.Join(root, "Cargo.toml") {
		t.Fatalf("workspace = %#v, want root %q", project, root)
	}
	if !slices.Equal(project.MemberManifests, []string{filepath.Join(member, "Cargo.toml")}) {
		t.Fatalf("member manifests = %#v", project.MemberManifests)
	}
	if project.Toolchain != "1.96.0-x86_64-unknown-linux-gnu" || project.ToolchainProvenance != filepath.Join(root, RustToolchainTOML) {
		t.Fatalf("toolchain = %q from %q", project.Toolchain, project.ToolchainProvenance)
	}
	if project.CompilerCommit != "abc123" {
		t.Fatalf("compiler commit = %q, want exact rustc commit", project.CompilerCommit)
	}
	if project.HostTriple != "x86_64-unknown-linux-gnu" || project.TargetTriple != "thumbv7em-none-eabihf" {
		t.Fatalf("host/target = %q/%q", project.HostTriple, project.TargetTriple)
	}
	if !project.LockedPreparation || project.Lockfile != filepath.Join(root, "Cargo.lock") {
		t.Fatalf("locked preparation = %t lockfile %q", project.LockedPreparation, project.Lockfile)
	}
	if project.RegistryCache != filepath.Join(cargoHome, "registry") || project.GitCache != filepath.Join(cargoHome, "git") || project.TargetDir != filepath.Join(root, "target") {
		t.Fatalf("cache destinations = registry %q git %q target %q", project.RegistryCache, project.GitCache, project.TargetDir)
	}
	if project.ProjectCacheKey == "" || project.TargetCacheConcurrency != RustTargetCacheConcurrency {
		t.Fatalf("cache contract = key %q concurrency %q", project.ProjectCacheKey, project.TargetCacheConcurrency)
	}
	if !slices.Contains(project.CacheCompatibility, "thumbv7em-none-eabihf") {
		t.Fatalf("cache compatibility = %#v, want effective Cargo target", project.CacheCompatibility)
	}
	if !slices.Equal(project.FetchCommand, Command{"cargo", "+1.96.0-x86_64-unknown-linux-gnu", "fetch", "--locked", "--manifest-path", filepath.Join(root, "Cargo.toml")}) {
		t.Fatalf("fetch command = %#v", project.FetchCommand)
	}
}

func TestRustTargetArgumentPrecedenceAndValidation(t *testing.T) {
	for name, tt := range map[string]struct {
		args    []string
		want    string
		found   bool
		wantErr string
	}{
		"equals":               {args: []string{"--target=wasm32-wasip2"}, want: "wasm32-wasip2", found: true},
		"separate":             {args: []string{"--target", "thumbv7em-none-eabihf"}, want: "thumbv7em-none-eabihf", found: true},
		"last wins":            {args: []string{"--target=a", "--target", "b"}, want: "b", found: true},
		"passthrough boundary": {args: []string{"--", "--target=ignored"}},
		"empty equals":         {args: []string{"--target="}, wantErr: "non-empty"},
		"missing separate":     {args: []string{"--target"}, wantErr: "requires a value"},
		"flag as value":        {args: []string{"--target", "--release"}, wantErr: "requires a value"},
	} {
		t.Run(name, func(t *testing.T) {
			got, found, err := rustTargetArgument(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want || found != tt.found {
				t.Fatalf("rustTargetArgument() = %q, %t, %v; want %q, %t", got, found, err, tt.want, tt.found)
			}
		})
	}
}

func TestResolveRustProjectRejectsMalformedAndUnknownToolchains(t *testing.T) {
	for name, tt := range map[string]struct {
		file    string
		content string
		want    string
	}{
		"malformed TOML":         {file: RustToolchainTOML, content: "[toolchain\n", want: "parse"},
		"ambiguous stable":       {file: RustToolchainTOML, content: "[toolchain]\nchannel = \"stable\"\n", want: `"stable"`},
		"unknown future channel": {file: RustToolchainFile, content: "future\n", want: `"future"`},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"app\"\nversion = \"0.1.0\"\n")
			writeFile(t, filepath.Join(dir, tt.file), tt.content)
			_, err := ResolveRustProject(t.Context(), dir, os.Environ(), nil)
			if err == nil || !strings.Contains(err.Error(), filepath.Join(dir, tt.file)) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ResolveRustProject() error = %v, want path and %q", err, tt.want)
			}
		})
	}
}

func TestRustToolchainWithinDoesNotUseParentProjectOrToolchain(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	workdir := filepath.Join(project, "crate")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(parent, "Cargo.toml"), "[package]\nname = \"parent\"\nversion = \"0.1.0\"\n")
	writeFile(t, filepath.Join(parent, RustToolchainFile), "1.95.0\n")
	if _, err := RustToolchainWithin(workdir, project); err == nil || !strings.Contains(err.Error(), "canonical project") {
		t.Fatalf("RustToolchainWithin error = %v, want in-project Cargo marker rejection", err)
	}
	writeFile(t, filepath.Join(project, "Cargo.toml"), "[package]\nname = \"project\"\nversion = \"0.1.0\"\n")
	got, err := RustToolchainWithin(workdir, project)
	if err != nil {
		t.Fatal(err)
	}
	if got != DefaultRustToolchain {
		t.Fatalf("RustToolchainWithin = %q, want default %q instead of parent toolchain", got, DefaultRustToolchain)
	}
}

func TestRustDependencyTargetPathsOwnsSafeManifestPlaceholders(t *testing.T) {
	paths, err := RustDependencyTargetPaths(map[string][]byte{
		"Cargo.toml":       []byte("[workspace]\nmembers = [\"crate\"]\n"),
		"crate/Cargo.toml": []byte("[package]\nname = \"crate\"\nversion = \"0.1.0\"\n[lib]\npath = \"source/lib.rs\"\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"crate/src/lib.rs", "crate/src/main.rs", "crate/source/lib.rs"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("target paths = %#v, missing %q", paths, want)
		}
	}
	_, err = RustDependencyTargetPaths(map[string][]byte{"Cargo.toml": []byte("[package]\nname = \"app\"\nversion = \"0.1.0\"\n[lib]\npath = \"../outside.rs\"\n")})
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("RustDependencyTargetPaths error = %v, want confinement rejection", err)
	}
}

func TestRustDependencyManifestPathsSelectsWorkspaceMembersAndPathDependencies(t *testing.T) {
	files := map[string][]byte{
		"Cargo.toml":            []byte("[workspace]\nmembers = [\"crates/*\"]\n"),
		"crates/app/Cargo.toml": []byte("[package]\nname = \"app\"\nversion = \"0.1.0\"\n[dependencies]\nlocal = { path = \"../../local\" }\n"),
		"local/Cargo.toml":      []byte("[package]\nname = \"local\"\nversion = \"0.1.0\"\n"),
		"unrelated/Cargo.toml":  []byte("[package]\nname = \"unrelated\"\nversion = \"0.1.0\"\n"),
	}
	got, err := RustDependencyManifestPaths(files, "crates/app")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Cargo.toml", "crates/app/Cargo.toml", "local/Cargo.toml"} {
		if !slices.Contains(got, want) {
			t.Fatalf("manifest paths = %#v, missing %q", got, want)
		}
	}
	if slices.Contains(got, "unrelated/Cargo.toml") {
		t.Fatalf("manifest paths include unrelated project: %#v", got)
	}
}

func TestResolveRustProjectPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := ResolveRustProject(ctx, t.TempDir(), os.Environ(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestResolveRustWorkspaceTargetUsesRunnerRelativeWorkdir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Cargo.toml"), "[package]\nname = \"app\"\nversion = \"0.1.0\"\n")
	metadata := cargoMetadata{WorkspaceRoot: root, TargetDirectory: filepath.Join(root, "target")}
	metadata.Packages = append(metadata.Packages, struct {
		ManifestPath string `json:"manifest_path"`
	}{ManifestPath: filepath.Join(root, "Cargo.toml")})
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "rustc"), "#!/bin/sh\nprintf '%s\\n' 'release: 1.96.0' 'commit-hash: abc123' 'host: x86_64-unknown-linux-gnu'\n")
	writeExecutable(t, filepath.Join(bin, "cargo"), "#!/bin/sh\nprintf '%s\\n' "+shellQuote(string(metadataJSON))+"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	targets, err := ResolveExecutionTargets(t.Context(), RustWorkspaceTargets, root, os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Workdir != "." || !strings.Contains(targets[0].Label, root) {
		t.Fatalf("targets = %#v, want one runner-relative Cargo workspace", targets)
	}
}

func TestValidateConfigRejectsUnsupportedGlobalShell(t *testing.T) {
	err := ValidateConfig(Config{Shell: "fish"})
	if err == nil || !strings.Contains(err.Error(), `shell must be sh or bash, got "fish"`) {
		t.Fatalf("ValidateConfig() error = %v, want unsupported shell error", err)
	}
}

func TestValidateConfigRejectsUnsupportedRecipeShell(t *testing.T) {
	err := ValidateConfig(Config{Recipes: map[string]Recipe{
		"script": {
			Shell: "fish",
			Cmd:   ScriptCommand("true"),
		},
	}})
	if err == nil || !strings.Contains(err.Error(), `recipe "script" shell must be sh or bash, got "fish"`) {
		t.Fatalf("ValidateConfig() error = %v, want unsupported recipe shell error", err)
	}
}

func TestInvocationParsesBracketStyleArguments(t *testing.T) {
	name, args := Invocation([]string{"build[project=./cmd/shadowtree,binary=st]"})

	if name != "build" {
		t.Fatalf("name = %q", name)
	}
	if !slices.Equal(args, []string{"project=./cmd/shadowtree", "binary=st"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestNodeBuiltinsDetectPackageManager(t *testing.T) {
	tests := []struct {
		name        string
		packageJSON string
		lockfile    string
		want        string
	}{
		{name: "packageManager pnpm", packageJSON: `{"packageManager":"pnpm@9.0.0"}`, want: "pnpm install"},
		{name: "packageManager yarn", packageJSON: `{"packageManager":"yarn@4.0.0"}`, want: "yarn install"},
		{name: "packageManager bun", packageJSON: `{"packageManager":"bun@1.1.0"}`, want: "bun install"},
		{name: "pnpm lockfile", packageJSON: `{}`, lockfile: "pnpm-lock.yaml", want: "pnpm install"},
		{name: "default npm", packageJSON: `{}`, want: "npm install"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeNodePackage(t, dir, tt.packageJSON)
			if tt.lockfile != "" {
				if err := os.WriteFile(filepath.Join(dir, tt.lockfile), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			rec := Builtins(NodeProfile, BuiltinOptions{Dir: dir})["install"]

			if body := ScriptBody(rec.Cmd); !strings.Contains(body, tt.want) {
				t.Fatalf("install body = %q, want %q", body, tt.want)
			}
		})
	}
}

func TestNodeInstallCommandUsesDetectedPackageManager(t *testing.T) {
	tests := []struct {
		name        string
		packageJSON string
		lockfile    string
		want        string
	}{
		{name: "packageManager pnpm", packageJSON: `{"packageManager":"pnpm@9.0.0"}`, want: "pnpm add --global eslint@^9"},
		{name: "packageManager yarn", packageJSON: `{"packageManager":"yarn@4.0.0"}`, want: "yarn global add eslint@^9"},
		{name: "packageManager bun", packageJSON: `{"packageManager":"bun@1.1.0"}`, want: "bun add --global eslint@^9"},
		{name: "pnpm lockfile", packageJSON: `{}`, lockfile: "pnpm-lock.yaml", want: "pnpm add --global eslint@^9"},
		{name: "default npm", packageJSON: `{}`, want: "npm install -g eslint@^9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeNodePackage(t, dir, tt.packageJSON)
			if tt.lockfile != "" {
				if err := os.WriteFile(filepath.Join(dir, tt.lockfile), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			pm := NodePackageManager(dir)
			if got := NodeInstallCommandForPackageManager(pm, "eslint@^9"); got != tt.want {
				t.Fatalf("NodeInstallCommandForPackageManager(%q) = %q, want %q", pm, got, tt.want)
			}
		})
	}
}

func TestNodeBuiltinsDetectAncestorPackageManager(t *testing.T) {
	root := t.TempDir()
	writeNodePackage(t, root, `{"packageManager":"pnpm@9.0.0"}`)
	leaf := filepath.Join(root, "packages", "app")
	writeNodePackage(t, leaf, `{"scripts":{"test":"vitest"}}`)

	rec := Builtins(NodeProfile, BuiltinOptions{Dir: leaf})["test"]

	if body := ScriptBody(rec.Cmd); !strings.Contains(body, `pnpm run test -- {@}`) {
		t.Fatalf("test body = %q, want ancestor pnpm package manager", body)
	}
	if body := ScriptBody(rec.Cmd); !strings.HasPrefix(body, "cd "+shellQuote(leaf)+"\n") {
		t.Fatalf("test body = %q, want cd to nearest package dir %q", body, leaf)
	}
}

func TestNodeBuiltinsDetectAncestorLockfile(t *testing.T) {
	root := t.TempDir()
	writeNodePackage(t, root, `{}`)
	if err := os.WriteFile(filepath.Join(root, "yarn.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(root, "packages", "app")
	writeNodePackage(t, leaf, `{"scripts":{"test":"vitest"}}`)

	rec := Builtins(NodeProfile, BuiltinOptions{Dir: leaf})["test"]

	if body := ScriptBody(rec.Cmd); !strings.Contains(body, `yarn run test -- {@}`) {
		t.Fatalf("test body = %q, want ancestor yarn lockfile", body)
	}
}

func TestNodeBuiltinsRunFromNearestPackageDir(t *testing.T) {
	root := t.TempDir()
	writeNodePackage(t, root, `{"scripts":{"test":"vitest"}}`)
	subdir := filepath.Join(root, "src", "feature")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	rec := Builtins(NodeProfile, BuiltinOptions{Dir: subdir})["test"]

	if body := ScriptBody(rec.Cmd); !strings.HasPrefix(body, "cd "+shellQuote(root)+"\n") {
		t.Fatalf("test body = %q, want cd to package dir %q", body, root)
	}
}

func TestNodeBuiltinsAreUnsandboxed(t *testing.T) {
	dir := t.TempDir()
	writeNodePackage(t, dir, `{
		"scripts":{"dev":"vite","build":"vite build","start":"vite preview","test":"vitest","lint":"eslint .","fmt":"prettier --write .","typecheck":"tsc --noEmit","lint:fix":"eslint --fix ."}
	}`)

	for name, rec := range Builtins(NodeProfile, BuiltinOptions{Dir: dir}) {
		if rec.Sandboxed == nil || *rec.Sandboxed != SandboxModeHost {
			t.Fatalf("%s Sandboxed = %#v, want false", name, rec.Sandboxed)
		}
	}
}

func TestNodeBuiltinsInferFrameworkRecipes(t *testing.T) {
	dir := t.TempDir()
	writeNodePackage(t, dir, `{"dependencies":{"next":"latest"}}`)

	recipes := Builtins(NodeProfile, BuiltinOptions{Dir: dir})

	for name, want := range map[string]string{
		"dev":   "npm exec -- next dev",
		"build": "npm exec -- next build",
		"start": "npm exec -- next start",
	} {
		if body := ScriptBody(recipes[name].Cmd); !strings.Contains(body, want) {
			t.Fatalf("%s body = %q, want %q", name, body, want)
		}
	}
}

func TestNodeBuiltinsInferBunTest(t *testing.T) {
	dir := t.TempDir()
	writeNodePackage(t, dir, `{"packageManager":"bun@1.0.0"}`)

	rec := Builtins(NodeProfile, BuiltinOptions{Dir: dir})["test"]

	if body := ScriptBody(rec.Cmd); !strings.Contains(body, `bun test {@}`) {
		t.Fatalf("test body = %q, want bun test", body)
	}
}

func TestNodeBuiltinsPreferBunVitestWhenInstalled(t *testing.T) {
	dir := t.TempDir()
	writeNodePackage(t, dir, `{"packageManager":"bun@1.0.0","devDependencies":{"vitest":"latest"}}`)

	rec := Builtins(NodeProfile, BuiltinOptions{Dir: dir})["test"]

	if body := ScriptBody(rec.Cmd); !strings.Contains(body, `bunx vitest {@}`) {
		t.Fatalf("test body = %q, want bunx vitest", body)
	}
}

func TestNodeBuiltinsInferLintInOrder(t *testing.T) {
	tests := []struct {
		name        string
		packageJSON string
		files       []string
		want        string
	}{
		{name: "eslint dependency", packageJSON: `{"devDependencies":{"eslint":"latest","@biomejs/biome":"latest"}}`, want: "npm exec -- eslint ."},
		{name: "oxlint rc", packageJSON: `{}`, files: []string{".oxlintrc.jsonc", "biome.json"}, want: "npm exec -- oxlint"},
		{name: "biome", packageJSON: `{"devDependencies":{"@biomejs/biome":"latest"}}`, want: "npm exec -- biome lint ."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeNodePackage(t, dir, tt.packageJSON)
			for _, file := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, file), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			rec := Builtins(NodeProfile, BuiltinOptions{Dir: dir})["lint"]

			if body := ScriptBody(rec.Cmd); !strings.Contains(body, tt.want) {
				t.Fatalf("lint body = %q, want %q", body, tt.want)
			}
		})
	}
}

func TestNodeBuiltinsInferFmtInOrder(t *testing.T) {
	tests := []struct {
		name        string
		packageJSON string
		files       []string
		want        string
	}{
		{name: "prettier config", packageJSON: `{"devDependencies":{"@biomejs/biome":"latest"}}`, files: []string{".prettierrc.json"}, want: "npm exec -- prettier --write ."},
		{name: "oxfmt rc", packageJSON: `{}`, files: []string{".oxfmtrc.jsonc", "biome.json"}, want: "npm exec -- oxfmt"},
		{name: "biome", packageJSON: `{"devDependencies":{"@biomejs/biome":"latest"}}`, want: "npm exec -- biome format --write ."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeNodePackage(t, dir, tt.packageJSON)
			for _, file := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, file), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			rec := Builtins(NodeProfile, BuiltinOptions{Dir: dir})["fmt"]

			if body := ScriptBody(rec.Cmd); !strings.Contains(body, tt.want) {
				t.Fatalf("fmt body = %q, want %q", body, tt.want)
			}
		})
	}
}

func TestNodeBuiltinsTypecheckScriptWins(t *testing.T) {
	dir := t.TempDir()
	writeNodePackage(t, dir, `{"scripts":{"type-check":"vue-tsc --noEmit"},"devDependencies":{"typescript":"latest"}}`)

	rec := Builtins(NodeProfile, BuiltinOptions{Dir: dir})["typecheck"]

	if body := ScriptBody(rec.Cmd); !strings.Contains(body, `npm run type-check -- {@}`) {
		t.Fatalf("typecheck body = %q, want type-check script", body)
	}
}

func TestNodeBuiltinsComposeTypecheckTools(t *testing.T) {
	dir := t.TempDir()
	writeNodePackage(t, dir, `{"devDependencies":{"vue-tsc":"latest","svelte-check":"latest","typescript":"latest"}}`)

	rec := Builtins(NodeProfile, BuiltinOptions{Dir: dir})["typecheck"]
	body := ScriptBody(rec.Cmd)

	for _, want := range []string{
		"set -e",
		`npm exec -- vue-tsc --noEmit {@}`,
		`npm exec -- svelte-check {@}`,
		`npm exec -- tsc --noEmit {@}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("typecheck body = %q, missing %q", body, want)
		}
	}
	vueIndex := strings.Index(body, `npm exec -- vue-tsc --noEmit {@}`)
	svelteIndex := strings.Index(body, `npm exec -- svelte-check {@}`)
	tscIndex := strings.Index(body, `npm exec -- tsc --noEmit {@}`)
	if vueIndex > svelteIndex || svelteIndex > tscIndex {
		t.Fatalf("typecheck body order = %q", body)
	}
}

func TestNodeBuiltinsCheckComposesAvailableRecipes(t *testing.T) {
	dir := t.TempDir()
	writeNodePackage(t, dir, `{"scripts":{"test":"vitest"},"devDependencies":{"eslint":"latest","typescript":"latest"}}`)

	rec := Builtins(NodeProfile, BuiltinOptions{Dir: dir})["check"]

	if len(rec.Pre) != 2 || !slices.Equal(rec.Pre[0].Cmd, Command{"@lint"}) || !slices.Equal(rec.Pre[1].Cmd, Command{"@typecheck"}) {
		t.Fatalf("check pre = %#v, want lint then typecheck", rec.Pre)
	}
	if !slices.Equal(rec.Cmd, Command{"@test"}) {
		t.Fatalf("check cmd = %#v, want @test", rec.Cmd)
	}
}

func TestNodeBuiltinsPackageScriptsFillGaps(t *testing.T) {
	dir := t.TempDir()
	writeNodePackage(t, dir, `{"scripts":{"test":"custom test","lint:fix":"eslint --fix .","build":"custom build"},"devDependencies":{"vite":"latest"}}`)

	recipes := Builtins(NodeProfile, BuiltinOptions{Dir: dir})

	if body := ScriptBody(recipes["build"].Cmd); !strings.Contains(body, `npm run build -- {@}`) {
		t.Fatalf("build body = %q, want package script", body)
	}
	if body := ScriptBody(recipes["lint-fix"].Cmd); !strings.Contains(body, `npm run lint:fix -- {@}`) {
		t.Fatalf("lint-fix body = %q, want original script key", body)
	}
}

func TestNodeBuiltinsPackageScriptNormalizationCollision(t *testing.T) {
	dir := t.TempDir()
	writeNodePackage(t, dir, `{"scripts":{"lint:fix":"colon","lint-fix":"exact"}}`)

	rec := Builtins(NodeProfile, BuiltinOptions{Dir: dir})["lint-fix"]

	if body := ScriptBody(rec.Cmd); !strings.Contains(body, `npm run lint-fix -- {@}`) {
		t.Fatalf("lint-fix body = %q, want exact normalized script", body)
	}
}

func TestNodeBuiltinsPackageScriptNormalizationReplacesInvalidCharacters(t *testing.T) {
	dir := t.TempDir()
	writeNodePackage(t, dir, `{"scripts":{"pre view":"vite preview","lint---fix":"eslint --fix .","#foo":"node hash.js"}}`)

	rec := Builtins(NodeProfile, BuiltinOptions{Dir: dir})["pre-view"]

	if body := ScriptBody(rec.Cmd); !strings.Contains(body, `npm run 'pre view' -- {@}`) {
		t.Fatalf("pre-view body = %q, want original script key quoted", body)
	}
	rec = Builtins(NodeProfile, BuiltinOptions{Dir: dir})["lint-fix"]
	if body := ScriptBody(rec.Cmd); !strings.Contains(body, `npm run lint---fix -- {@}`) {
		t.Fatalf("lint-fix body = %q, want original repeated-dash script key", body)
	}
	rec = Builtins(NodeProfile, BuiltinOptions{Dir: dir})["foo"]
	if body := ScriptBody(rec.Cmd); !strings.Contains(body, `npm run '#foo' -- {@}`) {
		t.Fatalf("foo body = %q, want hash-prefixed script key quoted", body)
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	writeFile(t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeRootGoPackagesFixture(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n")
	mkdirAll(t, filepath.Join(dir, "internal", "lib"))
	writeFile(t, filepath.Join(dir, "internal", "lib", "lib.go"), "package lib\n")
	mkdirAll(t, filepath.Join(dir, "cmd", "api"))
	writeFile(t, filepath.Join(dir, "cmd", "api", "main.go"), "package main\n")
}

func go1264Builtins(t *testing.T) map[string]Recipe {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n\ngo 1.26.4\n")
	return Builtins(GoProfile, BuiltinOptions{Context: t.Context(), Dir: dir})
}

func writeNodePackage(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
