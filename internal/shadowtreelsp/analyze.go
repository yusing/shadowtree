package shadowtreelsp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/BurntSushi/toml"
	recipecompletion "github.com/yusing/shadowtree/internal/completion"
	"github.com/yusing/shadowtree/internal/configfile"
	"github.com/yusing/shadowtree/internal/recipe"
)

type documentAnalysis struct {
	Lines         []string
	Recipes       []string
	GlobalVars    []string
	RecipeVars    map[string][]string
	Arguments     map[string][]string
	ScriptRegions []scriptRegion
	CurrentTable  string
}

type completion struct {
	Label           string
	InsertText      string
	Kind            int
	Detail          string
	Quote           bool
	RecipeReference bool
	Edit            *completionEdit
}

type completionEdit struct {
	Start int
	End   int
}

type completionOptions struct {
	Dir        string
	ConfigPath string
}

const (
	completionKindFunction = 3
	completionKindField    = 5
	completionKindVariable = 6
	completionKindValue    = 12
	completionKindKeyword  = 14
)

var topKeys = []completion{
	{Label: "profile", InsertText: `profile = "go"`, Kind: completionKindKeyword, Detail: "Shadowtree profile"},
	{Label: "shell", InsertText: `shell = "sh"`, Kind: completionKindKeyword, Detail: "Shell for script commands"},
	{Label: "shell_prelude", InsertText: "shell_prelude = '''\n\n'''", Kind: completionKindKeyword, Detail: "Shared shell code"},
	{Label: "sync_out", InsertText: "sync_out = []", Kind: completionKindKeyword, Detail: "Global sync-out paths"},
}

var recipeKeys = []completion{
	{Label: "help", InsertText: `help = ""`, Kind: completionKindKeyword, Detail: "Recipe help text"},
	{Label: "shell", InsertText: `shell = "sh"`, Kind: completionKindKeyword, Detail: "Recipe shell"},
	{Label: "shell_prelude", InsertText: "shell_prelude = '''\n\n'''", Kind: completionKindKeyword, Detail: "Recipe shell prelude"},
	{Label: "sandboxed", InsertText: "sandboxed = true", Kind: completionKindKeyword, Detail: "Run in disposable workspace"},
	{Label: "cmd", InsertText: `cmd = [""]`, Kind: completionKindKeyword, Detail: "Main command"},
	{Label: "args", InsertText: "args = []", Kind: completionKindKeyword, Detail: "Fixed args"},
	{Label: "default_args", InsertText: "default_args = []", Kind: completionKindKeyword, Detail: "Default CLI args"},
	{Label: "pre", InsertText: "pre = []", Kind: completionKindKeyword, Detail: "Commands before main"},
	{Label: "post", InsertText: "post = []", Kind: completionKindKeyword, Detail: "Commands after main"},
	{Label: "sync_out", InsertText: "sync_out = []", Kind: completionKindKeyword, Detail: "Recipe sync-out paths"},
}

var argumentKeys = []completion{
	{Label: "help", InsertText: `help = ""`, Kind: completionKindKeyword, Detail: "Argument help text"},
	{Label: "type", InsertText: `type = "string"`, Kind: completionKindKeyword, Detail: "Argument type"},
	{Label: "path_kind", InsertText: `path_kind = "any"`, Kind: completionKindKeyword, Detail: "Path completion filter"},
	{Label: "position", InsertText: "position = 1", Kind: completionKindKeyword, Detail: "1-based positional index"},
	{Label: "required", InsertText: "required = false", Kind: completionKindKeyword, Detail: "Whether the argument is required"},
	{Label: "default", InsertText: `default = ""`, Kind: completionKindKeyword, Detail: "Default value"},
	{Label: "values", InsertText: "values = '''\n\n'''", Kind: completionKindKeyword, Detail: "Dynamic completion command"},
}

var shellValues = []completion{
	{Label: "sh", InsertText: "sh", Kind: completionKindValue, Detail: "POSIX shell", Quote: true},
	{Label: "bash", InsertText: "bash", Kind: completionKindValue, Detail: "Bash", Quote: true},
	{Label: "fish", InsertText: "fish", Kind: completionKindValue, Detail: "Fish", Quote: true},
}

var argumentTypeValues = []completion{
	{Label: "string", InsertText: "string", Kind: completionKindValue, Detail: "String argument", Quote: true},
	{Label: "int", InsertText: "int", Kind: completionKindValue, Detail: "Integer argument", Quote: true},
	{Label: "float", InsertText: "float", Kind: completionKindValue, Detail: "Float argument", Quote: true},
	{Label: "bool", InsertText: "bool", Kind: completionKindValue, Detail: "Boolean argument", Quote: true},
	{Label: "path", InsertText: "path", Kind: completionKindValue, Detail: "Absolute or relative path argument", Quote: true},
	{Label: "rel_path", InsertText: "rel_path", Kind: completionKindValue, Detail: "Relative path argument", Quote: true},
}

var pathKindValues = []completion{
	{Label: "any", InsertText: "any", Kind: completionKindValue, Detail: "Files and directories", Quote: true},
	{Label: "file", InsertText: "file", Kind: completionKindValue, Detail: "Files with directory traversal", Quote: true},
	{Label: "dir", InsertText: "dir", Kind: completionKindValue, Detail: "Directories", Quote: true},
	{Label: "executable", InsertText: "executable", Kind: completionKindValue, Detail: "Executable files with directory traversal", Quote: true},
}

var boolValues = []completion{
	{Label: "true", InsertText: "true", Kind: completionKindValue, Detail: "Boolean true"},
	{Label: "false", InsertText: "false", Kind: completionKindValue, Detail: "Boolean false"},
}

func analyzeDocument(text string, line int) documentAnalysis {
	lines := strings.Split(text, "\n")
	analysis := documentAnalysis{
		Lines:      lines,
		RecipeVars: map[string][]string{},
		Arguments:  map[string][]string{},
	}
	table := ""
	for i, raw := range lines {
		if parsed, ok := completeTableHeader(raw); ok {
			table = parsed
			if strings.HasPrefix(parsed, "recipes.") {
				parts := strings.Split(parsed, ".")
				if len(parts) >= 2 {
					analysis.Recipes = appendUnique(analysis.Recipes, parts[1])
				}
				if len(parts) >= 4 && parts[2] == "arguments" {
					analysis.Arguments[parts[1]] = appendUnique(analysis.Arguments[parts[1]], parts[3])
				}
			}
		}
		if key, ok := pairKey(raw); ok {
			switch {
			case table == "vars" || table == "var_commands":
				analysis.GlobalVars = appendUnique(analysis.GlobalVars, key)
			case strings.HasPrefix(table, "recipes."):
				parts := strings.Split(table, ".")
				if len(parts) == 3 && parts[2] == "vars" {
					analysis.RecipeVars[parts[1]] = appendUnique(analysis.RecipeVars[parts[1]], key)
				}
			}
		}
		if i == line {
			analysis.CurrentTable = table
		}
	}
	slices.Sort(analysis.Recipes)
	slices.Sort(analysis.GlobalVars)
	for name := range analysis.RecipeVars {
		slices.Sort(analysis.RecipeVars[name])
	}
	for name := range analysis.Arguments {
		slices.Sort(analysis.Arguments[name])
	}
	analysis.ScriptRegions = scriptRegions(lines, shellSettings(lines))
	return analysis
}

func completionsAt(ctx context.Context, text string, pos lspPosition) []completion {
	return completionsAtWithOptions(ctx, text, pos, completionOptions{})
}

