package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/configfile"
	"github.com/yusing/shadowtree/internal/recipe"
)

func stageCommands(commands ...recipe.Command) recipe.StageCommands {
	out := make(recipe.StageCommands, 0, len(commands))
	for _, command := range commands {
		out = append(out, recipe.StageCommand{Cmd: command})
	}
	return out
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	var out bytes.Buffer
	stdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	defer func() { os.Stdout = stdout }()

	err = fn()
	if closeErr := write.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, copyErr := out.ReadFrom(read); copyErr != nil {
		t.Fatal(copyErr)
	}
	if err != nil {
		t.Fatalf("function returned error: %v", err)
	}
	return out.String()
}

func writeTextFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseGlobalSkipsSeparateFlagValues(t *testing.T) {
	opts, rest, err := parseGlobal([]string{"--profile", "go", "--print", "--expanded", "test", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	if opts.profile != "go" {
		t.Fatalf("profile = %q", opts.profile)
	}
	if !opts.printOnly {
		t.Fatal("printOnly = false")
	}
	if !opts.expanded {
		t.Fatal("expanded = false")
	}
	if !slices.Equal(rest, []string{"test", "./..."}) {
		t.Fatalf("rest = %#v", rest)
	}
}

func TestValidateGlobalModeRejectsInvalidCombinations(t *testing.T) {
	tests := []struct {
		name string
		opts options
		want string
	}{
		{
			name: "print and check",
			opts: options{printOnly: true, checkOnly: true},
			want: "--print and --check cannot be used together",
		},
		{
			name: "expanded without print",
			opts: options{expanded: true},
			want: "--expanded requires --print",
		},
		{
			name: "shell without check",
			opts: options{checkShell: true},
			want: "--shell requires --check",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateGlobalMode(test.opts)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestHelpBypassesInvalidModeCombinations(t *testing.T) {
	out := captureStdout(t, func() error {
		return run(t.Context(), []string{"--expanded", "--help"})
	})
	if !strings.Contains(out, "usage: shadowtree") {
		t.Fatalf("help output missing usage:\n%s", out)
	}
}

func TestRunInitRejectsExtraOperands(t *testing.T) {
	err := run(t.Context(), []string{"init", "a", "b"})
	if err == nil || err.Error() != "usage: shadowtree init [path]" {
		t.Fatalf("error = %v, want init usage", err)
	}
}

func TestBasicHelpIncludesConfigCommand(t *testing.T) {
	var out bytes.Buffer
	printBasicHelp(&out)

	text := out.String()
	if !strings.Contains(text, "shadowtree config") {
		t.Fatalf("help output missing config command:\n%s", text)
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

func TestCompletionOptionsFollowGlobalBoundary(t *testing.T) {
	opts := completionOptions([]string{"shadowtree", "--sync-out", "dist", "--profile", "go", "test", "--config", "other.toml"})

	if opts.profile != "go" {
		t.Fatalf("profile = %q, want go", opts.profile)
	}
	if opts.configPath != "" {
		t.Fatalf("configPath = %q, want post-recipe config ignored", opts.configPath)
	}
}

func TestRunCompleteIgnoresPostRecipeConfigFlag(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeTextFile(t, filepath.Join(dir, ".shadowtree.toml"), `
[recipes.test]
cmd = "go test"

[recipes.test.arguments.local]
help = "Current config argument."
`)
	writeTextFile(t, filepath.Join(dir, "other.toml"), `
[recipes.test]
cmd = "go test"

[recipes.test.arguments.other]
help = "Other config argument."
`)

	out := captureStdout(t, func() error {
		return runComplete(t.Context(), []string{"fish", "shadowtree", "test", "--config", "other.toml", ""})
	})

	if !strings.Contains(out, "local=\tCurrent config argument.") {
		t.Fatalf("completion output = %q, want current config argument", out)
	}
	if strings.Contains(out, "other=") {
		t.Fatalf("completion output = %q, want no other config argument", out)
	}
}

func TestRunCompleteTreatsPostRecipeProfileFlagAsRecipeArguments(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeTextFile(t, filepath.Join(dir, ".shadowtree.toml"), `
[recipes.test]
cmd = "go test"

[recipes.test.arguments.first]
position = 1
values = "@enum first"

[recipes.test.arguments.second]
position = 2
values = "@enum second"

[recipes.test.arguments.third]
position = 3
values = "@enum third"
`)

	out := captureStdout(t, func() error {
		return runComplete(t.Context(), []string{"fish", "shadowtree", "test", "--profile", "node", ""})
	})

	if !strings.Contains(out, "third\t") {
		t.Fatalf("completion output = %q, want third positional argument value", out)
	}
	if strings.Contains(out, "first\t") || strings.Contains(out, "second\t") {
		t.Fatalf("completion output = %q, want --profile node consumed as recipe arguments", out)
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
		Pre:     stageCommands(recipe.Command{"go", "build"}),
		Cmd:     recipe.Command{"sh", "-c", "set -eu\ninstall -d bin\n"},
		Post:    stageCommands(recipe.Command{"echo", "done"}),
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

func TestPrintRecipeHelpColorsPresetsWhenEnabled(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "benchmark", recipe.Recipe{
		Help: "Run benchmark.",
		Cmd:  recipe.Command{"benchmark"},
		Presets: map[string]recipe.RecipePreset{
			"full": {
				Arguments: map[string]any{
					"enabled": false,
				},
			},
			"smoke": {
				Arguments: map[string]any{
					"enabled": true,
				},
			},
		},
	}, recipeHelpOptions{Color: true})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"\x1b[1;33m- Presets:\x1b[0m",
		"    \x1b[1;32mfull\x1b[0m  \x1b[36menabled=\x1b[0m\x1b[32mfalse\x1b[0m",
		"    \x1b[1;32msmoke\x1b[0m \x1b[36menabled=\x1b[0m\x1b[32mtrue\x1b[0m",
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
		Pre:  stageCommands(recipe.ScriptCommand("@vet")),
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

func TestPrintRecipeHelpShowsRequirements(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "benchmark", recipe.Recipe{
		Help: "Run benchmark.",
		Cmd:  recipe.Command{"go", "test"},
		Requires: recipe.Requirements{
			Commands:         []string{"docker", "openssl"},
			OptionalCommands: []string{"h2load"},
			GoCommands:       map[string]string{"stringer": "golang.org/x/tools/cmd/stringer@latest"},
			NodeCommands:     map[string]string{"eslint": "eslint@^9"},
		},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"- Requires:",
		"commands: docker, openssl",
		"optional: h2load",
		"go: stringer (golang.org/x/tools/cmd/stringer@latest)",
		"node: eslint (eslint@^9)",
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

func TestPrintRecipeHelpIncludesPresets(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "benchmark", recipe.Recipe{
		Help: "Run benchmark.",
		Cmd:  recipe.Command{"benchmark"},
		Arguments: map[string]recipe.Argument{
			"connections": {Type: "int", Default: 32},
			"requests":    {Type: "int", Default: 1000},
		},
		Presets: map[string]recipe.RecipePreset{
			"full": {
				Arguments: map[string]any{
					"connections": int64(128),
					"requests":    int64(50000),
				},
			},
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
		"- Presets:",
		"    full   connections=128 requests=50000",
		"    stable connections=64 requests=20000",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("recipe help output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRecipeHelpIncludesArgumentBounds(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(t.Context(), &out, "benchmark", recipe.Recipe{
		Help: "Run benchmark.",
		Cmd:  recipe.Command{"benchmark"},
		Arguments: map[string]recipe.Argument{
			"retries": {Type: "int", Default: 2, Min: 1, Max: 5},
			"timeout": {Type: "duration", Default: "2s", Min: "100ms", Max: "10s"},
		},
	}, recipeHelpOptions{})
	if err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		"info: type=int default=2 min=1 max=5",
		`info: type=duration default="2s" min="100ms" max="10s"`,
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
