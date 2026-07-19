package completion

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/shadowtree/internal/globalflag"
	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/runner"
)

type Candidate struct {
	Value string
	Help  string
}

type Options struct {
	Dir                        string
	ConfigPath                 string
	Env                        map[string]string
	EnumSets                   map[string]recipe.Command
	DisableCommandBackedValues bool
	CommandBackedValueTimeout  time.Duration
}

// DefaultCommandBackedValueTimeout bounds shell commands used to produce argument values.
const DefaultCommandBackedValueTimeout = 5 * time.Second

type Request struct {
	Shell       string
	Words       []string
	bashCurrent string
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

var bashShell = shellSpec{
	name:       "bash",
	groupOpen:  "[",
	groupClose: "]",
	normalize:  func(word string) string { return word },
}

var zshShell = shellSpec{
	name:       "zsh",
	groupOpen:  "[",
	groupClose: "]",
	normalize:  func(word string) string { return word },
}

func shellFor(name string) (shellSpec, error) {
	switch name {
	case "bash":
		return bashShell, nil
	case "fish":
		return fishShell, nil
	case "zsh":
		return zshShell, nil
	default:
		return shellSpec{}, fmt.Errorf("unsupported shell: %s", name)
	}
}

func Script(w io.Writer, shell string) error {
	switch shell {
	case "bash":
		return bashScript(w)
	case "fish":
		return fishScript(w)
	case "zsh":
		return zshScript(w)
	default:
		return fmt.Errorf("unsupported shell: %s", shell)
	}
}

func fishScript(w io.Writer) error {
	if _, err := io.WriteString(w, `function __shadowtree_complete
    set -l tokens (commandline -opc)
    set -l current (commandline -ct)
    shadowtree __complete fish $tokens $current
end

function __shadowtree_global_options
    set -l skip_next 0
    for token in (commandline -opc)[2..-1]
        if test $skip_next -eq 1
            set skip_next 0
            continue
        end
        switch $token
            case --
                return 1
`); err != nil {
		return err
	}
	var valueFlags []string
	for _, spec := range globalflag.All() {
		if spec.TakesValue() {
			valueFlags = append(valueFlags, "--"+spec.Name)
		}
	}
	if len(valueFlags) > 0 {
		if _, err := fmt.Fprintf(w, "            case %s\n", strings.Join(valueFlags, " ")); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, `                if not string match -q -- '*=*' $token
                    set skip_next 1
                end
                continue
            case '-*'
                continue
            case '*'
                return 1
        end
    end
    return 0
end

complete -c shadowtree -f -a '(__shadowtree_complete)'
`); err != nil {
		return err
	}
	for _, spec := range globalflag.All() {
		if _, err := fmt.Fprintf(w, "complete -c shadowtree -n __shadowtree_global_options -l %s", spec.Name); err != nil {
			return err
		}
		if spec.FishOptions != "" {
			if _, err := fmt.Fprintf(w, " %s", spec.FishOptions); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, " -d '%s'\n", spec.Help); err != nil {
			return err
		}
	}
	return nil
}

func zshScript(w io.Writer) error {
	_, err := io.WriteString(w, `#compdef shadowtree

_shadowtree() {
    local command_name value description record ret=1
    local -a nospace_records space_records

    command_name=${words[1]:-shadowtree}
    while IFS=$'\t' read -r value description; do
        if [[ -z $value ]]; then
            continue
        fi
        record=${value//\\/\\\\}
        record=${record//:/\\:}
        if [[ -n $description ]]; then
            record+=":${description}"
        fi
        if [[ $value == *= || $value == */ ]]; then
            nospace_records+=("$record")
        else
            space_records+=("$record")
        fi
    done < <("$command_name" __complete zsh "${words[@]}")

    if (( ${#nospace_records} )); then
        _describe -t commands 'shadowtree candidate' nospace_records -S '' && ret=0
    fi
    if (( ${#space_records} )); then
        _describe -t commands 'shadowtree candidate' space_records && ret=0
    fi
    return ret
}

if ! whence -w compdef >/dev/null 2>&1; then
    autoload -Uz compinit
    compinit
fi
compdef _shadowtree shadowtree
`)
	return err
}

func bashScript(w io.Writer) error {
	_, err := io.WriteString(w, `_shadowtree_complete() {
    local candidate candidate_line command_name
    command_name=${1:-shadowtree}
    COMPREPLY=()
    while IFS= read -r candidate_line; do
        candidate=${candidate_line%%$'\t'*}
        COMPREPLY+=("$candidate")
    done < <("$command_name" __complete bash "$COMP_POINT" "$COMP_LINE" "$2")
	for candidate in "${COMPREPLY[@]}"; do
	    case "$candidate" in
	        *=|*/) compopt -o nospace 2>/dev/null || true; break ;;
	    esac
	done
}

complete -F _shadowtree_complete shadowtree
`)
	return err
}

func WriteCandidates(w io.Writer, shell string, candidates []Candidate) error {
	switch shell {
	case "bash", "zsh":
		return writeTabCandidates(w, candidates)
	case "fish":
		return writeFishCandidates(w, candidates)
	default:
		return fmt.Errorf("unsupported shell: %s", shell)
	}
}

func writeFishCandidates(w io.Writer, candidates []Candidate) error {
	for _, candidate := range candidates {
		value := escapeFishValue(candidate.Value)
		desc := sanitizeCandidateHelp(candidate.Help)
		if _, err := fmt.Fprintf(w, "%s\t%s\n", value, desc); err != nil {
			return err
		}
	}
	return nil
}

func writeTabCandidates(w io.Writer, candidates []Candidate) error {
	for _, candidate := range candidates {
		desc := sanitizeCandidateHelp(candidate.Help)
		if _, err := fmt.Fprintf(w, "%s\t%s\n", candidate.Value, desc); err != nil {
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

func sanitizeCandidateHelp(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func ParseRequest(args []string) (Request, error) {
	if len(args) == 0 {
		return Request{}, fmt.Errorf("usage: shadowtree __complete <shell> <words...>")
	}
	shell := args[0]
	if _, err := shellFor(shell); err != nil {
		return Request{}, err
	}
	request := Request{
		Shell: shell,
		Words: args[1:],
	}
	if shell != "bash" {
		return request, nil
	}
	if len(args) != 3 && len(args) != 4 {
		return Request{}, fmt.Errorf("usage: shadowtree __complete bash <cursor> <line> [current]")
	}
	point, err := strconv.Atoi(args[1])
	if err != nil {
		return Request{}, fmt.Errorf("invalid bash cursor: %w", err)
	}
	request.Words = BashWords(args[2], point)
	if len(args) == 4 {
		request.bashCurrent = args[3]
	}
	return request, nil
}

func (request Request) StaticCandidates() ([]Candidate, bool, error) {
	return StaticCandidates(request.Shell, request.Words)
}

func (request Request) AdjustCandidates(candidates []Candidate) []Candidate {
	if request.Shell == "bash" {
		return BashReplacementCandidates(request.Words, request.bashCurrent, candidates)
	}
	return candidates
}

func Candidates(ctx context.Context, shell string, words []string, recipes map[string]recipe.Recipe, opts Options) ([]Candidate, error) {
	spec, err := shellFor(shell)
	if err != nil {
		return nil, err
	}
	if candidates, ok := staticCandidates(spec, words); ok {
		return candidates, nil
	}
	if candidates, ok := helpCandidates(words, recipes); ok {
		return candidates, nil
	}
	if candidates, ok := argumentCandidates(ctx, spec, words, recipes, opts); ok {
		return candidates, nil
	}
	if commandSelected(words) {
		return nil, nil
	}
	candidates := []Candidate{
		{Value: "exec", Help: "Run an explicit command in a shadow workspace"},
		{Value: "help", Help: "Show CLI and recipe help"},
		{Value: "recipes", Help: "List resolved recipes"},
		{Value: "init", Help: "Create .shadowtree.toml"},
		{Value: "config", Help: "Print resolved config"},
		{Value: "completion", Help: "Generate shell completion"},
	}
	candidates = append(candidates, recipeCandidates(words, recipes)...)
	return filterPrefix(candidates, currentWord(words)), nil
}

// StaticCandidates returns completions that do not require recipe resolution.
func StaticCandidates(shell string, words []string) ([]Candidate, bool, error) {
	spec, err := shellFor(shell)
	if err != nil {
		return nil, false, err
	}
	candidates, ok := staticCandidates(spec, words)
	return candidates, ok, nil
}

func staticCandidates(spec shellSpec, words []string) ([]Candidate, bool) {
	if len(positionalWords(words)) > 0 {
		return nil, false
	}
	if completesFlagValue(words, globalflag.Profile) {
		return []Candidate{
			{Value: recipe.GoProfile, Help: "Go project"},
			{Value: recipe.NodeProfile, Help: "Node project"},
			{Value: recipe.RustProfile, Help: "Rust project"},
		}, true
	}
	if completesFlagValue(words, globalflag.Config) || completesFlagValue(words, globalflag.SyncOut) {
		return nil, true
	}
	if candidates, ok := flagCandidates(spec, words); ok {
		return candidates, true
	}
	return nil, false
}

func flagCandidates(spec shellSpec, words []string) ([]Candidate, bool) {
	if spec.name == "fish" {
		return nil, false
	}
	current := currentWord(words)
	if !strings.HasPrefix(current, "-") {
		return nil, false
	}
	candidates := make([]Candidate, 0, len(globalflag.All()))
	for _, spec := range globalflag.All() {
		candidates = append(candidates, Candidate{
			Value: "--" + spec.Name,
			Help:  spec.Help,
		})
	}
	return filterPrefix(candidates, current), true
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
	if !exists || len(rec.Arguments) == 0 && len(rec.Presets) == 0 {
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
		return groupedArgumentCandidates(ctx, spec, prefix, content, rec, recipes, used, opts), true
	}
	return spacedArgumentCandidates(ctx, current, rec, recipes, used, opts), true
}

func groupedArgumentCandidates(ctx context.Context, spec shellSpec, prefix, content string, rec recipe.Recipe, recipes map[string]recipe.Recipe, used map[string]bool, opts Options) []Candidate {
	before, active := splitGroupedContent(spec, content)
	tokenPrefix := prefix + before
	if key, valuePrefix, ok := strings.Cut(active, "="); ok {
		if key == recipe.PresetArgumentName && len(rec.Presets) > 0 {
			valuePrefix, prefix = splitQuotedValuePrefix(valuePrefix, tokenPrefix+key+"=")
			return presetValueCandidates(prefix, valuePrefix, rec)
		}
		arg, exists := rec.Arguments[key]
		if !exists {
			return nil
		}
		valuePrefix, prefix = splitQuotedValuePrefix(valuePrefix, tokenPrefix+key+"=")
		return valueCandidates(ctx, prefix, valuePrefix, arg, rec, recipes, opts)
	}
	if candidates := argumentNameCandidates(tokenPrefix, active, rec, used); len(candidates) > 0 {
		return candidates
	}
	if active != "" {
		if candidates := positionalValueCandidates(ctx, tokenPrefix, active, rec, recipes, used, opts); len(candidates) > 0 {
			return candidates
		}
	}
	return nil
}

// GroupedArgumentCandidates completes bracket-style recipe arguments.
func GroupedArgumentCandidates(ctx context.Context, prefix, content string, rec recipe.Recipe, recipes map[string]recipe.Recipe, opts Options) []Candidate {
	before, _ := splitGroupedContent(bashShell, content)
	used := usedArguments(bashShell, []string{before}, "", rec)
	return groupedArgumentCandidates(ctx, bashShell, prefix, content, rec, recipes, used, opts)
}

func spacedArgumentCandidates(ctx context.Context, current string, rec recipe.Recipe, recipes map[string]recipe.Recipe, used map[string]bool, opts Options) []Candidate {
	if key, valuePrefix, ok := strings.Cut(current, "="); ok {
		if key == recipe.PresetArgumentName && len(rec.Presets) > 0 {
			valuePrefix, prefix := splitQuotedValuePrefix(valuePrefix, key+"=")
			return presetValueCandidates(prefix, valuePrefix, rec)
		}
		arg, exists := rec.Arguments[key]
		if !exists {
			return nil
		}
		valuePrefix, prefix := splitQuotedValuePrefix(valuePrefix, key+"=")
		return valueCandidates(ctx, prefix, valuePrefix, arg, rec, recipes, opts)
	}
	if current == "" {
		if candidates := positionalValueCandidates(ctx, "", current, rec, recipes, used, opts); len(candidates) > 0 {
			return candidates
		}
	}
	if candidates := argumentNameCandidates("", current, rec, used); len(candidates) > 0 {
		return candidates
	}
	if current != "" {
		if candidates := positionalValueCandidates(ctx, "", current, rec, recipes, used, opts); len(candidates) > 0 {
			return candidates
		}
	}
	return nil
}

func splitQuotedValuePrefix(valuePrefix, prefix string) (string, string) {
	if valuePrefix == "" || (valuePrefix[0] != '\'' && valuePrefix[0] != '"') {
		return valuePrefix, prefix
	}
	return valuePrefix[1:], prefix + valuePrefix[:1]
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
	names := slices.Collect(maps.Keys(rec.Arguments))
	if len(rec.Presets) > 0 && !used[recipe.PresetArgumentName] {
		names = append(names, recipe.PresetArgumentName)
	}
	slices.Sort(names)
	matchName := strings.TrimSuffix(current, "=")
	var candidates []Candidate
	for _, name := range names {
		if used[name] {
			continue
		}
		value := prefix + name + "="
		if current != "" && !strings.HasPrefix(name, matchName) {
			continue
		}
		candidates = append(candidates, Candidate{
			Value: value,
			Help:  argumentNameHelp(rec, name),
		})
	}
	return candidates
}

func argumentNameHelp(rec recipe.Recipe, name string) string {
	if name == recipe.PresetArgumentName && len(rec.Presets) > 0 {
		return "recipe preset"
	}
	return recipe.ArgumentHelp(rec.Arguments[name])
}

func presetValueCandidates(prefix, valuePrefix string, rec recipe.Recipe) []Candidate {
	matchPrefix := prefix + valuePrefix
	var names []string
	for name := range rec.Presets {
		if strings.HasPrefix(prefix+name, matchPrefix) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	candidates := make([]Candidate, 0, len(names))
	for _, name := range names {
		candidates = append(candidates, Candidate{Value: prefix + name, Help: "recipe preset"})
	}
	return filterPrefix(candidates, matchPrefix)
}

func valueCandidates(ctx context.Context, prefix, valuePrefix string, arg recipe.Argument, rec recipe.Recipe, recipes map[string]recipe.Recipe, opts Options) []Candidate {
	if len(arg.Values) > 0 {
		return dynamicValueCandidates(ctx, prefix, valuePrefix, arg, rec, recipes, opts)
	}
	return staticValueCandidates(prefix, valuePrefix, arg, opts)
}

func staticValueCandidates(prefix, valuePrefix string, arg recipe.Argument, opts Options) []Candidate {
	switch recipe.ArgumentType(arg) {
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

func dynamicValueCandidates(ctx context.Context, prefix, valuePrefix string, arg recipe.Argument, rec recipe.Recipe, recipes map[string]recipe.Recipe, opts Options) []Candidate {
	if values, ok, err := recipe.BuiltinValues(arg.Values, recipe.ValueBuiltinOptions{
		Context:     ctx,
		Dir:         opts.Dir,
		ConfigPath:  opts.ConfigPath,
		Recipe:      rec,
		Recipes:     recipes,
		EnumSets:    opts.EnumSets,
		ValuePrefix: valuePrefix,
	}); ok {
		if err != nil {
			return nil
		}
		candidates := make([]Candidate, 0, len(values))
		for _, value := range values {
			candidates = append(candidates, Candidate{Value: prefix + value.Value, Help: value.Help})
		}
		return candidates
	}
	if recipes == nil {
		return nil
	}
	if opts.DisableCommandBackedValues {
		return nil
	}
	command, err := recipe.CommandWithRecipeReferenceExpandedPrelude(arg.Values, rec.Shell, rec.ShellPrelude, rec.Vars)
	if err != nil {
		return nil
	}
	env := maps.Clone(opts.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, rec.Env)
	timeout := opts.CommandBackedValueTimeout
	if timeout == 0 {
		timeout = DefaultCommandBackedValueTimeout
	}
	valueCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := runner.CommandOutput(valueCtx, opts.Dir, env, command, runner.CommandOutputOptions{
		Recipes:    recipes,
		EnumSets:   opts.EnumSets,
		ConfigPath: opts.ConfigPath,
		SourceDir:  opts.Dir,
	})
	if err != nil {
		return nil
	}
	values := recipe.ParseValueCandidates(output)
	var candidates []Candidate
	for _, value := range values {
		if valuePrefix != "" && !strings.HasPrefix(value.Value, valuePrefix) {
			continue
		}
		candidates = append(candidates, Candidate{
			Value: prefix + value.Value,
			Help:  value.Help,
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

func positionalValueCandidates(ctx context.Context, prefix, valuePrefix string, rec recipe.Recipe, recipes map[string]recipe.Recipe, used map[string]bool, opts Options) []Candidate {
	for _, name := range recipe.PositionalArguments(rec.Arguments) {
		if used[name] {
			continue
		}
		arg := rec.Arguments[name]
		return valueCandidates(ctx, prefix, valuePrefix, arg, rec, recipes, opts)
	}
	return nil
}

func pathValueCandidates(prefix, valuePrefix string, arg recipe.Argument, opts Options) []Candidate {
	argType := recipe.ArgumentType(arg)
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
	switch kind {
	case "", "any":
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

func completesFlagValue(words []string, name string) bool {
	if len(words) == 0 {
		return false
	}
	flag := "--" + name
	if words[len(words)-1] == flag {
		return true
	}
	return len(words) > 1 && words[len(words)-1] == "" && words[len(words)-2] == flag
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

func helpCandidates(words []string, recipes map[string]recipe.Recipe) ([]Candidate, bool) {
	positionals := positionalWords(words)
	current := currentWord(words)
	if len(positionals) == 0 || positionals[0] != "help" {
		return nil, false
	}

	optionPositionals := positionals
	if current != "" && optionPositionals[len(optionPositionals)-1] == current {
		optionPositionals = optionPositionals[:len(optionPositionals)-1]
	}
	if len(optionPositionals) == 2 {
		if _, ok := recipes[optionPositionals[1]]; ok {
			candidates := filterPrefix([]Candidate{{Value: "color=false", Help: "Disable help color"}}, current)
			if len(candidates) > 0 {
				return candidates, true
			}
		}
	}

	if len(positionals) <= 2 {
		return recipeCandidates(words, recipes), true
	}
	return nil, false
}

func recipeCandidates(words []string, recipes map[string]recipe.Recipe) []Candidate {
	var candidates []Candidate
	names := slices.Sorted(maps.Keys(recipes))
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
		if len(positionals) > 0 {
			positionals = append(positionals, word)
			continue
		}
		if skipValue {
			skipValue = false
			continue
		}
		if strings.HasPrefix(word, "-") {
			if globalflag.TakesValue(word) {
				skipValue = true
				continue
			}
			if _, ok := globalflag.Lookup(word); ok {
				continue
			}
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

// BashReplacementCandidates converts full logical candidates into replacements
// for Bash's active word. Bash treats '=' as a word break, so value completion
// must return only the value side even though Candidates works with name=value.
func BashReplacementCandidates(words []string, bashCurrent string, candidates []Candidate) []Candidate {
	if len(candidates) == 0 {
		return candidates
	}
	current := rawCurrentWord(words)
	idx := strings.LastIndexByte(current, '=')
	if idx < 0 {
		return candidates
	}
	prefix := current[:idx+1]
	valuePrefix := current[idx+1:]
	if valuePrefix != "" && !strings.HasSuffix(valuePrefix, bashCurrent) {
		return candidates
	}
	out := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if !strings.HasPrefix(candidate.Value, prefix) {
			out = append(out, candidate)
			continue
		}
		candidate.Value = strings.TrimPrefix(candidate.Value, prefix)
		out = append(out, candidate)
	}
	return out
}

// BashWords splits COMP_LINE up to COMP_POINT into the word shape expected by
// Candidates. It avoids Bash's COMP_WORDS splitting on '=' so name=value
// arguments complete the same way as fish.
func BashWords(line string, point int) []string {
	if point < 0 || point > len(line) {
		point = len(line)
	}
	var words []string
	var current strings.Builder
	haveWord := false
	trailingSpace := false
	escaped := false
	inSingle := false
	inDouble := false
	for _, r := range line[:point] {
		if escaped {
			current.WriteRune(r)
			haveWord = true
			trailingSpace = false
			escaped = false
			continue
		}
		switch {
		case r == '\\' && !inSingle:
			escaped = true
			haveWord = true
			trailingSpace = false
		case r == '\'' && !inDouble:
			current.WriteRune(r)
			haveWord = true
			trailingSpace = false
			inSingle = !inSingle
		case r == '"' && !inSingle:
			current.WriteRune(r)
			haveWord = true
			trailingSpace = false
			inDouble = !inDouble
		case isBashSpace(r) && !inSingle && !inDouble:
			if haveWord {
				words = append(words, current.String())
				current.Reset()
				haveWord = false
			}
			trailingSpace = true
		default:
			current.WriteRune(r)
			haveWord = true
			trailingSpace = false
		}
	}
	if escaped {
		current.WriteByte('\\')
	}
	if haveWord || len(words) == 0 || trailingSpace {
		words = append(words, current.String())
	}
	return words
}

func isBashSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
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