func completionsAtWithOptions(ctx context.Context, text string, pos lspPosition, opts completionOptions) []completion {
	analysis := analyzeDocument(text, pos.Line)
	line := lineAt(analysis.Lines, pos.Line)
	prefix := linePrefix(line, pos.Character)
	if tablePrefix, ok := openTablePrefix(prefix); ok {
		return tableCompletions(analysis, tablePrefix)
	}
	if items, ok := recipeArgumentReferenceCompletions(ctx, text, analysis.CurrentTable, prefix, pos, opts); ok {
		return items
	}
	if items, ok := scriptRecipeReferenceCompletions(ctx, text, analysis, pos, opts); ok {
		return items
	}
	if recipePrefix, ok := recipeReferencePrefix(analysis.CurrentTable, prefix); ok {
		return recipeReferenceCompletionsWithOptions(ctx, text, analysis, recipePrefix, opts)
	}
	if variablePrefix, ok := placeholderPrefix(prefix); ok {
		return placeholderCompletions(analysis, currentRecipe(analysis.CurrentTable), variablePrefix)
	}
	if key, ok := keyBeforeValue(prefix); ok {
		return valueCompletions(key)
	}
	if inScriptRegion(analysis.Lines, analysis.ScriptRegions, pos) {
		return nil
	}
	return keyCompletions(analysis.CurrentTable)
}

func recipeArgumentReferenceCompletions(ctx context.Context, text, table, prefix string, pos lspPosition, opts completionOptions) ([]completion, bool) {
	key, ok := keyBeforeValue(prefix)
	if !ok || !recipeReferenceKey(table, key) {
		return nil, false
	}
	quote := lastOpenQuote(prefix)
	if quote < 0 {
		return nil, false
	}
	value := prefix[quote+1:]
	if !strings.HasPrefix(value, "@") || !strings.Contains(value, "[") {
		return nil, false
	}
	fragment := strings.TrimPrefix(value, "@")
	recipes, ok := completionRecipes(text)
	if !ok {
		return nil, true
	}
	name, content, ok := cutReferenceGroup(fragment)
	if !ok {
		return nil, true
	}
	ref := recipeReferenceForCompletion(name)
	rec, ok := recipes[ref.Name]
	if ref.Path != "" {
		rec, ok = crossConfigCompletionRecipe(ctx, ref, opts)
	}
	if !ok || len(rec.Arguments) == 0 {
		return nil, true
	}
	candidates := recipecompletion.GroupedArgumentCandidates(
		name+"[",
		content,
		rec,
		recipecompletion.Options{Dir: opts.Dir},
	)
	return recipeArgumentCompletions(candidates, quote+len("@")+1, pos.Character), true
}

func scriptRecipeReferenceCompletions(ctx context.Context, text string, analysis documentAnalysis, pos lspPosition, opts completionOptions) ([]completion, bool) {
	region, ok := scriptRegionAt(analysis.Lines, analysis.ScriptRegions, pos)
	if !ok || !recipeScriptReferenceRegion(region) || (region.Shell != "" && region.Shell != "sh" && region.Shell != "bash") {
		return nil, false
	}
	line := lineAt(analysis.Lines, pos.Line)
	start, prefix, ok := scriptRecipeReferencePrefixAt(line, pos, region)
	if !ok {
		return nil, false
	}
	if strings.Contains(prefix, "[") {
		items := groupedScriptRecipeReferenceCompletions(ctx, text, prefix, pos, start, opts)
		return items, true
	}
	if strings.ContainsAny(prefix, " \t") {
		items := spacedScriptRecipeReferenceCompletions(ctx, text, prefix, pos, start, opts)
		return items, true
	}
	items := recipeReferenceCompletionsWithOptions(ctx, text, analysis, prefix, opts)
	for i := range items {
		items[i].Edit = &completionEdit{Start: start + 1, End: pos.Character}
	}
	return items, true
}

func groupedScriptRecipeReferenceCompletions(ctx context.Context, text, prefix string, pos lspPosition, start int, opts completionOptions) []completion {
	fakePrefix := `cmd = "@` + prefix
	fakeBase := len(`cmd = "@`)
	fakePos := lspPosition{Line: pos.Line, Character: len(fakePrefix)}
	items, ok := recipeArgumentReferenceCompletions(ctx, text, "recipes.__script", fakePrefix, fakePos, opts)
	if !ok {
		return nil
	}
	for i := range items {
		if items[i].Edit == nil {
			continue
		}
		items[i].Edit = &completionEdit{
			Start: start + 1 + max(items[i].Edit.Start-fakeBase, 0),
			End:   pos.Character,
		}
	}
	return items
}

func spacedScriptRecipeReferenceCompletions(ctx context.Context, text, prefix string, pos lspPosition, start int, opts completionOptions) []completion {
	name, argsText, ok := strings.Cut(prefix, " ")
	if !ok {
		name, argsText, ok = strings.Cut(prefix, "\t")
	}
	if !ok || strings.TrimSpace(name) == "" {
		return nil
	}
	recipes, ok := completionRecipes(text)
	if !ok {
		return nil
	}
	ref := recipeReferenceForCompletion(name)
	rec, ok := recipes[ref.Name]
	if ref.Path != "" {
		rec, ok = crossConfigCompletionRecipe(ctx, ref, opts)
	}
	if !ok || len(rec.Arguments) == 0 {
		return nil
	}
	currentStart := len(argsText)
	for currentStart > 0 && !isShellSpace(argsText[currentStart-1]) {
		currentStart--
	}
	before := strings.TrimSpace(argsText[:currentStart])
	active := argsText[currentStart:]
	content := active
	if before != "" {
		content = strings.Join(strings.Fields(before), ", ") + ", " + active
	}
	candidates := recipecompletion.GroupedArgumentCandidates("", content, rec, recipecompletion.Options{Dir: opts.Dir})
	editStart := start + 1 + len(name) + 1 + currentStart
	return recipeArgumentCompletions(candidates, editStart, pos.Character)
}

func recipeArgumentCompletions(candidates []recipecompletion.Candidate, editStart, editEnd int) []completion {
	items := make([]completion, 0, len(candidates))
	for _, candidate := range candidates {
		label := recipeArgumentCompletionLabel(candidate.Value)
		kind := completionKindValue
		if strings.HasSuffix(label, "=") {
			kind = completionKindVariable
		}
		items = append(items, completion{
			Label:      label,
			InsertText: candidate.Value,
			Kind:       kind,
			Detail:     candidate.Help,
			Edit:       &completionEdit{Start: editStart, End: editEnd},
		})
	}
	return items
}

func scriptRegionAt(lines []string, regions []scriptRegion, pos lspPosition) (scriptRegion, bool) {
	for _, region := range regions {
		if pos.Line < region.StartLine || pos.Line > region.EndLine {
			continue
		}
		line := lineAt(lines, pos.Line)
		character := min(pos.Character, len(line))
		if pos.Line == region.StartLine && character < region.StartCol {
			continue
		}
		if pos.Line == region.EndLine && character > region.EndCol {
			continue
		}
		return region, true
	}
	return scriptRegion{}, false
}

func inScriptRegion(lines []string, regions []scriptRegion, pos lspPosition) bool {
	_, ok := scriptRegionAt(lines, regions, pos)
	return ok
}

func scriptRecipeReferencePrefixAt(line string, pos lspPosition, region scriptRegion) (int, string, bool) {
	character := min(pos.Character, len(line))
	end := character
	start := 0
	if pos.Line == region.StartLine {
		start = region.StartCol
	}
	if pos.Line == region.EndLine {
		end = min(end, region.EndCol)
	}
	commandPosition := true
	activeStart := -1
	for col := start; col < end; {
		ch := line[col]
		if ch == '#' {
			break
		}
		if isShellSpace(ch) {
			col++
			continue
		}
		if ch == '\'' || ch == '"' {
			next := skipShellString(line, col, end, ch)
			if character <= next {
				return 0, "", false
			}
			col = next
			commandPosition = false
			continue
		}
		if commandPosition {
			if next, ok := scanShellAssignment(line, col, end); ok {
				if character <= next {
					return 0, "", false
				}
				col = next
				continue
			}
			if ch == '@' {
				activeStart = col
				break
			}
		}
		if ch == '$' {
			if token, ok := shellVariableToken(line, 0, col, end, "sh"); ok {
				col += token.Length
				commandPosition = false
				continue
			}
		}
		if isShellOperator(ch) {
			commandPosition = true
			col++
			continue
		}
		if isShellWordStart(ch) {
			wordStart := col
			for col < end && isShellWordPart(line[col]) {
				col++
			}
			word := line[wordStart:col]
			commandPosition = shellKeyword("sh", word) && commandContinuesAfterKeyword("sh", word)
			continue
		}
		col++
	}
	if activeStart < 0 || activeStart >= end {
		return 0, "", false
	}
	prefix := line[activeStart+1 : end]
	if strings.ContainsAny(prefix, "\r\n;|&()<>\"'") {
		return 0, "", false
	}
	return activeStart, prefix, true
}

