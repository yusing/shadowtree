package completion

import (
	"bytes"
	"os"
	"path/filepath"
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

func TestCandidatesSeparateExecCommandFromRunRecipe(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", ""}, map[string]recipe.Recipe{
		"run": {Help: "Run a Go command.", Cmd: recipe.Command{"go", "run"}},
	})

	if !hasCandidate(candidates, "exec") {
		t.Fatalf("candidates = %#v, want exec", candidates)
	}
	if got := helpFor(candidates, "run"); got != "Run a Go command." {
		t.Fatalf("run help = %q", got)
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

func TestCandidatesCompleteDynamicArgumentValues(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build[project=s"}, map[string]recipe.Recipe{
		"build": {
			Cmd:          recipe.Command{"go", "build"},
			ShellPrelude: "project_values() { printf 'sip/scheduler\\tScheduler daemon\\ntools/agi\\tFastAGI server\\n'; }",
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.Command{"sh", "-c", "project_values"},
				},
			},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "build[project=sip/scheduler" || candidates[0].Help != "Scheduler daemon" {
		t.Fatalf("candidates = %#v, want sip scheduler value", candidates)
	}
}

func TestCandidatesCompleteDynamicArgumentValuesWithRecipeEnv(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build", "project=api"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Env: map[string]string{"PROJECTS": "api\tAPI service\nweb\tWeb service\n"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand("printf '%s' \"$PROJECTS\""),
				},
			},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "project=api" || candidates[0].Help != "API service" {
		t.Fatalf("candidates = %#v, want api value from recipe env", candidates)
	}
}

func TestCandidatesCompleteDynamicArgumentValuesFromRecipeReference(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build", "project=api"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.Command{"@projects"},
				},
			},
		},
		"projects": {
			Cmd: recipe.Command{"printf", "api\tAPI service\nweb\tWeb service\n"},
		},
	})

	if len(candidates) != 1 || candidates[0].Value != "project=api" || candidates[0].Help != "API service" {
		t.Fatalf("candidates = %#v, want api value from recipe reference", candidates)
	}
}

func TestCandidatesCompleteSpacedDynamicArgumentValuesContainingSlash(t *testing.T) {
	candidates := complete(t, []string{"shadowtree", "build", "project=sip/"}, map[string]recipe.Recipe{
		"build": {
			Cmd: recipe.Command{"go", "build"},
			Arguments: map[string]recipe.Argument{
				"project": {
					Type:   "string",
					Values: recipe.ScriptCommand("printf 'sip/scheduler\\tScheduler daemon\\nsip/snmptrap\\tSNMP trap daemon\\n'"),
				},
			},
		},
	})

	if len(candidates) != 2 || candidates[0].Value != "project=sip/scheduler" || candidates[1].Value != "project=sip/snmptrap" {
		t.Fatalf("candidates = %#v, want slash-containing project values", candidates)
	}
}

func TestCandidatesCompleteNamedPathArgumentValues(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "cmd", "shadowtree"))
	mkdirAll(t, filepath.Join(dir, "cmd", "shadowtree-lsp"))
	writeFile(t, filepath.Join(dir, "README.md"))

	recipes := map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "path"},
			},
		},
	}
	candidates := completeWithOptions(t, []string{"shadowtree", "open", "file=cmd"}, recipes, Options{Dir: dir})
	if len(candidates) != 1 || candidates[0].Value != "file=cmd/" || candidates[0].Help != "directory" {
		t.Fatalf("candidates = %#v, want cmd directory", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "open", "file=cmd/"}, recipes, Options{Dir: dir})
	if !hasCandidate(candidates, "file=cmd/shadowtree/") || !hasCandidate(candidates, "file=cmd/shadowtree-lsp/") {
		t.Fatalf("candidates = %#v, want cmd children", candidates)
	}

	absolutePrefix := "file=" + filepath.Join(dir, "c")
	candidates = completeWithOptions(t, []string{"shadowtree", "open", absolutePrefix}, recipes, Options{Dir: t.TempDir()})
	if len(candidates) != 1 || candidates[0].Value != "file="+filepath.Join(dir, "cmd")+"/" {
		t.Fatalf("candidates = %#v, want absolute cmd directory", candidates)
	}
}

