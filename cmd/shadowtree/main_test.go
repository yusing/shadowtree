package main

import (
	"bytes"
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

func TestPrintRecipeHelpIncludesCommandDetails(t *testing.T) {
	var out bytes.Buffer
	err := printRecipeHelp(&out, "install", recipe.Recipe{
		Help:    "Install Shadowtree.",
		Pre:     []recipe.Command{{"go", "build"}},
		Cmd:     recipe.Command{"sh", "-c", "set -eu\ninstall -d bin\n"},
		SyncOut: []string{"bin/shadowtree"},
	})
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

func zeroLoaded() configfile.Loaded {
	return configfile.Loaded{}
}