func completionRecipes(text string) (map[string]recipe.Recipe, bool) {
	var cfg recipe.Config
	if _, err := toml.Decode(text, &cfg); err != nil {
		return nil, false
	}
	profile := cfg.Profile
	if profile == "" {
		profile = recipe.GoProfile
	}
	recipes, err := recipe.MergeRecipes(recipe.Builtins(profile), cfg.Recipes)
	if err != nil {
		return nil, false
	}
	return recipe.ApplyGlobals(recipes, cfg.Vars, cfg.Shell, cfg.ShellPrelude), true
}

func cutReferenceGroup(value string) (string, string, bool) {
	open := strings.IndexByte(value, '[')
	if open < 1 {
		return "", "", false
	}
	return value[:open], value[open+1:], true
}

func recipeArgumentCompletionLabel(value string) string {
	if open := strings.IndexByte(value, '['); open >= 0 {
		value = value[open+1:]
	}
	if comma := strings.LastIndexByte(value, ','); comma >= 0 {
		value = value[comma+1:]
	}
	value = strings.TrimSpace(strings.TrimSuffix(value, "]"))
	if _, suffix, ok := strings.Cut(value, "="); ok && suffix != "" {
		return suffix
	}
	return value
}

func tableCompletions(analysis documentAnalysis, prefix string) []completion {
	switch {
	case prefix == "":
		return []completion{
			{Label: "env", InsertText: "env]", Kind: completionKindField, Detail: "Environment variables"},
			{Label: "vars", InsertText: "vars]", Kind: completionKindVariable, Detail: "Shared placeholders"},
			{Label: "var_commands", InsertText: "var_commands]", Kind: completionKindFunction, Detail: "Dynamic placeholders"},
			{Label: "recipes", InsertText: "recipes.", Kind: completionKindField, Detail: "Recipe table"},
		}
	case prefix == "recipes" || prefix == "recipes.":
		items := []completion{
			{Label: "<name>", InsertText: "", Kind: completionKindField, Detail: "New recipe name"},
		}
		for _, recipe := range analysis.Recipes {
			items = append(items, completion{
				Label:      recipe,
				InsertText: recipe + "]",
				Kind:       completionKindField,
				Detail:     "Existing recipe",
			})
		}
		return items
	}
	recipe, rest, ok := strings.Cut(strings.TrimPrefix(prefix, "recipes."), ".")
	if !ok || recipe == "" {
		return nil
	}
	switch {
	case rest == "":
		return recipeSubtableCompletions()
	case rest == "arguments" || rest == "arguments.":
		items := []completion{
			{Label: "<name>", InsertText: "", Kind: completionKindField, Detail: "New argument name"},
		}
		for _, arg := range analysis.Arguments[recipe] {
			items = append(items, completion{
				Label:      arg,
				InsertText: arg + "]",
				Kind:       completionKindVariable,
				Detail:     "Existing argument",
			})
		}
		return items
	}
	return recipeSubtableCompletions()
}

func recipeReferenceCompletionsWithOptions(ctx context.Context, text string, analysis documentAnalysis, prefix string, opts completionOptions) []completion {
	if pathPrefix, recipePrefix, ok := strings.Cut(prefix, ":"); ok {
		return crossConfigRecipeReferenceCompletions(ctx, pathPrefix, recipePrefix, opts)
	}
	recipes, _ := completionRecipes(text)
	names := slices.Clone(analysis.Recipes)
	for _, name := range recipe.BuiltinNames(recipe.GoProfile) {
		names = appendUnique(names, name)
	}
	slices.Sort(names)
	var items []completion
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		items = append(items, completion{
			Label:           "@" + name,
			InsertText:      name,
			Kind:            completionKindFunction,
			Detail:          recipeReferenceDetail(recipes, name),
			RecipeReference: true,
		})
	}
	items = append(items, crossConfigDirectoryCompletions(prefix, opts)...)
	return items
}

func crossConfigDirectoryCompletions(prefix string, opts completionOptions) []completion {
	base := completionBaseDir(opts)
	if base == "" || strings.Contains(prefix, "{") {
		return nil
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var items []completion
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		configPath := filepath.Join(base, name, ".shadowtree.toml")
		if _, err := os.Stat(configPath); err != nil {
			continue
		}
		items = append(items, completion{
			Label:      "@" + name + ":",
			InsertText: name + ":",
			Kind:       completionKindField,
			Detail:     "Shadowtree config directory",
		})
	}
	return items
}

func crossConfigRecipeReferenceCompletions(ctx context.Context, pathPrefix, recipePrefix string, opts completionOptions) []completion {
	if pathPrefix == "" || strings.Contains(pathPrefix, "{") {
		return nil
	}
	recipes, ok := crossConfigCompletionRecipes(ctx, pathPrefix, opts)
	if !ok {
		return nil
	}
	names := mapsKeys(recipes)
	slices.Sort(names)
	var items []completion
	for _, name := range names {
		if !strings.HasPrefix(name, recipePrefix) {
			continue
		}
		value := pathPrefix + ":" + name
		items = append(items, completion{
			Label:           "@" + value,
			InsertText:      value,
			Kind:            completionKindFunction,
			Detail:          recipeReferenceDetail(recipes, name),
			RecipeReference: true,
		})
	}
	return items
}

func recipeReferenceDetail(recipes map[string]recipe.Recipe, name string) string {
	if rec, ok := recipes[name]; ok && rec.Help != "" {
		return rec.Help
	}
	return "Shadowtree recipe reference"
}

func crossConfigCompletionRecipe(ctx context.Context, ref recipe.RecipeReferenceTarget, opts completionOptions) (recipe.Recipe, bool) {
	recipes, ok := crossConfigCompletionRecipes(ctx, ref.Path, opts)
	if !ok {
		return recipe.Recipe{}, false
	}
	rec, ok := recipes[ref.Name]
	return rec, ok
}

func crossConfigCompletionRecipes(ctx context.Context, path string, opts completionOptions) (map[string]recipe.Recipe, bool) {
	base := completionBaseDir(opts)
	if base == "" {
		return nil, false
	}
	targetDir := path
	if !filepath.IsAbs(targetDir) {
		targetDir = filepath.Join(base, targetDir)
	}
	loaded, err := configfile.Load(filepath.Join(targetDir, ".shadowtree.toml"))
	if err != nil {
		return nil, false
	}
	recipes, _, err := configfile.ResolveRecipes(ctx, loaded, targetDir, configfile.ResolveOptions{})
	if err != nil {
		return nil, false
	}
	return recipes, true
}

func completionBaseDir(opts completionOptions) string {
	if opts.ConfigPath != "" {
		return filepath.Dir(opts.ConfigPath)
	}
	return opts.Dir
}

func recipeReferenceForCompletion(name string) recipe.RecipeReferenceTarget {
	ref, ok := recipe.ParseRecipeReference(recipe.Command{"@" + name})
	if !ok {
		return recipe.RecipeReferenceTarget{Name: name}
	}
	return ref
}

