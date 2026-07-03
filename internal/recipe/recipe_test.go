package recipe

import (
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
