package completion

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/shadowtree/internal/recipe"
)

type Candidate struct {
	Value string
	Help  string
}

type Options struct {
	Dir string
	Env map[string]string
}

type shellSpec struct {
	name       string
	groupOpen  string
	groupClose string
	normalize  func(string) string
}

var fishShell = shellSpec{
	name:       "fish",
	groupOpen:  "[",
	groupClose: "]",
	normalize:  func(word string) string { return word },
}

func shellFor(name string) (shellSpec, error) {
	switch name {
	case "fish":
		return fishShell, nil
	default:
		return shellSpec{}, fmt.Errorf("unsupported shell: %s", name)
	}
}

func Script(w io.Writer, shell string) error {
	switch shell {
	case "fish":
		return fishScript(w)
	default:
		return fmt.Errorf("unsupported shell: %s", shell)
	}
}

func fishScript(w io.Writer) error {
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
complete -c shadowtree -l print -d 'Print resolved plan without running'
complete -c shadowtree -l verbose -d 'Show commands and workspace paths'
complete -c shadowtree -l help -d 'Show help'
complete -c shadowtree -l version -d 'Show version'
`)
	return err
}

func WriteCandidates(w io.Writer, shell string, candidates []Candidate) error {
	switch shell {
	case "fish":
		return writeFishCandidates(w, candidates)
	default:
		return fmt.Errorf("unsupported shell: %s", shell)
	}
}

func writeFishCandidates(w io.Writer, candidates []Candidate) error {
	for _, candidate := range candidates {
		value := escapeFishValue(candidate.Value)
		desc := sanitizeFishField(candidate.Help)
		if _, err := fmt.Fprintf(w, "%s\t%s\n", value, desc); err != nil {
			return err
		}
	}
	return nil
}

func escapeFishValue(value string) string {
	if !strings.ContainsAny(value, "\\ \t\r\n") {
		return value
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch r {
		case '\\', ' ':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\t':
			b.WriteString("\\t")
		case '\r':
			b.WriteString("\\r")
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sanitizeFishField(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func Candidates(ctx context.Context, shell string, words []string, recipes map[string]recipe.Recipe, opts Options) ([]Candidate, error) {
	spec, err := shellFor(shell)
	if err != nil {
		return nil, err
	}
	if completesProfile(words) {
		return []Candidate{{Value: "go", Help: "Go project"}}, nil
	}
	if completesConfig(words) || completesSyncOut(words) {
		return nil, nil
	}
	if completesHelpRecipe(words) {
		return recipeCandidates(words, recipes), nil
	}
	if candidates, ok := argumentCandidates(ctx, spec, words, recipes, opts); ok {
		return candidates, nil
	}
	if commandSelected(words) {
		return nil, nil
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
	return filterPrefix(candidates, currentWord(words)), nil
}

func argumentCandidates(ctx context.Context, spec shellSpec, words []string, recipes map[string]recipe.Recipe, opts Options) ([]Candidate, bool) {
	positionals := normalizedWords(spec, positionalWords(words))
	if len(positionals) == 0 {
		return nil, false
	}
	name, typedPrefix, grouped := cutGroupedStart(spec, positionals[0])
	if !grouped {
		name = positionals[0]
	}
	current := spec.normalize(currentWord(words))
	rawCurrent := spec.normalize(rawCurrentWord(words))
	if current == "" && (strings.Contains(rawCurrent, "=") || strings.Contains(rawCurrent, spec.groupOpen)) {
		current = rawCurrent
	}
	if strings.Contains(current, spec.groupOpen) {
		if currentName, currentPrefix, ok := cutGroupedStart(spec, current); ok {
			name = currentName
			typedPrefix = currentPrefix
			grouped = true
		}
	}
	rec, exists := recipes[name]
	if !exists || len(rec.Arguments) == 0 {
		return nil, false
	}
	if current == "" && rawCurrent != "" {
		current = rawCurrent
	}
	if !grouped && len(positionals) == 1 && current == positionals[0] {
		return nil, false
	}
	used := usedArguments(spec, positionals[1:], current, rec)
	if grouped {
		prefix := name + spec.groupOpen
		content := typedPrefix
		if len(positionals) > 1 && positionals[0] != current {
			content = strings.Join(append([]string{typedPrefix}, positionals[1:]...), " ")
		}
		return groupedArgumentCandidates(ctx, spec, prefix, content, rec, used, opts), true
	}
	return spacedArgumentCandidates(ctx, current, rec, used, opts), true
}

func groupedArgumentCandidates(ctx context.Context, spec shellSpec, prefix, content string, rec recipe.Recipe, used map[string]bool, opts Options) []Candidate {
	before, active := splitGroupedContent(spec, content)
	tokenPrefix := prefix + before
	if key, valuePrefix, ok := strings.Cut(active, "="); ok {
		arg, exists := rec.Arguments[key]
		if !exists {
			return nil
		}
		return valueCandidates(ctx, tokenPrefix+key+"=", valuePrefix, arg, rec, opts)
	}
	if active != "" {
		if candidates := positionalValueCandidates(tokenPrefix, active, rec, used, opts); len(candidates) > 0 {
			return candidates
		}
	}
	return argumentNameCandidates(tokenPrefix, active, rec, used)
}

func spacedArgumentCandidates(ctx context.Context, current string, rec recipe.Recipe, used map[string]bool, opts Options) []Candidate {
	if key, valuePrefix, ok := strings.Cut(current, "="); ok {
		arg, exists := rec.Arguments[key]
		if !exists {
			return nil
		}
		return valueCandidates(ctx, key+"=", valuePrefix, arg, rec, opts)
	}
	if current != "" {
		if candidates := positionalValueCandidates("", current, rec, used, opts); len(candidates) > 0 {
			return candidates
		}
	}
	return argumentNameCandidates("", current, rec, used)
}

func splitGroupedContent(spec shellSpec, content string) (string, string) {
	content = strings.TrimSuffix(content, spec.groupClose)
	idx := strings.LastIndexByte(content, ',')
	if idx < 0 {
		return "", strings.TrimSpace(content)
	}
	before := content[:idx+1]
	active := strings.TrimSpace(content[idx+1:])
	if active == "" && !strings.HasSuffix(before, " ") {
		before += " "
	}
	return before, active
}

func cutGroupedStart(spec shellSpec, text string) (string, string, bool) {
	open := strings.Index(text, spec.groupOpen)
	if open < 1 {
		return "", "", false
	}
	return text[:open], text[open+len(spec.groupOpen):], true
}

func argumentNameCandidates(prefix, current string, rec recipe.Recipe, used map[string]bool) []Candidate {
	names := mapsKeys(rec.Arguments)
	slices.Sort(names)
	var candidates []Candidate
	for _, name := range names {
		if used[name] {
			continue
		}
		value := prefix + name + "="
		if current != "" && !strings.HasPrefix(name, strings.TrimSuffix(current, "=")) {
			continue
		}
		candidates = append(candidates, Candidate{
			Value: value,
			Help:  recipe.ArgumentHelp(rec.Arguments[name]),
		})
	}
	return candidates
}

func valueCandidates(ctx context.Context, prefix, valuePrefix string, arg recipe.Argument, rec recipe.Recipe, opts Options) []Candidate {
	if len(arg.Values) > 0 {
		return dynamicValueCandidates(ctx, prefix, valuePrefix, arg, rec, opts)
	}
	switch argumentType(arg) {
	case "bool":
		return filterPrefix([]Candidate{
			{Value: prefix + "true", Help: "bool"},
			{Value: prefix + "false", Help: "bool"},
		}, prefix+valuePrefix)
	case "path", "rel_path":
		return pathValueCandidates(prefix, valuePrefix, arg, opts)
	default:
		return nil
	}
}

func dynamicValueCandidates(ctx context.Context, prefix, valuePrefix string, arg recipe.Argument, rec recipe.Recipe, opts Options) []Candidate {
	command := recipe.CommandWithShell(arg.Values, rec.Shell, rec.ShellPrelude)
	env := maps.Clone(opts.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, rec.Env)
	output, err := recipe.CommandOutput(ctx, opts.Dir, env, command)
	if err != nil {
		return nil
	}
	var candidates []Candidate
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		value, help, _ := strings.Cut(line, "\t")
		if valuePrefix != "" && !strings.HasPrefix(value, valuePrefix) {
			continue
		}
		candidates = append(candidates, Candidate{
			Value: prefix + value,
			Help:  help,
		})
	}
	return candidates
}

func usedArguments(spec shellSpec, tokens []string, current string, rec recipe.Recipe) map[string]bool {
	used := map[string]bool{}
	positionals := recipe.PositionalArguments(rec.Arguments)
	nextPositional := 0
	for _, token := range tokens {
		token = strings.TrimSuffix(strings.TrimSpace(token), spec.groupClose)
		if token == "" || token == current {
			continue
		}
		for part := range strings.SplitSeq(token, ",") {
			part = strings.TrimSpace(strings.TrimSuffix(part, spec.groupClose))
			if part == "" || part == current {
				continue
			}
			if key, _, ok := strings.Cut(part, "="); ok {
				used[key] = true
				continue
			}
			if nextPositional < len(positionals) {
				used[positionals[nextPositional]] = true
				nextPositional++
			}
		}
	}
	return used
}

func positionalValueCandidates(prefix, valuePrefix string, rec recipe.Recipe, used map[string]bool, opts Options) []Candidate {
	for _, name := range recipe.PositionalArguments(rec.Arguments) {
		if used[name] {
			continue
		}
		arg := rec.Arguments[name]
		if argumentType(arg) != "path" && argumentType(arg) != "rel_path" {
			return nil
		}
		return pathValueCandidates(prefix, valuePrefix, arg, opts)
	}
	return nil
}

func pathValueCandidates(prefix, valuePrefix string, arg recipe.Argument, opts Options) []Candidate {
	argType := argumentType(arg)
	if argType == "path" && valuePrefix == "~" {
		return []Candidate{{Value: prefix + "~/", Help: "home"}}
	}
	dir, valueDir, entryPrefix, ok := pathCompletionDir(valuePrefix, argType, opts.Dir)
	if !ok {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	var candidates []Candidate
	showHidden := strings.HasPrefix(entryPrefix, ".")
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !showHidden {
			continue
		}
		if !strings.HasPrefix(name, entryPrefix) {
			continue
		}
		if !includePathEntry(entry, arg.PathKind) {
			continue
		}
		value := valueDir + name
		help := "file"
		if entry.IsDir() {
			value += "/"
			help = "directory"
		}
		candidates = append(candidates, Candidate{Value: prefix + value, Help: help})
	}
	return candidates
}

func includePathEntry(entry os.DirEntry, kind string) bool {
	if entry.IsDir() {
		return true
	}
	switch pathKind(kind) {
	case "any":
		return true
	case "file":
		info, err := entry.Info()
		return err == nil && info.Mode().IsRegular()
	case "dir":
		return false
	case "executable":
		info, err := entry.Info()
		return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
	default:
		return true
	}
}

func pathKind(kind string) string {
	if kind == "" {
		return "any"
	}
	return kind
}

func pathCompletionDir(valuePrefix, argType, cwd string) (string, string, string, bool) {
	if cwd == "" {
		cwd = "."
	}
	switch {
	case strings.HasPrefix(valuePrefix, "~"):
		if argType != "path" || !strings.HasPrefix(valuePrefix, "~/") {
			return "", "", "", false
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", "", false
		}
		dirPart, entryPrefix := filepath.Split(strings.TrimPrefix(valuePrefix, "~/"))
		return filepath.Join(home, dirPart), "~/" + dirPart, entryPrefix, true
	case filepath.IsAbs(valuePrefix):
		if argType != "path" {
			return "", "", "", false
		}
		dirPart, entryPrefix := filepath.Split(valuePrefix)
		return dirPart, dirPart, entryPrefix, true
	default:
		dirPart, entryPrefix := filepath.Split(valuePrefix)
		return filepath.Join(cwd, dirPart), dirPart, entryPrefix, true
	}
}

func argumentType(arg recipe.Argument) string {
	if arg.Type == "" {
		return "string"
	}
	return arg.Type
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
		case "--sync-out-all", "--print", "--verbose", "--help", "--version":
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
	word := rawCurrentWord(words)
	if word == "" {
		return ""
	}
	if filepath.Base(word) == word {
		return word
	}
	return ""
}

func rawCurrentWord(words []string) string {
	if len(words) == 0 {
		return ""
	}
	return words[len(words)-1]
}

func normalizedWords(spec shellSpec, words []string) []string {
	out := make([]string, len(words))
	for i, word := range words {
		out[i] = spec.normalize(word)
	}
	return out
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