func recipeSubtableCompletions() []completion {
	return []completion{
		{Label: "vars", InsertText: "vars]", Kind: completionKindVariable, Detail: "Recipe placeholders"},
		{Label: "env", InsertText: "env]", Kind: completionKindField, Detail: "Recipe environment"},
		{Label: "arguments", InsertText: "arguments.", Kind: completionKindField, Detail: "Recipe argument"},
	}
}

func keyCompletions(table string) []completion {
	switch {
	case table == "":
		return topKeys
	case strings.HasPrefix(table, "recipes."):
		parts := strings.Split(table, ".")
		if len(parts) == 2 {
			return recipeKeys
		}
		if len(parts) == 4 && parts[2] == "arguments" {
			return argumentKeys
		}
	}
	return nil
}

func valueCompletions(key string) []completion {
	switch key {
	case "profile":
		return []completion{{Label: "go", InsertText: "go", Kind: completionKindValue, Detail: "Go profile", Quote: true}}
	case "shell":
		return shellValues
	case "type":
		return argumentTypeValues
	case "path_kind":
		return pathKindValues
	case "sandboxed", "required":
		return boolValues
	default:
		return nil
	}
}

func placeholderCompletions(analysis documentAnalysis, recipe, prefix string) []completion {
	var names []string
	names = append(names, analysis.GlobalVars...)
	names = append(names, analysis.RecipeVars[recipe]...)
	names = append(names, analysis.Arguments[recipe]...)
	names = uniqueSorted(names)
	var items []completion
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		insertText := name
		if !strings.HasSuffix(prefix, "}") {
			insertText += "}"
		}
		items = append(items, completion{
			Label:      "{" + name + "}",
			InsertText: insertText,
			Kind:       completionKindVariable,
			Detail:     "Shadowtree placeholder",
		})
	}
	return items
}

func semanticTokens(text string) []uint32 {
	lines := strings.Split(text, "\n")
	var tokens []semanticToken
	table := ""
	shells := shellSettings(lines)
	regions := scriptRegions(lines, shells)
	references := commandReferenceSpansWithScriptRegions(lines, regions)
	referenceOverlaps := commandReferenceOverlapIndex(references)
	for _, ref := range references {
		tokens = append(tokens, recipeReferenceSemanticTokens(lines, ref)...)
	}
	for lineNo, raw := range lines {
		if parsed, ok := completeTableHeader(raw); ok {
			table = parsed
		}
		key, ok := pairKey(raw)
		if ok && (table == "vars" || table == "var_commands" || strings.HasSuffix(table, ".vars")) {
			col := strings.Index(raw, key)
			tokens = append(tokens, semanticToken{Line: lineNo, Start: col, Length: len(key), Type: semanticTokenVariable})
		}
		for _, span := range placeholderSpans(raw) {
			if overlapsCommandReference(referenceOverlaps, lineNo, span) {
				continue
			}
			tokens = append(tokens, semanticToken{Line: lineNo, Start: span.Start, Length: span.Length, Type: semanticTokenVariable})
		}
	}
	for _, region := range regions {
		for _, token := range shellSemanticTokens(lines, region) {
			if overlapsCommandReference(referenceOverlaps, token.Line, span{Start: token.Start, Length: token.Length}) {
				continue
			}
			tokens = append(tokens, token)
		}
	}
	slices.SortFunc(tokens, func(a, b semanticToken) int {
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Start - b.Start
	})
	return encodeSemanticTokens(lines, tokens)
}

type scriptRegion struct {
	Shell     string
	Table     string
	Key       string
	StartLine int
	StartCol  int
	EndLine   int
	EndCol    int
}

func shellSettings(lines []string) map[string]string {
	shells := map[string]string{"": "sh"}
	table := ""
	for _, raw := range lines {
		if parsed, ok := completeTableHeader(raw); ok {
			table = parsed
			if _, ok := shells[table]; !ok {
				shells[table] = shells[""]
			}
			continue
		}
		key, ok := pairKey(raw)
		if !ok || key != "shell" {
			continue
		}
		if value, ok := pairStringValue(raw); ok && supportedShell(value) {
			shells[table] = value
		}
	}
	return shells
}

func scriptRegions(lines []string, shells map[string]string) []scriptRegion {
	var regions []scriptRegion
	table := ""
	for lineNo := 0; lineNo < len(lines); lineNo++ {
		raw := lines[lineNo]
		if parsed, ok := completeTableHeader(raw); ok {
			table = parsed
			continue
		}
		key, ok := pairKey(raw)
		if !ok || !scriptKey(key) {
			continue
		}
		shell := shellForTable(shells, table)
		if key == "pre" || key == "post" {
			listRegions, endLine := commandListScriptRegions(lines, lineNo, table, key, shell)
			regions = append(regions, listRegions...)
			lineNo = endLine
			continue
		}
		start, quote, ok := stringStart(raw)
		if !ok {
			continue
		}
		if quote.Triple {
			bodyStart := start + len(quote.Delimiter)
			if end := strings.Index(raw[bodyStart:], quote.Delimiter); end >= 0 {
				regions = append(regions, scriptRegion{
					Shell: shell, Table: table, Key: key, StartLine: lineNo, StartCol: bodyStart,
					EndLine: lineNo, EndCol: bodyStart + end,
				})
				continue
			}
			endLine, endCol := findTripleStringEnd(lines, lineNo+1, quote.Delimiter)
			regions = append(regions, scriptRegion{
				Shell: shell, Table: table, Key: key, StartLine: lineNo, StartCol: bodyStart,
				EndLine: endLine, EndCol: endCol,
			})
			lineNo = endLine
			continue
		}
		bodyStart := start + 1
		bodyEnd := findStringEnd(raw, bodyStart, quote.Delimiter[0])
		regions = append(regions, scriptRegion{
			Shell: shell, Table: table, Key: key, StartLine: lineNo, StartCol: bodyStart,
			EndLine: lineNo, EndCol: bodyEnd,
		})
	}
	return regions
}

func commandListScriptRegions(lines []string, startLine int, table, key, shell string) ([]scriptRegion, int) {
	var regions []scriptRegion
	depth := 0
	started := false
	for lineNo := startLine; lineNo < len(lines); lineNo++ {
		raw := lines[lineNo]
		col := 0
		if lineNo == startLine {
			_, value, ok := strings.Cut(raw, "=")
			if !ok {
				return nil, startLine
			}
			col = len(raw) - len(value)
		}
		for col < len(raw) {
			switch raw[col] {
			case '#':
				col = len(raw)
			case '[':
				depth++
				started = true
				col++
			case ']':
				if depth > 0 {
					depth--
				}
				col++
				if started && depth == 0 {
					return regions, lineNo
				}
			case '\'', '"':
				quote := quoteAt(raw, col)
				if quote.Delimiter == "" {
					col++
					continue
				}
				bodyStart := col + len(quote.Delimiter)
				if quote.Triple {
					if end := strings.Index(raw[bodyStart:], quote.Delimiter); end >= 0 {
						bodyEnd := bodyStart + end
						if depth == 1 && !recipe.IsRecipeReferenceString(raw[bodyStart:bodyEnd]) {
							regions = append(regions, scriptRegion{
								Shell: shell, Table: table, Key: key, StartLine: lineNo, StartCol: bodyStart,
								EndLine: lineNo, EndCol: bodyEnd,
							})
						}
						col = bodyEnd + len(quote.Delimiter)
						continue
					}
					endLine, endCol := findTripleStringEnd(lines, lineNo+1, quote.Delimiter)
					if depth == 1 {
						regions = append(regions, scriptRegion{
							Shell: shell, Table: table, Key: key, StartLine: lineNo, StartCol: bodyStart,
							EndLine: endLine, EndCol: endCol,
						})
					}
					lineNo = endLine
					raw = lineAt(lines, lineNo)
					col = min(endCol+len(quote.Delimiter), len(raw))
					continue
				}
				bodyEnd := findStringEnd(raw, bodyStart, quote.Delimiter[0])
				if depth == 1 && !recipe.IsRecipeReferenceString(raw[bodyStart:bodyEnd]) {
					regions = append(regions, scriptRegion{
						Shell: shell, Table: table, Key: key, StartLine: lineNo, StartCol: bodyStart,
						EndLine: lineNo, EndCol: bodyEnd,
					})
				}
				col = min(bodyEnd+1, len(raw))
			default:
				col++
			}
		}
		if started && depth == 0 {
			return regions, lineNo
		}
	}
	return regions, len(lines) - 1
}

