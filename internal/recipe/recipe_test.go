package recipe

import (
	"context"
	"slices"
	"testing"
)

func TestMergeRecipeOverridesOnlySpecifiedFields(t *testing.T) {
	base := Recipe{
		Cmd:         Command{"go", "test"},
		DefaultArgs: []string{"./..."},
	}
	override := Recipe{
		Args: []string{"-count=1"},
		Pre:  []Command{{"go", "generate", "./..."}},
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

func TestMergeRecipesRejectsReservedNames(t *testing.T) {
	if _, err := MergeRecipes(nil, map[string]Recipe{"run": {Cmd: Command{"go"}}}); err == nil {
		t.Fatal("MergeRecipes succeeded with reserved name")
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
	if !slices.Equal(got.Main, Command{"sh", "-c", "say_hi() { printf hi; }\nsay_hi", "shadowtree"}) {
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
	if !slices.Equal(got.Main, Command{"bash", "-c", "printf ok", "shadowtree"}) {
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
		context.Background(),
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

func TestInvocationParsesBracketStyleArguments(t *testing.T) {
	name, args := Invocation([]string{"build[project=./cmd/shadowtree,binary=st]"})

	if name != "build" {
		t.Fatalf("name = %q", name)
	}
	if !slices.Equal(args, []string{"project=./cmd/shadowtree", "binary=st"}) {
		t.Fatalf("args = %#v", args)
	}
}