func TestCandidatesCompletePositionalPathArgumentValues(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "cmd", "shadowtree"))
	mkdirAll(t, filepath.Join(dir, "cmd", "shadowtree-lsp"))

	candidates := completeWithOptions(t, []string{"shadowtree", "open", "cmd"}, map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "rel_path", Position: 1},
			},
		},
	}, Options{Dir: dir})

	if len(candidates) != 1 || candidates[0].Value != "cmd/" {
		t.Fatalf("candidates = %#v, want cmd/", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "open", "cmd/"}, map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "rel_path", Position: 1},
			},
		},
	}, Options{Dir: dir})
	if !hasCandidate(candidates, "cmd/shadowtree/") || !hasCandidate(candidates, "cmd/shadowtree-lsp/") {
		t.Fatalf("candidates = %#v, want cmd children", candidates)
	}
}

func TestCandidatesCompletePathHomeButNotRelativePathHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mkdirAll(t, filepath.Join(home, "projects"))

	pathRecipes := map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "path"},
			},
		},
	}
	candidates := completeWithOptions(t, []string{"shadowtree", "open", "file=~/"}, pathRecipes, Options{Dir: t.TempDir()})
	if len(candidates) != 1 || candidates[0].Value != "file=~/projects/" {
		t.Fatalf("candidates = %#v, want home child", candidates)
	}

	relPathRecipes := map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "rel_path"},
			},
		},
	}
	candidates = completeWithOptions(t, []string{"shadowtree", "open", "file=~/"}, relPathRecipes, Options{Dir: t.TempDir()})
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want no rel_path home completion", candidates)
	}
}

func TestCandidatesFilterPathArgumentValuesByKind(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "bin"))
	writeFile(t, filepath.Join(dir, "bin", "tool"))
	if err := os.Chmod(filepath.Join(dir, "bin", "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "bin", "notes.txt"))
	mkdirAll(t, filepath.Join(dir, "bin", "nested"))

	candidates := completeWithOptions(t, []string{"shadowtree", "run-bin", "bin/"}, map[string]recipe.Recipe{
		"run-bin": {
			Cmd: recipe.Command{"sh", "-c"},
			Arguments: map[string]recipe.Argument{
				"bin": {Type: "rel_path", PathKind: "executable", Position: 1},
			},
		},
	}, Options{Dir: dir})
	if !hasCandidate(candidates, "bin/tool") || !hasCandidate(candidates, "bin/nested/") {
		t.Fatalf("candidates = %#v, want executable and traversal directory", candidates)
	}
	if hasCandidate(candidates, "bin/notes.txt") {
		t.Fatalf("candidates = %#v, want non-executable file filtered out", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "cd", "bin/"}, map[string]recipe.Recipe{
		"cd": {
			Cmd: recipe.Command{"cd"},
			Arguments: map[string]recipe.Argument{
				"dir": {Type: "rel_path", PathKind: "dir", Position: 1},
			},
		},
	}, Options{Dir: dir})
	if len(candidates) != 1 || candidates[0].Value != "bin/nested/" {
		t.Fatalf("candidates = %#v, want only nested directory", candidates)
	}

	candidates = completeWithOptions(t, []string{"shadowtree", "open", "bin/"}, map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "rel_path", PathKind: "file", Position: 1},
			},
		},
	}, Options{Dir: dir})
	if !hasCandidate(candidates, "bin/tool") || !hasCandidate(candidates, "bin/notes.txt") || !hasCandidate(candidates, "bin/nested/") {
		t.Fatalf("candidates = %#v, want files and traversal directory", candidates)
	}
}

func TestCandidatesRejectUnsupportedShell(t *testing.T) {
	_, err := Candidates(t.Context(), "zsh", []string{"shadowtree", ""}, nil, Options{})
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

func TestFishPathCandidatesEscapeValueWhitespace(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "My Project"))

	candidates := completeWithOptions(t, []string{"shadowtree", "open", "file=My"}, map[string]recipe.Recipe{
		"open": {
			Cmd: recipe.Command{"cat"},
			Arguments: map[string]recipe.Argument{
				"file": {Type: "path"},
			},
		},
	}, Options{Dir: dir})

	var out bytes.Buffer
	if err := WriteCandidates(&out, "fish", candidates); err != nil {
		t.Fatal(err)
	}

	want := "file=My\\ Project/\tdirectory\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func complete(t *testing.T, words []string, recipes map[string]recipe.Recipe) []Candidate {
	t.Helper()
	return completeWithOptions(t, words, recipes, Options{})
}

func completeWithOptions(t *testing.T, words []string, recipes map[string]recipe.Recipe, opts Options) []Candidate {
	t.Helper()
	candidates, err := Candidates(t.Context(), "fish", words, recipes, opts)
	if err != nil {
		t.Fatal(err)
	}
	return candidates
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
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