func quoteAt(line string, col int) quoteInfo {
	switch {
	case strings.HasPrefix(line[col:], `'''`):
		return quoteInfo{Delimiter: `'''`, Triple: true}
	case strings.HasPrefix(line[col:], `"""`):
		return quoteInfo{Delimiter: `"""`, Triple: true}
	case line[col] == '\'' || line[col] == '"':
		return quoteInfo{Delimiter: line[col : col+1]}
	default:
		return quoteInfo{}
	}
}

type quoteInfo struct {
	Delimiter string
	Triple    bool
}

func stringStart(line string) (int, quoteInfo, bool) {
	_, value, ok := strings.Cut(line, "=")
	if !ok {
		return 0, quoteInfo{}, false
	}
	base := len(line) - len(value)
	for base < len(line) && (line[base] == ' ' || line[base] == '\t') {
		base++
	}
	switch {
	case strings.HasPrefix(line[base:], `'''`):
		return base, quoteInfo{Delimiter: `'''`, Triple: true}, true
	case strings.HasPrefix(line[base:], `"""`):
		return base, quoteInfo{Delimiter: `"""`, Triple: true}, true
	case base < len(line) && (line[base] == '\'' || line[base] == '"'):
		return base, quoteInfo{Delimiter: line[base : base+1]}, true
	default:
		return 0, quoteInfo{}, false
	}
}

func findTripleStringEnd(lines []string, startLine int, delimiter string) (int, int) {
	for lineNo := startLine; lineNo < len(lines); lineNo++ {
		if col := strings.Index(lines[lineNo], delimiter); col >= 0 {
			return lineNo, col
		}
	}
	lastLine := len(lines) - 1
	if lastLine < 0 {
		return 0, 0
	}
	return lastLine, len(lines[lastLine])
}

func findStringEnd(line string, start int, quote byte) int {
	for col := start; col < len(line); col++ {
		if quote == '"' && line[col] == '\\' {
			col++
			continue
		}
		if line[col] == quote {
			return col
		}
	}
	return len(line)
}

func shellForTable(shells map[string]string, table string) string {
	if shell, ok := shells[table]; ok && shell != "" {
		return shell
	}
	if strings.HasPrefix(table, "recipes.") {
		recipe := "recipes." + currentRecipe(table)
		if shell, ok := shells[recipe]; ok && shell != "" {
			return shell
		}
	}
	return shells[""]
}

func pairStringValue(line string) (string, bool) {
	start, quote, ok := stringStart(line)
	if !ok || quote.Triple {
		return "", false
	}
	end := findStringEnd(line, start+1, quote.Delimiter[0])
	if end <= start {
		return "", false
	}
	return line[start+1 : end], true
}

func supportedShell(shell string) bool {
	return shell == "sh" || shell == "bash" || shell == "fish"
}

func scriptKey(key string) bool {
	return key == "cmd" || key == "pre" || key == "post" || key == "shell_prelude" || key == "values"
}

func shellSemanticTokens(lines []string, region scriptRegion) []semanticToken {
	var tokens []semanticToken
	for lineNo := region.StartLine; lineNo <= region.EndLine && lineNo < len(lines); lineNo++ {
		line := lines[lineNo]
		start, end := 0, len(line)
		if lineNo == region.StartLine {
			start = region.StartCol
		}
		if lineNo == region.EndLine {
			end = region.EndCol
		}
		tokens = append(tokens, shellLineTokens(line, lineNo, start, end, region.Shell)...)
	}
	return tokens
}

func shellLineTokens(line string, lineNo, start, end int, shell string) []semanticToken {
	var tokens []semanticToken
	commandPosition := true
	fishExpectFunctionName := false
	fishInFunctionDeclaration := false
	fishExpectFunctionArgs := false
	fishExpectSetName := false
	fishExpectStatusSubcommand := false
	for col := start; col < end; {
		ch := line[col]
		if ch == '#' {
			tokens = append(tokens, semanticToken{Line: lineNo, Start: col, Length: end - col, Type: semanticTokenComment})
			break
		}
		if isShellSpace(ch) {
			col++
			continue
		}
		if ch == '\'' {
			col = skipShellString(line, col, end, ch)
			commandPosition = false
			continue
		}
		if ch == '"' {
			next, stringTokens := doubleQuotedShellTokens(line, lineNo, col, end, shell)
			tokens = append(tokens, stringTokens...)
			col = next
			commandPosition = false
			continue
		}
		if ch == '$' {
			if token, ok := shellVariableToken(line, lineNo, col, end, shell); ok {
				tokens = append(tokens, token)
				col += token.Length
				commandPosition = false
				continue
			}
		}
		if isShellOperator(ch) {
			tokens = append(tokens, semanticToken{Line: lineNo, Start: col, Length: 1, Type: semanticTokenOperator})
			commandPosition = true
			col++
			continue
		}
		if isShellWordStart(ch) {
			wordStart := col
			for col < end && isShellWordPart(line[col]) {
				col++
			}
			word := line[wordStart:col]
			switch {
			case shell == "fish" && fishExpectFunctionName:
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenFunction})
				fishExpectFunctionName = false
				fishInFunctionDeclaration = true
				commandPosition = false
			case shell == "fish" && fishExpectFunctionArgs && !strings.HasPrefix(word, "-"):
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenVariable})
				commandPosition = false
			case shell == "fish" && fishExpectSetName && !strings.HasPrefix(word, "-"):
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenVariable})
				fishExpectSetName = false
				commandPosition = false
			case shell == "fish" && fishExpectStatusSubcommand && !strings.HasPrefix(word, "-"):
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenParameter})
				fishExpectStatusSubcommand = false
				commandPosition = false
			case shellKeyword(shell, word):
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenKeyword})
				if shell == "fish" {
					switch word {
					case "function":
						fishExpectFunctionName = true
					case "set":
						fishExpectSetName = true
					}
				}
				commandPosition = commandContinuesAfterKeyword(shell, word)
			case strings.HasPrefix(word, "-"):
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenParameter})
				if shell == "fish" && fishInFunctionDeclaration && (word == "-a" || word == "--argument-names") {
					fishExpectFunctionArgs = true
				}
				commandPosition = false
			case commandPosition:
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenFunction})
				if shell == "fish" && word == "status" {
					fishExpectStatusSubcommand = true
				}
				commandPosition = false
			default:
				commandPosition = false
			}
			continue
		}
		col++
	}
	return tokens
}

func shellVariableToken(line string, lineNo, start, end int, shell string) (semanticToken, bool) {
	if start+1 >= end {
		return semanticToken{}, false
	}
	if line[start+1] == '{' {
		close := start + 2
		for close < end && line[close] != '}' {
			close++
		}
		if close < end {
			return semanticToken{Line: lineNo, Start: start, Length: close - start + 1, Type: semanticTokenVariable}, true
		}
	}
	if shell == "fish" && isIdentPart(line[start+1]) {
		col := start + 2
		for col < end && isIdentPart(line[col]) {
			col++
		}
		return semanticToken{Line: lineNo, Start: start, Length: col - start, Type: semanticTokenVariable}, true
	}
	if strings.ContainsRune("0123456789@*#?$!-", rune(line[start+1])) {
		return semanticToken{Line: lineNo, Start: start, Length: 2, Type: semanticTokenVariable}, true
	}
	if !isIdentStart(line[start+1]) {
		return semanticToken{}, false
	}
	col := start + 2
	for col < end && isIdentPart(line[col]) {
		col++
	}
	return semanticToken{Line: lineNo, Start: start, Length: col - start, Type: semanticTokenVariable}, true
}

