package completion

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/shadowtree/internal/recipe"
)

type Candidate struct {
	Value string
	Help  string
}

func FishScript(w io.Writer) error {
	_, err := io.WriteString(w, `function __shadowtree_complete
    set -l tokens (commandline -opc)
    set -l current (commandline -ct)
    shadowtree __complete fish $tokens $current
end

complete -c shadowtree -f -a '(__shadowtree_complete)'
complete -c shadowtree -l config -r -d 'Use config file'
complete -c shadowtree -l profile -x -a 'go' -d 'Use profile'
complete -c shadowtree -l sync-out -r -d 'Copy path back after success'
complete -c shadowtree -l sync-out-all -d 'Copy entire workspace back after success'
complete -c shadowtree -l keep -d 'Keep shadow workspace after run'
complete -c shadowtree -l print -d 'Print resolved plan without running'
complete -c shadowtree -l verbose -d 'Show commands and workspace paths'
complete -c shadowtree -l help -d 'Show help'
complete -c shadowtree -l version -d 'Show version'
`)
	return err
}

func FishCandidates(w io.Writer, candidates []Candidate) error {
	for _, candidate := range candidates {
		value := sanitizeFishField(candidate.Value)
		desc := sanitizeFishField(candidate.Help)
		if _, err := fmt.Fprintf(w, "%s\t%s\n", value, desc); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeFishField(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func Candidates(words []string, recipes map[string]recipe.Recipe) []Candidate {
	if completesProfile(words) {
		return []Candidate{{Value: "go", Help: "Go project"}}
	}
	if completesConfig(words) || completesSyncOut(words) {
		return nil
	}
	if completesHelpRecipe(words) {
		return recipeCandidates(words, recipes)
	}
	if commandSelected(words) {
		return nil
	}
	candidates := []Candidate{
		{Value: "run", Help: "Run an explicit command in a shadow workspace"},
		{Value: "help", Help: "Show CLI and recipe help"},
		{Value: "recipes", Help: "List resolved recipes"},
		{Value: "init", Help: "Create .shadowtree.toml"},
		{Value: "config", Help: "Print resolved config"},
		{Value: "completion", Help: "Generate shell completion"},
	}
	candidates = append(candidates, recipeCandidates(words, recipes)...)
	return filterPrefix(candidates, currentWord(words))
}

func completesProfile(words []string) bool {
	return len(words) > 0 && words[len(words)-1] == "--profile"
}

func completesConfig(words []string) bool {
	return len(words) > 0 && words[len(words)-1] == "--config"
}

func completesSyncOut(words []string) bool {
	return len(words) > 0 && words[len(words)-1] == "--sync-out"
}

func commandSelected(words []string) bool {
	positionals := positionalWords(words)
	if len(positionals) == 0 {
		return false
	}
	last := currentWord(words)
	if last != "" && positionals[len(positionals)-1] == last {
		return len(positionals) > 1
	}
	return true
}

func completesHelpRecipe(words []string) bool {
	positionals := positionalWords(words)
	return len(positionals) > 0 && positionals[0] == "help" && len(positionals) <= 2
}

func recipeCandidates(words []string, recipes map[string]recipe.Recipe) []Candidate {
	var candidates []Candidate
	names := mapsKeys(recipes)
	slices.Sort(names)
	for _, name := range names {
		candidates = append(candidates, Candidate{Value: name, Help: recipe.Help(recipes[name])})
	}
	return filterPrefix(candidates, currentWord(words))
}

func positionalWords(words []string) []string {
	skipValue := false
	var positionals []string
	for i, word := range words {
		if i == 0 {
			continue
		}
		if word == "" {
			continue
		}
		if skipValue {
			skipValue = false
			continue
		}
		switch word {
		case "--config", "--profile", "--sync-out":
			skipValue = true
			continue
		case "--sync-out-all", "--keep", "--print", "--verbose", "--help", "--version":
			continue
		}
		if strings.HasPrefix(word, "-") {
			continue
		}
		positionals = append(positionals, word)
	}
	return positionals
}

func currentWord(words []string) string {
	if len(words) == 0 {
		return ""
	}
	word := words[len(words)-1]
	if filepath.Base(word) == word {
		return word
	}
	return ""
}

func filterPrefix(candidates []Candidate, prefix string) []Candidate {
	if prefix == "" {
		return candidates
	}
	var out []Candidate
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate.Value, prefix) {
			out = append(out, candidate)
		}
	}
	return out
}

func mapsKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
