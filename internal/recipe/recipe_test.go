package recipe

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestMergeRecipeOverridesOnlySpecifiedFields(t *testing.T) {
	base := Recipe{
		Cmd:         Command{"go", "test"},
		DefaultArgs: []string{"./..."},
		Sandboxed:   new(true),
	}
	override := Recipe{
		Args:      []string{"-count=1"},
		Pre:       []Command{{"go", "generate", "./..."}},
		Sandboxed: new(false),
	}

	got := MergeRecipe(base, override)
	if !slices.Equal(got.Cmd, Command{"go", "test"}) {
		t.Fatalf("Cmd = %#v", got.Cmd)
	}
	if !slices.Equal(got.DefaultArgs, []string{"./..."}) {
		t.Fatalf("DefaultArgs = %#v", got.DefaultArgs)
	}
	if !slices.Equal(got.Args, []string{"-count=1"}) {
		t.Fatalf("Args = %#v", got.Args)
	}
	if len(got.Pre) != 1 || !slices.Equal(got.Pre[0], Command{"go", "generate", "./..."}) {
		t.Fatalf("Pre = %#v", got.Pre)
	}
	if got.Sandboxed == nil || *got.Sandboxed {
		t.Fatalf("Sandboxed = %#v, want false", got.Sandboxed)
	}
}

func TestResolveUsesCLIArgsInsteadOfDefaultArgs(t *testing.T) {
	rec := Recipe{
		Cmd:         Command{"go", "test"},
		Args:        []string{"-race"},
		DefaultArgs: []string{"./..."},
	}

	got, err := Resolve("test-race", rec, []string{"./internal/..."}, nil, nil, "", GoProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{"go", "test", "-race", "./internal/..."}) {
		t.Fatalf("Main = %#v", got.Main)
	}
}

func TestRecipeReferenceSplitsNameAndArgs(t *testing.T) {
	name, args, ok := RecipeReference(Command{"@gen-swagger", "service=api"})
	if !ok {
		t.Fatal("RecipeReference did not detect @ command")
	}
	if name != "gen-swagger" {
		t.Fatalf("name = %q", name)
	}
	if !slices.Equal(args, []string{"service=api"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestRecipeReferenceSplitsBracketStyleArguments(t *testing.T) {
	name, args, ok := RecipeReference(Command{"@build[component=godoxy, mode=dev]"})
	if !ok {
		t.Fatal("RecipeReference did not detect @ command")
	}
	if name != "build" {
		t.Fatalf("name = %q", name)
	}
	if !slices.Equal(args, []string{"component=godoxy", "mode=dev"}) {
		t.Fatalf("args = %#v", args)
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

func TestBuiltinTidySandboxedCanBeOverridden(t *testing.T) {
	builtins := Builtins(GoProfile)
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

func TestGoBuiltinsIncludeWorkflowRecipes(t *testing.T) {
	builtins := Builtins(GoProfile)
	for _, name := range []string{"check", "fmt", "run"} {
		if _, ok := builtins[name]; !ok {
			t.Fatalf("built-in %q is missing", name)
		}
	}
	if RecipeSandboxed(builtins["fmt"]) {
		t.Fatal("built-in fmt is sandboxed, want unsandboxed")
	}

	run := builtins["run"]
	if !slices.Equal(run.Cmd, Command{"go", "run"}) {
		t.Fatalf("run Cmd = %#v", run.Cmd)
	}
	if !slices.Equal(run.DefaultArgs, []string{"{command}"}) {
		t.Fatalf("run DefaultArgs = %#v", run.DefaultArgs)
	}
	command := run.Arguments["command"]
	if command.Type != "rel_path" || command.Position != 1 || !command.Required {
		t.Fatalf("run command argument = %#v", command)
	}
}

func TestMergeRecipesRejectsReservedNames(t *testing.T) {
	if _, err := MergeRecipes(nil, map[string]Recipe{"exec": {Cmd: Command{"go"}}}); err == nil {
		t.Fatal("MergeRecipes succeeded with reserved name")
	}
}

func TestMergeRecipesAllowsRunOverride(t *testing.T) {
	merged, err := MergeRecipes(Builtins(GoProfile), map[string]Recipe{
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

func TestHelpUsesConfiguredHelp(t *testing.T) {
	got := Help(Recipe{
		Help:        "Run tests\ninside a shadow workspace.",
		Cmd:         Command{"go", "test"},
		DefaultArgs: []string{"./..."},
	})

	if got != "Run tests inside a shadow workspace." {
		t.Fatalf("Help = %q", got)
	}
}

func TestMergeRecipeKeepsBaseHelpUnlessOverridden(t *testing.T) {
	got := MergeRecipe(
		Recipe{Help: "Run tests.", Cmd: Command{"go", "test"}},
		Recipe{Args: []string{"-race"}},
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
		Cmd:         Command{"go", "build"},
		Args:        []string{"-o", "bin/{binary}"},
		DefaultArgs: []string{"{project}"},
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
		Cmd:         Command{"go", "build"},
		DefaultArgs: []string{"{project}"},
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

func TestResolveExpandsGlobalVarsAndArgumentValues(t *testing.T) {
	rec := ApplyGlobals(map[string]Recipe{
		"build": {
			Cmd:         Command{"go", "build"},
			Args:        []string{"-ldflags={go_ldflags}"},
			DefaultArgs: []string{"{project}"},
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

func TestResolveShellScriptUsesDefaultShellAndPrelude(t *testing.T) {
	rec := ApplyGlobals(map[string]Recipe{
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
	rec := ApplyGlobals(map[string]Recipe{
		"script": {
			Cmd: ScriptCommand("printf ok"),
		},
	}, nil, "bash", "")["script"]

	got, err := Resolve("script", rec, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Main, Command{scriptCommand, "bash", "printf ok", "shadowtree"}) {
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
		Cmd:         Command{"echo", "{count}"},
		DefaultArgs: []string{"{enabled}"},
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
		Cmd:         Command{"echo"},
		DefaultArgs: []string{"{source}", "{target}"},
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
		Cmd:         Command{"echo"},
		DefaultArgs: []string{"{source}"},
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

func TestInvocationParsesBracketStyleArguments(t *testing.T) {
	name, args := Invocation([]string{"build[project=./cmd/shadowtree,binary=st]"})

	if name != "build" {
		t.Fatalf("name = %q", name)
	}
	if !slices.Equal(args, []string{"project=./cmd/shadowtree", "binary=st"}) {
		t.Fatalf("args = %#v", args)
	}
}