func skipShellString(line string, start, end int, quote byte) int {
	for col := start + 1; col < end; col++ {
		if quote == '"' && line[col] == '\\' {
			col++
			continue
		}
		if line[col] == quote {
			return col + 1
		}
	}
	return end
}

func doubleQuotedShellTokens(line string, lineNo, start, end int, shell string) (int, []semanticToken) {
	var tokens []semanticToken
	col := start + 1
	for col < end {
		switch line[col] {
		case '\\':
			col += 2
		case '"':
			return col + 1, tokens
		case '$':
			if token, ok := shellVariableToken(line, lineNo, col, end, shell); ok {
				tokens = append(tokens, token)
				col += token.Length
				continue
			}
			col++
		default:
			col++
		}
	}
	return end, tokens
}

func shellKeyword(shell, word string) bool {
	if shell == "fish" {
		return slices.Contains(fishKeywords, word)
	}
	return slices.Contains(shKeywords, word)
}

func commandContinuesAfterKeyword(shell, word string) bool {
	if shell == "fish" {
		return word == "and" || word == "or" || word == "not" || word == "command" || word == "if" || word == "while"
	}
	return word == "then" || word == "do" || word == "else" || word == "elif"
}

var shKeywords = []string{
	"case", "do", "done", "elif", "else", "esac", "fi", "for", "function",
	"if", "in", "select", "then", "time", "until", "while",
}

var fishKeywords = []string{
	"and", "begin", "case", "command", "else", "end", "for", "function",
	"if", "in", "not", "or", "set", "switch", "while",
}

func isShellSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r'
}

func isShellOperator(ch byte) bool {
	return strings.ContainsRune("|&;()<>", rune(ch))
}

func isShellWordStart(ch byte) bool {
	return ch == '-' || ch == '_' || ch == '.' || ch == '/' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func isShellWordPart(ch byte) bool {
	return isShellWordStart(ch) || ch == ':' || ch == '+'
}

type span struct {
	Start  int
	Length int
}

func placeholderSpans(line string) []span {
	var spans []span
	for i := 0; i < len(line); i++ {
		if line[i] != '{' {
			continue
		}
		end := i + 1
		if end >= len(line) || !isIdentStart(line[end]) {
			continue
		}
		end++
		for end < len(line) && isIdentPart(line[end]) {
			end++
		}
		if end < len(line) && line[end] == '}' {
			spans = append(spans, span{Start: i, Length: end - i + 1})
			i = end
		}
	}
	return spans
}

func commandReferenceOverlapIndex(references []commandReferenceSpan) map[int][]span {
	overlaps := map[int][]span{}
	for _, ref := range references {
		overlaps[ref.Line] = append(overlaps[ref.Line], span{Start: ref.Start, Length: ref.End - ref.Start})
	}
	return overlaps
}

func overlapsCommandReference(references map[int][]span, lineNo int, item span) bool {
	start := item.Start
	end := item.Start + item.Length
	for _, ref := range references[lineNo] {
		if start < ref.Start+ref.Length && ref.Start < end {
			return true
		}
	}
	return false
}

type commandReferenceSpan struct {
	Path      string
	Name      string
	Line      int
	Start     int
	End       int
	TargetEnd int
}

func (ref commandReferenceSpan) Target() string {
	if ref.Path == "" {
		return ref.Name
	}
	return ref.Path + ":" + ref.Name
}

type commandReferenceScan struct {
	depth   int
	list    bool
	pending bool
}

func commandReferenceSpans(lines []string) []commandReferenceSpan {
	return commandReferenceSpansWithScriptRegions(lines, scriptRegions(lines, shellSettings(lines)))
}

func commandReferenceSpansWithScriptRegions(lines []string, regions []scriptRegion) []commandReferenceSpan {
	var spans []commandReferenceSpan
	table := ""
	scan := commandReferenceScan{}
	for lineNo, line := range lines {
		start := 0
		if scan.depth == 0 {
			if parsed, ok := completeTableHeader(line); ok {
				table = parsed
				continue
			}
			key, ok := pairKey(line)
			if !ok || !recipeReferenceKey(table, key) {
				continue
			}
			_, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			start = len(line) - len(value)
			scan.list = key == "pre" || key == "post"
			scan.pending = !scan.list
		}
		lineSpans := commandReferenceSpansInText(line[start:], lineNo, start, &scan)
		spans = append(spans, lineSpans...)
		if scan.depth <= 0 {
			scan = commandReferenceScan{}
		}
	}
	spans = append(spans, scriptCommandReferenceSpans(lines, regions)...)
	return uniqueCommandReferenceSpans(spans)
}

func uniqueCommandReferenceSpans(spans []commandReferenceSpan) []commandReferenceSpan {
	seen := map[commandReferenceSpan]bool{}
	out := make([]commandReferenceSpan, 0, len(spans))
	for _, span := range spans {
		if seen[span] {
			continue
		}
		seen[span] = true
		out = append(out, span)
	}
	return out
}

func commandReferenceSpansInText(text string, lineNo, offset int, scan *commandReferenceScan) []commandReferenceSpan {
	var spans []commandReferenceSpan
	followsOpen := false
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '"', '\'':
			start := i + 1
			end := findStringEnd(text, start, text[i])
			value := text[start:end]
			if scan.list {
				switch {
				case scan.depth == 1 && recipe.IsRecipeReferenceString(value):
					spans = append(spans, commandReferenceSpanFromString(value, lineNo, offset+start, offset+end))
				case scan.depth == 2 && followsOpen && strings.HasPrefix(value, "@"):
					spans = append(spans, commandReferenceSpanFromString(value, lineNo, offset+start, offset+end))
				}
			} else {
				switch {
				case scan.depth == 0:
					scan.pending = false
					if recipe.IsRecipeReferenceString(value) {
						spans = append(spans, commandReferenceSpanFromString(value, lineNo, offset+start, offset+end))
					}
				case scan.depth == 1 && scan.pending:
					scan.pending = false
					if strings.HasPrefix(value, "@") {
						spans = append(spans, commandReferenceSpanFromString(value, lineNo, offset+start, offset+end))
					}
				}
			}
			i = end
			followsOpen = false
		case '[':
			scan.depth++
			followsOpen = true
		case ']':
			scan.depth--
			followsOpen = false
		case ' ', '\t', '\r':
		default:
			followsOpen = false
		}
	}
	return spans
}

func commandReferenceSpanFromString(value string, lineNo, start, end int) commandReferenceSpan {
	lookup := recipeReferenceLookupValue(value)
	ref, ok := recipe.ParseRecipeReference(recipe.Command{lookup})
	if !ok {
		ref.Name = strings.TrimPrefix(lookup, "@")
	}
	return commandReferenceSpan{
		Path:      ref.Path,
		Name:      ref.Name,
		Line:      lineNo,
		Start:     start,
		End:       end,
		TargetEnd: start + len(lookup),
	}
}

func recipeReferenceLookupValue(value string) string {
	target := strings.TrimPrefix(value, "@")
	open := strings.IndexByte(target, '[')
	space := strings.IndexAny(target, " \t")
	if space >= 0 && (open < 0 || space < open) {
		return value[:1+space]
	}
	return value
}

func scriptCommandReferenceSpans(lines []string, regions []scriptRegion) []commandReferenceSpan {
	var spans []commandReferenceSpan
	for _, region := range regions {
		if !recipeScriptReferenceRegion(region) {
			continue
		}
		if region.Shell != "" && region.Shell != "sh" && region.Shell != "bash" {
			continue
		}
		var state scriptCommandScanState
		for lineNo := region.StartLine; lineNo <= region.EndLine && lineNo < len(lines); lineNo++ {
			line := lines[lineNo]
			start, end := 0, len(line)
			if lineNo == region.StartLine {
				start = region.StartCol
			}
			if lineNo == region.EndLine {
				end = region.EndCol
			}
			spans = append(spans, scriptLineCommandReferenceSpans(line, lineNo, start, end, &state)...)
		}
	}
	return spans
}

