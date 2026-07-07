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

func TestParseRecipeHelpOptions(t *testing.T) {
	color, err := parseRecipeHelpOptions([]string{"color=false"})
	if err != nil {
		t.Fatal(err)
	}
	if color {
		t.Fatal("color = true, want false")
	}

	color, err = parseRecipeHelpOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !color {
		t.Fatal("color = false, want true")
	}
}

func TestParseRecipeHelpOptionsRejectsUnknown(t *testing.T) {
	_, err := parseRecipeHelpOptions([]string{"theme=dark"})
	if err == nil || !strings.Contains(err.Error(), `unknown help argument "theme"`) {
		t.Fatalf("error = %v, want unknown help argument", err)
	}
}

func TestParseRecipeHelpOptionsRejectsInvalidColor(t *testing.T) {
	_, err := parseRecipeHelpOptions([]string{"color=maybe"})
	if err == nil || !strings.Contains(err.Error(), `color: want bool, got "maybe"`) {
		t.Fatalf("error = %v, want invalid color", err)
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
		Post:    []recipe.Command{{"echo", "done"}},
		SyncOut: []string{"bin/shadowtree"},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"Install Shadowtree",
		"- Command:\n\n    sh -c <script>",
		"- Pre commands:\n\n    [0] go build",
		"- Post commands:\n\n    [0] echo done",
		"- Sync out:\n\n    bin/shadowtree",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("recipe help output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRecipeHelpPreservesFallbackCommandSummary(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "test", recipe.Recipe{
		Cmd: recipe.Command{"go", "test", "."},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if text := out.String(); !strings.Contains(text, "  go test .\n") {
		t.Fatalf("recipe help output missing preserved fallback command summary:\n%s", text)
	}
}

func TestPrintRecipeHelpColorsWhenEnabled(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "build", recipe.Recipe{
		Help: "Build binary.",
		Cmd:  recipe.Command{"go", "build"},
		Arguments: map[string]recipe.Argument{
			"project": {
				Help: "Go package to build.",
				Type: "rel_path",
			},
		},
	}, recipeHelpOptions{Color: true})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"\x1b[1;36mbuild\x1b[0m",
		"\x1b[1;33m- Arguments:\x1b[0m",
		"\x1b[1;32mproject\x1b[0m - Go package to build",
		"\x1b[36minfo:\x1b[0m type=\x1b[32mrel_path\x1b[0m",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("colored recipe help output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRecipeHelpShowsBareRecipeReferences(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "check", recipe.Recipe{
		Help: "Run vet and tests.",
		Pre:  []recipe.Command{recipe.ScriptCommand("@vet")},
		Cmd:  recipe.ScriptCommand("@test ./..."),
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"- Command:\n\n    @test ./...  +1 pre",
		"- Pre commands:\n\n    [0] @vet",
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
	if !strings.Contains(text, "- Sandboxed:\n\n    false") {
		t.Fatalf("recipe help output missing sandboxed marker:\n%s", text)
	}
	if strings.Contains(text, "- Sync out:") {
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
		"- Arguments:",
		"    project - Go main package",
		"    project - Go main package\n\n      info: type=string",
		"      values:",
		"        cmd/api     API server",
		"        cmd/worker  Worker service",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("recipe help output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRecipeHelpIncludesProfiles(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "benchmark", recipe.Recipe{
		Help: "Run benchmark.",
		Cmd:  recipe.Command{"benchmark"},
		Arguments: map[string]recipe.Argument{
			"connections": {Type: "int", Default: 32},
			"requests":    {Type: "int", Default: 1000},
		},
		Profiles: map[string]recipe.RecipeProfile{
			"stable": {
				Arguments: map[string]any{
					"connections": int64(64),
					"requests":    int64(20000),
				},
			},
		},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"- Profiles:",
		"    stable connections=64 requests=20000",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("recipe help output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRecipeHelpFormatsArgumentBlocks(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "build", recipe.Recipe{
		Help: "Build binary.",
		Cmd:  recipe.Command{"go", "build"},
		SyncOut: []string{
			"bin/{name}",
		},
		Arguments: map[string]recipe.Argument{
			"component": {
				Help:     "Binary component to build.",
				Type:     "string",
				Position: 1,
				Default:  "godoxy",
				Values:   recipe.ScriptCommand("printf 'godoxy\\nmain\\n'"),
			},
			"docker": {
				Help:    "Move the binary to /app/run for container images.",
				Type:    "bool",
				Default: false,
			},
			"name": {
				Help:    "Override output binary name under bin/.",
				Type:    "string",
				Default: "",
			},
		},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"    component - Binary component to build",
		"info: type=string position=1 default=\"godoxy\"",
		"      values:",
		"        main\n\n    docker - Move the binary to /app/run for container images\n\n      info: type=bool default=false",
		`    name - Override output binary name under bin/` + "\n\n" + `      info: type=string default=""`,
		`info: type=string default=""` + "\n\n- Sync out:\n\n    bin/{name}",
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
		"        api",
		"        worker",
		"        admin ui",
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

	want := "        root/recipe/" + filepath.Base(dir) + "  from command"
	if text := out.String(); !strings.Contains(text, want) {
		t.Fatalf("recipe help output missing %q:\n%s", want, text)
	}
}

func TestPrintRecipeHelpDynamicValuesExpandPreludePlaceholders(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "build", recipe.Recipe{
		Help:         "Build binary.",
		ShellPrelude: "project_values() { printf '%s\\tfrom command\\n' \"{project}\"; }",
		Vars:         map[string]string{"project": "cmd/api"},
		Cmd:          recipe.Command{"go", "build"},
		Arguments: map[string]recipe.Argument{
			"project": {
				Type:   "string",
				Values: recipe.ScriptCommand("project_values"),
			},
		},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	want := "        cmd/api  from command"
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
		"        api  API service",
		"        web  Web service",
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
	if !strings.Contains(text, "    race\n\n      info: type=bool") {
		t.Fatalf("recipe help output missing bool argument:\n%s", text)
	}
	if strings.Contains(text, "\n      bool") {
		t.Fatalf("recipe help output includes fallback bool help:\n%s", text)
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
