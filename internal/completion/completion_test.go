package completion

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
)

func TestCandidatesIncludeRecipesForEmptyCurrentWord(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", ""}, map[string]recipe.Recipe{
		"test": {Help: "Run tests.", Cmd: recipe.Command{"go", "test"}, DefaultArgs: []string{"./..."}},
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
	candidates := complete(t, []string{"shadowtree", "--profile"}, nil)

	if !hasCandidate(candidates, "go") {
		t.Fatalf("candidates = %#v, want go", candidates)
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

func TestCandidatesRejectUnsupportedShell(t *testing.T) {
	_, err := Candidates("zsh", []string{"shadowtree", ""}, nil)
	if err == nil {
		t.Fatal("Candidates succeeded for unsupported shell")
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

func complete(t *testing.T, words []string, recipes map[string]recipe.Recipe) []Candidate {
	t.Helper()
	candidates, err := Candidates("fish", words, recipes)
	if err != nil {
		t.Fatal(err)
	}
	return candidates
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