func recipeScriptReferenceRegion(region scriptRegion) bool {
	if region.Key == "shell_prelude" {
		return region.Table == "" || recipeTable(region.Table)
	}
	if recipeTable(region.Table) {
		return region.Key == "cmd" || region.Key == "pre" || region.Key == "post"
	}
	if recipeArgumentTable(region.Table) {
		return region.Key == "values"
	}
	return false
}

type scriptCommandScanState struct {
	quote    byte
	heredocs []string
}

func scriptLineCommandReferenceSpans(line string, lineNo, start, end int, state *scriptCommandScanState) []commandReferenceSpan {
	var spans []commandReferenceSpan
	commandPosition := true
	if state.quote != 0 {
		next, closed := skipShellStringBody(line, start, end, state.quote)
		if !closed {
			return nil
		}
		state.quote = 0
		commandPosition = false
		start = next
	}
	if len(state.heredocs) > 0 {
		if strings.TrimSpace(line[start:end]) == state.heredocs[0] {
			state.heredocs = state.heredocs[1:]
		}
		return nil
	}
	for col := start; col < end; {
		ch := line[col]
		if ch == '#' {
			break
		}
		if isShellSpace(ch) {
			col++
			continue
		}
		if ch == '\'' || ch == '"' {
			next, closed := skipShellStringWithClose(line, col, end, ch)
			if !closed {
				state.quote = ch
				return spans
			}
			col = next
			commandPosition = false
			continue
		}
		if commandPosition {
			if next, ok := scanShellAssignment(line, col, end); ok {
				col = next
				continue
			}
			if ch == '@' {
				refEnd := shellRecipeReferenceEnd(line, col, end)
				if refEnd > col+1 {
					spans = append(spans, commandReferenceSpanFromString(line[col:refEnd], lineNo, col, refEnd))
				}
				col = refEnd
				commandPosition = false
				continue
			}
		}
		if ch == '<' && col+1 < end && line[col+1] == '<' {
			if delimiter, next, ok := scanHereDocDelimiter(line, col+2, end); ok {
				state.heredocs = append(state.heredocs, delimiter)
				col = next
				commandPosition = false
				continue
			}
		}
		if ch == '$' {
			if token, ok := shellVariableToken(line, lineNo, col, end, "sh"); ok {
				col += token.Length
				commandPosition = false
				continue
			}
		}
		if isShellOperator(ch) {
			commandPosition = true
			col++
			continue
		}
		if isShellWordStart(ch) {
			wordStart := col
			for col < end && isShellWordPart(line[col]) {
				col++
			}
			word := line[wordStart:col]
			commandPosition = shellKeyword("sh", word) && commandContinuesAfterKeyword("sh", word)
			continue
		}
		col++
	}
	return spans
}

func skipShellStringWithClose(line string, start, end int, quote byte) (int, bool) {
	for col := start + 1; col < end; col++ {
		if quote == '"' && line[col] == '\\' {
			col++
			continue
		}
		if line[col] == quote {
			return col + 1, true
		}
	}
	return end, false
}

func skipShellStringBody(line string, start, end int, quote byte) (int, bool) {
	for col := start; col < end; col++ {
		if quote == '"' && line[col] == '\\' {
			col++
			continue
		}
		if line[col] == quote {
			return col + 1, true
		}
	}
	return end, false
}

func scanHereDocDelimiter(line string, start, end int) (string, int, bool) {
	col := start
	if col < end && line[col] == '-' {
		col++
	}
	for col < end && isShellSpace(line[col]) {
		col++
	}
	if col >= end {
		return "", col, false
	}
	delimiterStart := col
	for col < end && !isShellSpace(line[col]) && !isShellOperator(line[col]) {
		col++
	}
	delimiter := strings.Trim(line[delimiterStart:col], `'"`)
	return delimiter, col, delimiter != ""
}

func scanShellAssignment(line string, start, end int) (int, bool) {
	if start >= end || !isIdentStart(line[start]) {
		return start, false
	}
	col := start + 1
	for col < end && isIdentPart(line[col]) {
		col++
	}
	if col >= end || line[col] != '=' {
		return start, false
	}
	col++
	for col < end {
		ch := line[col]
		if isShellSpace(ch) || isShellOperator(ch) || ch == '#' {
			break
		}
		if ch == '\'' || ch == '"' {
			col = skipShellString(line, col, end, ch)
			continue
		}
		col++
	}
	return col, true
}

func shellRecipeReferenceEnd(line string, start, end int) int {
	col := start
	bracket := false
	for col < end {
		ch := line[col]
		if ch == '[' {
			bracket = true
			col++
			continue
		}
		if ch == ']' {
			col++
			break
		}
		if !bracket && (isShellSpace(ch) || isShellOperator(ch) || ch == '\'' || ch == '"' || ch == '#') {
			break
		}
		if bracket && (ch == '\'' || ch == '"' || ch == '#') {
			break
		}
		col++
	}
	if bracket {
		return col
	}
	refEnd := col
	for col < end {
		for col < end && isShellSpace(line[col]) {
			col++
		}
		if col >= end || line[col] == '#' || isShellOperator(line[col]) {
			return refEnd
		}
		for col < end {
			ch := line[col]
			if isShellSpace(ch) || isShellOperator(ch) || ch == '#' {
				break
			}
			if ch == '\'' || ch == '"' {
				col = skipShellString(line, col, end, ch)
				continue
			}
			col++
		}
		refEnd = col
	}
	return refEnd
}

func recipeReferenceSemanticTokens(lines []string, ref commandReferenceSpan) []semanticToken {
	line := lineAt(lines, ref.Line)
	if ref.Start >= len(line) || ref.End > len(line) || !strings.HasPrefix(line[ref.Start:ref.End], "@") {
		return nil
	}
	value := line[ref.Start:ref.End]
	target := strings.TrimPrefix(value, "@")
	if strings.HasPrefix(target, "{") {
		return []semanticToken{{Line: ref.Line, Start: ref.Start, Length: ref.End - ref.Start, Type: semanticTokenRecipeReference}}
	}
	nameStart := ref.Start + 1
	open := strings.IndexByte(target, '[')
	if open < 0 {
		if space := strings.IndexAny(target, " \t"); space >= 0 {
			var tokens []semanticToken
			if space > 0 {
				tokens = append(tokens, semanticToken{Line: ref.Line, Start: ref.Start, Length: space + 1, Type: semanticTokenFunction})
			}
			tokens = append(tokens, recipeReferenceSpacedArgumentTokens(line, ref.Line, nameStart+space, ref.End)...)
			return tokens
		}
		if target == "" {
			return []semanticToken{{Line: ref.Line, Start: ref.Start, Length: ref.End - ref.Start, Type: semanticTokenRecipeReference}}
		}
		return []semanticToken{{Line: ref.Line, Start: ref.Start, Length: len(value), Type: semanticTokenFunction}}
	}
	var tokens []semanticToken
	if open > 0 {
		tokens = append(tokens, semanticToken{Line: ref.Line, Start: ref.Start, Length: open + 1, Type: semanticTokenFunction})
	}
	contentStart := nameStart + open + 1
	contentEnd := ref.End
	if strings.HasSuffix(value, "]") {
		contentEnd--
	}
	tokens = append(tokens, recipeReferenceArgumentTokens(line, ref.Line, contentStart, contentEnd)...)
	return tokens
}

