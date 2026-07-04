package main

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/configfile"
	"github.com/yusing/shadowtree/internal/recipe"
)

func TestParseGlobalSkipsSeparateFlagValues(t *testing.T) {
	opts, rest, err := parseGlobal([]string{"--profile", "go", "--print", "test", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	if opts.profile != "go" {
		t.Fatalf("profile = %q", opts.profile)
	}
	if !opts.printOnly {
		t.Fatal("printOnly = false")
	}
	if !slices.Equal(rest, []string{"test", "./..."}) {
		t.Fatalf("rest = %#v", rest)
	}
}

func TestParseGlobalStopsAfterRecipe(t *testing.T) {
	_, rest, err := parseGlobal([]string{"test", "-v", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rest, []string{"test", "-v", "./..."}) {
		t.Fatalf("rest = %#v", rest)
	}
}

func TestPrintHelpIncludesRecipeHelp(t *testing.T) {
	var out bytes.Buffer
	err := printHelp(&out, zeroLoaded(), "go", map[string]recipe.Recipe{
		"test": {Help: "Run tests.", Cmd: recipe.Command{"go", "test"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"usage: shadowtree",
		"recipes:",
		"test         Run tests.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("help output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintConfigAlignsLongRecipeNames(t *testing.T) {
	var out bytes.Buffer
	err := printConfig(&out, zeroLoaded(), "go", map[string]recipe.Recipe{
		"build":            {Help: "Build binary.", Cmd: recipe.Command{"go", "build"}},
		"export-db-schema": {Help: "Export schemas.", Cmd: recipe.Command{"go", "run"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(out.String(), "\n")
	buildLine := lineWithPrefix(t, lines, "  build")
	exportLine := lineWithPrefix(t, lines, "  export-db-schema")
	if got, want := strings.Index(buildLine, "Build binary."), strings.Index(exportLine, "Export schemas."); got != want {
		t.Fatalf("description columns differ: build=%d export=%d\n%s\n%s", got, want, buildLine, exportLine)
	}
}

func TestPrintRecipesAlignsLongRecipeNames(t *testing.T) {
	var out bytes.Buffer
	err := printRecipes(&out, map[string]recipe.Recipe{
		"build":            {Help: "Build binary.", Cmd: recipe.Command{"go", "build"}},
		"export-db-schema": {Help: "Export schemas.", Cmd: recipe.Command{"go", "run"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(out.String(), "\n")
	buildLine := lineWithPrefix(t, lines, "build")
	exportLine := lineWithPrefix(t, lines, "export-db-schema")
	if got, want := strings.Index(buildLine, "Build binary."), strings.Index(exportLine, "Export schemas."); got != want {
		t.Fatalf("description columns differ: build=%d export=%d\n%s\n%s", got, want, buildLine, exportLine)
	}
}

func TestPrintRecipeHelpIncludesCommandDetails(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "install", recipe.Recipe{
		Help:    "Install Shadowtree.",
		Pre:     []recipe.Command{{"go", "build"}},
		Cmd:     recipe.Command{"sh", "-c", "set -eu\ninstall -d bin\n"},
		SyncOut: []string{"bin/shadowtree"},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"Install Shadowtree.",
		"command: sh -c <script>",
		"pre[0]: go build",
		"sync_out: bin/shadowtree",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("recipe help output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRecipeHelpShowsBareRecipeReferences(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "check", recipe.Recipe{
		Help:        "Run vet and tests.",
		Pre:         []recipe.Command{recipe.ScriptCommand("@vet")},
		Cmd:         recipe.Command{"@test"},
		DefaultArgs: []string{"./..."},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"command: @test ./...  +1 pre",
		"pre[0]: @vet",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("recipe help output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRecipeHelpHidesUnsandboxedSyncOut(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "tidy", recipe.Recipe{
		Help:      "Tidy module files.",
		Cmd:       recipe.Command{"go", "mod", "tidy"},
		Sandboxed: new(false),
		SyncOut:   []string{"go.mod", "go.sum"},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	if !strings.Contains(text, "sandboxed: false") {
		t.Fatalf("recipe help output missing sandboxed marker:\n%s", text)
	}
	if strings.Contains(text, "sync_out:") {
		t.Fatalf("recipe help output shows ignored sync_out:\n%s", text)
	}
}

func TestPrintRecipeHelpIncludesDynamicArgumentValues(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "build", recipe.Recipe{
		Help: "Build binary.",
		Cmd:  recipe.Command{"go", "build"},
		Arguments: map[string]recipe.Argument{
			"project": {
				Help:   "Go main package.",
				Type:   "string",
				Values: recipe.ScriptCommand("printf 'cmd/api\\tAPI server\\ncmd/worker\\tWorker service\\n'"),
			},
		},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"arg project:string",
		"  values:",
		"    cmd/api     API server",
		"    cmd/worker  Worker service",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("recipe help output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRecipeHelpIncludesEnumArgumentValues(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "build", recipe.Recipe{
		Help: "Build binary.",
		Cmd:  recipe.Command{"go", "build"},
		Arguments: map[string]recipe.Argument{
			"project": {
				Type:   "string",
				Values: recipe.ScriptCommand(`@enum api worker "admin ui"`),
			},
		},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"    api",
		"    worker",
		"    admin ui",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("recipe help output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRecipeHelpDynamicValuesUseDirEnvAndPrelude(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "build", recipe.Recipe{
		Help:         "Build binary.",
		ShellPrelude: "project_values() { printf '%s/%s/%s\\tfrom command\\n' \"$ROOT_VALUE\" \"$RECIPE_VALUE\" \"${PWD##*/}\"; }",
		Cmd:          recipe.Command{"go", "build"},
		Env:          map[string]string{"RECIPE_VALUE": "recipe"},
		Arguments: map[string]recipe.Argument{
			"project": {
				Type:   "string",
				Values: recipe.ScriptCommand("project_values"),
			},
		},
	}, recipeHelpOptions{
		Dir: dir,
		Env: map[string]string{"ROOT_VALUE": "root"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "    root/recipe/" + filepath.Base(dir) + "  from command"
	if text := out.String(); !strings.Contains(text, want) {
		t.Fatalf("recipe help output missing %q:\n%s", want, text)
	}
}

func TestPrintRecipeHelpDynamicValuesUseScriptRecipeReference(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "build", recipe.Recipe{
		Help: "Build binary.",
		Cmd:  recipe.Command{"go", "build"},
		Arguments: map[string]recipe.Argument{
			"project": {
				Type:   "string",
				Values: recipe.ScriptCommand("@projects"),
			},
		},
	}, recipeHelpOptions{
		Recipes: map[string]recipe.Recipe{
			"projects": {
				Cmd: recipe.Command{"printf", "api\tAPI service\nweb\tWeb service\n"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"    api  API service",
		"    web  Web service",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("recipe help output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRecipeHelpReportsUnavailableDynamicValues(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "build", recipe.Recipe{
		Help: "Build binary.",
		Cmd:  recipe.Command{"go", "build"},
		Arguments: map[string]recipe.Argument{
			"project": {
				Type:   "string",
				Values: recipe.ScriptCommand("exit 7"),
			},
		},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	if !strings.Contains(text, "values: <unavailable:") {
		t.Fatalf("recipe help output missing unavailable marker:\n%s", text)
	}
}

func TestPrintRecipeHelpSkipsImplicitBoolArgumentValues(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "test", recipe.Recipe{
		Help: "Run tests.",
		Cmd:  recipe.Command{"go", "test"},
		Arguments: map[string]recipe.Argument{
			"race": {Type: "bool"},
		},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	if strings.Contains(text, "  values:") {
		t.Fatalf("recipe help output includes implicit bool values:\n%s", text)
	}
	if !strings.Contains(text, "arg race:bool  bool") {
		t.Fatalf("recipe help output missing bool argument:\n%s", text)
	}
}

func zeroLoaded() configfile.Loaded {
	return configfile.Loaded{}
}

func lineWithPrefix(t *testing.T, lines []string, prefix string) string {
	t.Helper()
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("missing line with prefix %q in %#v", prefix, lines)
	return ""
}
