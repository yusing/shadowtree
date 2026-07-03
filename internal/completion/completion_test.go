package completion

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
)

func TestCandidatesIncludeRecipesForEmptyCurrentWord(t *testing.T) {
	candidates := Candidates([]string{"shadowtree", ""}, map[string]recipe.Recipe{
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
	candidates := Candidates([]string{"shadowtree", "te"}, map[string]recipe.Recipe{
		"build": {Help: "Build binary.", Cmd: recipe.Command{"go", "build"}},
		"test":  {Help: "Run tests.", Cmd: recipe.Command{"go", "test"}},
	})

	if len(candidates) != 1 || candidates[0].Value != "test" {
		t.Fatalf("candidates = %#v, want test only", candidates)
	}
}

func TestCandidatesCompleteProfileValues(t *testing.T) {
	candidates := Candidates([]string{"shadowtree", "--profile"}, nil)

	if !hasCandidate(candidates, "go") {
		t.Fatalf("candidates = %#v, want go", candidates)
	}
}

func TestCandidatesCompleteRecipesAfterHelp(t *testing.T) {
	candidates := Candidates([]string{"shadowtree", "help", ""}, map[string]recipe.Recipe{
		"test": {Help: "Run tests.", Cmd: recipe.Command{"go", "test"}},
	})

	if len(candidates) != 1 || candidates[0].Value != "test" {
		t.Fatalf("candidates = %#v, want test only", candidates)
	}
}

func TestFishCandidatesSanitizeNewlines(t *testing.T) {
	var out bytes.Buffer
	err := FishCandidates(&out, []Candidate{{
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