func recipeReferenceSpacedArgumentTokens(line string, lineNo, start, end int) []semanticToken {
	var tokens []semanticToken
	for start < end {
		for start < end && isShellSpace(line[start]) {
			start++
		}
		partEnd := start
		for partEnd < end && !isShellSpace(line[partEnd]) {
			if line[partEnd] == '\'' || line[partEnd] == '"' {
				partEnd = skipShellString(line, partEnd, end, line[partEnd])
				continue
			}
			partEnd++
		}
		tokens = append(tokens, recipeReferenceArgumentPartTokens(line, lineNo, start, partEnd)...)
		start = partEnd
	}
	return tokens
}

func recipeReferenceArgumentTokens(line string, lineNo, start, end int) []semanticToken {
	var tokens []semanticToken
	partStart := start
	for partStart <= end {
		partEnd := partStart
		for partEnd < end && line[partEnd] != ',' {
			partEnd++
		}
		tokens = append(tokens, recipeReferenceArgumentPartTokens(line, lineNo, partStart, partEnd)...)
		partStart = partEnd + 1
		if partStart > end {
			break
		}
	}
	return tokens
}

func recipeReferenceArgumentPartTokens(line string, lineNo, start, end int) []semanticToken {
	for start < end && (line[start] == ' ' || line[start] == '\t') {
		start++
	}
	for end > start && (line[end-1] == ' ' || line[end-1] == '\t') {
		end--
	}
	if start >= end {
		return nil
	}
	eq := strings.IndexByte(line[start:end], '=')
	if eq < 0 {
		return []semanticToken{{Line: lineNo, Start: start, Length: end - start, Type: semanticTokenString}}
	}
	eq += start
	tokens := []semanticToken{{Line: lineNo, Start: start, Length: eq - start, Type: semanticTokenParameter}}
	valueStart := eq + 1
	if valueStart < end {
		tokens = append(tokens, semanticToken{Line: lineNo, Start: valueStart, Length: end - valueStart, Type: semanticTokenString})
	}
	return tokens
}

func recipeReferencePrefix(table, prefix string) (string, bool) {
	key, ok := keyBeforeValue(prefix)
	if !ok || !recipeReferenceKey(table, key) {
		return "", false
	}
	quote := lastOpenQuote(prefix)
	if quote < 0 {
		return "", false
	}
	value := prefix[quote+1:]
	if !strings.HasPrefix(value, "@") {
		return "", false
	}
	return strings.TrimPrefix(value, "@"), true
}

func lastOpenQuote(prefix string) int {
	for i := 0; i < len(prefix); i++ {
		if prefix[i] != '"' && prefix[i] != '\'' {
			continue
		}
		end := findStringEnd(prefix, i+1, prefix[i])
		if end == len(prefix) {
			return i
		}
		i = end
	}
	return -1
}

func recipeReferenceKey(table, key string) bool {
	switch key {
	case "cmd", "pre", "post":
		return recipeTable(table)
	case "values":
		return recipeArgumentTable(table)
	default:
		return false
	}
}

func recipeTable(table string) bool {
	rest, ok := strings.CutPrefix(table, "recipes.")
	return ok && rest != "" && !strings.Contains(rest, ".")
}

func recipeArgumentTable(table string) bool {
	rest, ok := strings.CutPrefix(table, "recipes.")
	if !ok {
		return false
	}
	recipeName, argName, ok := strings.Cut(rest, ".arguments.")
	return ok && recipeName != "" && argName != "" && !strings.Contains(argName, ".")
}

func isRecipeReferenceNameByte(ch byte) bool {
	return isBareKeyByte(ch) || ch == '-' || ch == '_' || ch == '.' || ch == '/' || ch == ':' || ch == '{' || ch == '}'
}

func encodeSemanticTokens(lines []string, tokens []semanticToken) []uint32 {
	var out []uint32
	prevLine, prevStart := 0, 0
	for _, token := range tokens {
		line := lineAt(lines, token.Line)
		start := byteToUTF16Offset(line, token.Start)
		length := byteToUTF16Offset(line, token.Start+token.Length) - start
		deltaLine := token.Line - prevLine
		deltaStart := start
		if deltaLine == 0 {
			deltaStart -= prevStart
		}
		out = append(out,
			uint32(deltaLine),
			uint32(deltaStart),
			uint32(length),
			uint32(token.Type),
			0,
		)
		prevLine = token.Line
		prevStart = start
	}
	return out
}

type semanticToken struct {
	Line   int
	Start  int
	Length int
	Type   int
}

const semanticTokenVariable = 0

const (
	semanticTokenKeyword = iota + 1
	semanticTokenFunction
	semanticTokenParameter
	semanticTokenOperator
	semanticTokenComment
	semanticTokenRecipeReference
	semanticTokenString
)

func lineAt(lines []string, line int) string {
	if line < 0 || line >= len(lines) {
		return ""
	}
	return lines[line]
}

func linePrefix(line string, character int) string {
	if character < 0 {
		return ""
	}
	if character > len(line) {
		character = len(line)
	}
	return line[:character]
}

func utf16ToByteOffset(line string, character int) int {
	if character <= 0 {
		return 0
	}
	units := 0
	for offset, r := range line {
		width := utf16.RuneLen(r)
		if units+width > character {
			return offset
		}
		units += width
	}
	return len(line)
}

func byteToUTF16Offset(line string, offset int) int {
	if offset <= 0 {
		return 0
	}
	if offset > len(line) {
		offset = len(line)
	}
	units := 0
	for byteOffset, r := range line {
		if byteOffset >= offset {
			break
		}
		units += utf16.RuneLen(r)
	}
	return units
}

func openTablePrefix(prefix string) (string, bool) {
	trimmed := strings.TrimSpace(prefix)
	if strings.HasPrefix(trimmed, "[[") {
		trimmed = strings.TrimPrefix(trimmed, "[[")
	} else if strings.HasPrefix(trimmed, "[") {
		trimmed = strings.TrimPrefix(trimmed, "[")
	} else {
		return "", false
	}
	if strings.Contains(trimmed, "]") {
		return "", false
	}
	return strings.TrimSpace(trimmed), true
}

func completeTableHeader(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimSuffix(strings.TrimSpace(strings.Split(trimmed, "#")[0]), "\r")
	switch {
	case strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]"):
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "[["), "]]")), true
	case strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"):
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")), true
	default:
		return "", false
	}
}

func pairKey(line string) (string, bool) {
	before, _, ok := strings.Cut(line, "=")
	if !ok {
		return "", false
	}
	key := strings.TrimSpace(before)
	if key == "" || strings.ContainsAny(key, "[]") {
		return "", false
	}
	key = strings.Trim(key, `"'`)
	if key == "" {
		return "", false
	}
	return key, true
}

func keyBeforeValue(prefix string) (string, bool) {
	key, _, ok := strings.Cut(prefix, "=")
	if !ok {
		return "", false
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "[]") {
		return "", false
	}
	return strings.Trim(key, `"'`), true
}

func placeholderPrefix(prefix string) (string, bool) {
	open := strings.LastIndexByte(prefix, '{')
	close := strings.LastIndexByte(prefix, '}')
	if open < 0 || close > open {
		return "", false
	}
	for _, ch := range prefix[open+1:] {
		if !(ch == '_' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z') {
			return "", false
		}
	}
	return prefix[open+1:], true
}

func currentRecipe(table string) string {
	if !strings.HasPrefix(table, "recipes.") {
		return ""
	}
	rest := strings.TrimPrefix(table, "recipes.")
	recipe, _, _ := strings.Cut(rest, ".")
	return recipe
}

func appendUnique(values []string, value string) []string {
	if value == "" || slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func mapsKeys[V any](items map[string]V) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	return keys
}

func uniqueSorted(values []string) []string {
	var out []string
	for _, value := range values {
		out = appendUnique(out, value)
	}
	slices.Sort(out)
	return out
}

func isIdentStart(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func isIdentPart(ch byte) bool {
	return isIdentStart(ch) || ch >= '0' && ch <= '9'
}
