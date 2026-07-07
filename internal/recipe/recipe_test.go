package recipe

import (
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

func TestMergeRecipeOverridesOnlySpecifiedFields(t *testing.T) {
	base := Recipe{
		Cmd:       Command{"go", "test", "{pkg}", "{@}"},
		Sandboxed: new(true),
		Log:       "base.log",
		LogStages: []string{LogStageCmd},
		LogTee:    new(false),
	}
	override := Recipe{
		Pre:       []Command{{"go", "generate", "./..."}},
		Sandboxed: new(false),
		Log:       "override.log",
		LogStages: []string{LogStagePre, LogStagePost},
	}

	got := MergeRecipe(base, override)
	if !slices.Equal(got.Cmd, Command{"go", "test", "{pkg}", "{@}"}) {
		t.Fatalf("Cmd = %#v", got.Cmd)
	}
	if len(got.Pre) != 1 || !slices.Equal(got.Pre[0], Command{"go", "generate", "./..."}) {
		t.Fatalf("Pre = %#v", got.Pre)
	}
	if got.Sandboxed == nil || *got.Sandboxed {
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

func TestResolveExpandsRunIDEverywhere(t *testing.T) {
	recipes := applyGlobals(t, map[string]Recipe{
		"test": {
			Cmd:          ScriptCommand("printf '%s' '{marker} {local} {run_id}'"),
			Pre:          []Command{ScriptCommand("printf '%s' '{run_id}'")},
			Post:         []Command{ScriptCommand("printf '%s' '{run_id}'")},
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
		ScriptBody(got.Recipe.Pre[0]),
		ScriptBody(got.Recipe.Post[0]),
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
		ValueBuiltinOptions{Dir: dir},
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
		ValueBuiltinOptions{Dir: dir},
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
		ValueBuiltinOptions{Dir: dir, ValuePrefix: "services/"},
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

	values, ok, err := BuiltinValues(ScriptCommand("@go-main-packages"), ValueBuiltinOptions{Dir: dir})
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
		ValueBuiltinOptions{Dir: dir, ValuePrefix: "./cmd/"},
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

	values, ok, err := BuiltinValues(ScriptCommand("@go-packages"), ValueBuiltinOptions{Dir: dir})
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
		ValueBuiltinOptions{Dir: dir, ValuePrefix: "./internal/"},
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

	values, ok, err := BuiltinValues(ScriptCommand("@go-packages"), ValueBuiltinOptions{Dir: dir})
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

	values, ok, err := BuiltinValues(ScriptCommand("@go-packages"), ValueBuiltinOptions{Dir: dir})
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

	values, ok, err := BuiltinValues(ScriptCommand("@go-packages"), ValueBuiltinOptions{Dir: dir})
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
	if !got.Sandboxed {
		t.Fatal("Sandboxed = false, want true")
	}
}

func TestResolvePreservesUnsandboxedRecipe(t *testing.T) {
	got, err := Resolve("tidy", Recipe{Cmd: Command{"go", "mod", "tidy"}, Sandboxed: new(false)}, nil, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sandboxed {
		t.Fatal("Sandboxed = true, want false")
	}
}

func TestResolveUnsandboxedIgnoresSyncOut(t *testing.T) {
	got, err := Resolve(
		"tidy",
		Recipe{
			Cmd:       Command{"go", "mod", "tidy"},
			Sandboxed: new(false),
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
	if RecipeSandboxed(builtins["tidy"]) {
		t.Fatal("built-in tidy is sandboxed, want unsandboxed")
	}

	merged := MergeRecipe(builtins["tidy"], Recipe{Help: "Custom tidy."})
	if RecipeSandboxed(merged) {
		t.Fatal("partial override reset tidy sandboxing")
	}

	merged = MergeRecipe(merged, Recipe{Sandboxed: new(true)})
	if !RecipeSandboxed(merged) {
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
	if body := ScriptBody(tidy.Post[0]); body != "if test -f go.work; then go work sync; fi" {
		t.Fatalf("tidy post script = %q, want conditional go work sync", body)
	}
	if body := ScriptBody(tidy.ForEach); body != GoModuleValuesCommand {
		t.Fatalf("tidy ForEach = %q, want %s", body, GoModuleValuesCommand)
	}
}

func TestGoBuiltinsHostMutatingRecipesAreUnsandboxed(t *testing.T) {
	builtins := go1264Builtins(t)
	for _, name := range []string{"fix", "fmt", "tidy"} {
		if RecipeSandboxed(builtins[name]) {
			t.Fatalf("built-in %s is sandboxed, want unsandboxed", name)
		}
	}
}

func TestGoBuiltinFixRequiresGoAfter126(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n\ngo 1.26\n")

	if _, ok := Builtins(GoProfile, BuiltinOptions{Dir: dir})["fix"]; ok {
		t.Fatal("built-in fix exists for go 1.26, want absent")
	}

	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n\ngo 1.26.4\n")

	fix, ok := Builtins(GoProfile, BuiltinOptions{Dir: dir})["fix"]
	if !ok {
		t.Fatal("built-in fix is missing for go 1.26.4")
	}
	if !slices.Equal(fix.Cmd, Command{"go", "fix", "{pkg}", "{@}"}) {
		t.Fatalf("fix Cmd = %#v", fix.Cmd)
	}
}

func TestMostCommonGoDirectiveVersion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n\ngo 1.27\n")
	mkdirAll(t, filepath.Join(dir, "services", "api"))
	writeFile(t, filepath.Join(dir, "services", "api", "go.mod"), "module example.com/api\n\ngo 1.26.4\n")
	mkdirAll(t, filepath.Join(dir, "services", "worker"))
	writeFile(t, filepath.Join(dir, "services", "worker", "go.mod"), "module example.com/worker\n\ngo 1.26.4\n")

	if got := mostCommonGoDirectiveVersion(dir); got != "1.26.4" {
		t.Fatalf("mostCommonGoDirectiveVersion = %q, want 1.26.4", got)
	}

	empty := t.TempDir()
	if got := mostCommonGoDirectiveVersion(empty); got != "unknown" {
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
	if RecipeSandboxed(builtins["fmt"]) {
		t.Fatal("built-in fmt is sandboxed, want unsandboxed")
	}
	if !slices.Equal(builtins["fmt"].Cmd, Command{"go", "fmt", "{target}", "{@}"}) {
		t.Fatalf("fmt Cmd = %#v", builtins["fmt"].Cmd)
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
		t.Fatalf("run Cmd = %#v", run.Cmd)
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
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestGoBuiltinsRunForEachGoModule(t *testing.T) {
	builtins := go1264Builtins(t)
	for _, name := range []string{"build", "fix", "fmt", "generate", "lint", "test", "test-race", "tidy", "vet"} {
		rec := builtins[name]
		if values := ScriptBody(rec.ForEach); values != GoModuleValuesCommand {
			t.Fatalf("%s for_each = %q", name, values)
		}
		if rec.Workdir != "{item}" {
			t.Fatalf("%s workdir = %q, want {item}", name, rec.Workdir)
		}
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

func TestGoBuiltinBuildCompletesMainPackages(t *testing.T) {
	build := go1264Builtins(t)["build"]
	arg := build.Arguments["pkg"]
	if arg.Type != "rel_path" || arg.Position != 1 || arg.Default != "./..." {
		t.Fatalf("build pkg argument = %#v", arg)
	}
	if values := ScriptBody(arg.Values); values != GoMainPackageValuesCommand {
		t.Fatalf("build pkg values = %q", values)
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
		Pre: []Command{{"go", "build"}},
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
		Recipe{Pre: []Command{{"go", "generate", "./..."}}},
	)
	if got.Help != "Run tests." {
		t.Fatalf("Help = %q", got.Help)
	}

	got = MergeRecipe(got, Recipe{Help: "Run race tests."})
	if got.Help != "Run race tests." {
		t.Fatalf("Help = %q", got.Help)
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
	if !containsPlaceholderName(prelude) {
		t.Fatal("containsPlaceholderName = false, want true")
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
		Sandboxed: new(false),
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

func TestValidateConfigRejectsInvalidLogSettings(t *testing.T) {
	cases := []struct {
		name string
		rec  Recipe
		want string
	}{
		{
			name: "log stages without log",
			rec:  Recipe{Cmd: Command{"true"}, LogStages: []string{LogStageCmd}},
			want: "log_stages requires log",
		},
		{
			name: "log tee without log",
			rec:  Recipe{Cmd: Command{"true"}, LogTee: new(false)},
			want: "log_tee requires log",
		},
		{
			name: "bad stage",
			rec:  Recipe{Cmd: Command{"true"}, Log: "run.log", LogStages: []string{"cleanup"}},
			want: `unsupported stage "cleanup"`,
		},
		{
			name: "empty stages",
			rec:  Recipe{Cmd: Command{"true"}, Log: "run.log", LogStages: []string{}},
			want: "log_stages must not be empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(Config{Recipes: map[string]Recipe{"test": tc.rec}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateConfig() error = %v, want %q", err, tc.want)
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
		if rec.Sandboxed == nil || *rec.Sandboxed {
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

	if len(rec.Pre) != 2 || !slices.Equal(rec.Pre[0], Command{"@lint"}) || !slices.Equal(rec.Pre[1], Command{"@typecheck"}) {
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
	return Builtins(GoProfile, BuiltinOptions{Dir: dir})
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
