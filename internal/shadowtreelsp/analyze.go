package shadowtreelsp

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/BurntSushi/toml"
	recipecompletion "github.com/yusing/shadowtree/internal/completion"
	"github.com/yusing/shadowtree/internal/configfile"
	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/scriptref"
	"mvdan.cc/sh/v3/syntax"
)

type documentAnalysis struct {
	Lines         []string
	Recipes       []string
	GlobalVars    []string
	RecipeVars    map[string][]string
	Arguments     map[string][]string
	ArgumentHelp  map[string]map[string]string
	FanOutRecipes map[string]bool
	ScriptRegions []scriptRegion
	CurrentTable  string
}

type completion struct {
	Label           string
	InsertText      string
	Kind            int
	Detail          string
	Quote           bool
	Placeholder     bool
	RecipeReference bool
	Incomplete      bool
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
	{Label: "for_each", InsertText: `for_each = ""`, Kind: completionKindKeyword, Detail: "Run main command once per value"},
	{Label: "workdir", InsertText: `workdir = ""`, Kind: completionKindKeyword, Detail: "Relative main-command working directory"},
	{Label: "cmd", InsertText: `cmd = ""`, Kind: completionKindKeyword, Detail: "Main command"},
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
		Lines:         lines,
		RecipeVars:    map[string][]string{},
		Arguments:     map[string][]string{},
		ArgumentHelp:  map[string]map[string]string{},
		FanOutRecipes: map[string]bool{},
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
	slices.Sort(analysis.GlobalVars)
	var cfg recipe.Config
	if _, err := toml.Decode(text, &cfg); err == nil {
		for recipeName, rec := range cfg.Recipes {
			if len(rec.ForEach) > 0 {
				analysis.FanOutRecipes[recipeName] = true
			}
			for argName, arg := range rec.Arguments {
				if analysis.ArgumentHelp[recipeName] == nil {
					analysis.ArgumentHelp[recipeName] = map[string]string{}
				}
				analysis.ArgumentHelp[recipeName][argName] = recipe.ArgumentHelp(arg)
			}
		}
	}
	slices.Sort(analysis.Recipes)
	for name := range analysis.RecipeVars {
		slices.Sort(analysis.RecipeVars[name])
	}
	for name := range analysis.Arguments {
		slices.Sort(analysis.Arguments[name])
	}
	analysis.ScriptRegions = scriptRegions(lines, shellSettings(lines))
	return analysis
}

func enrichAnalysisWithResolvedRecipes(ctx context.Context, text string, analysis *documentAnalysis, ignoreLine int, opts completionOptions) {
	var recipes map[string]recipe.Recipe
	var ok bool
	if ignoreLine >= 0 {
		recipes, ok = completionRecipesIgnoringLine(ctx, text, ignoreLine, opts)
	} else {
		recipes, ok = completionRecipes(ctx, text, opts)
	}
	if !ok {
		return
	}
	for recipeName, rec := range recipes {
		analysis.Recipes = appendUnique(analysis.Recipes, recipeName)
		if len(rec.Arguments) > 0 {
			names := analysis.Arguments[recipeName]
			seen := make(map[string]bool, len(names)+len(rec.Arguments))
			for _, name := range names {
				seen[name] = true
			}
			for name := range rec.Arguments {
				if seen[name] {
					continue
				}
				seen[name] = true
				names = append(names, name)
			}
			analysis.Arguments[recipeName] = names
		}
		if len(rec.ForEach) > 0 {
			analysis.FanOutRecipes[recipeName] = true
		}
		for argName, arg := range rec.Arguments {
			if analysis.ArgumentHelp[recipeName] == nil {
				analysis.ArgumentHelp[recipeName] = map[string]string{}
			}
			analysis.ArgumentHelp[recipeName][argName] = recipe.ArgumentHelp(arg)
		}
	}
	slices.Sort(analysis.Recipes)
	for recipeName := range analysis.Arguments {
		slices.Sort(analysis.Arguments[recipeName])
	}
}

func completionsAt(ctx context.Context, text string, pos lspPosition) []completion {
	return completionsAtWithOptions(ctx, text, pos, completionOptions{})
}

func completionsAtWithOptions(ctx context.Context, text string, pos lspPosition, opts completionOptions) []completion {
	analysis := analyzeDocument(text, pos.Line)
	line := lineAt(analysis.Lines, pos.Line)
	prefix := linePrefix(line, pos.Character)
	if tablePrefix, ok := openTablePrefix(prefix); ok {
		if tablePrefixNeedsResolvedRecipes(tablePrefix) {
			enrichAnalysisWithResolvedRecipes(ctx, text, &analysis, pos.Line, opts)
		}
		return tableCompletions(analysis, tablePrefix)
	}
	if items, ok := recipeArgumentReferenceCompletions(ctx, text, analysis.CurrentTable, prefix, pos, opts); ok {
		return items
	}
	if items, ok := commandListRecipeReferenceCompletions(ctx, text, analysis, pos, opts); ok {
		return items
	}
	if items, ok := scriptRecipeReferenceCompletions(ctx, text, analysis, pos, opts); ok {
		return items
	}
	if recipePrefix, key, ok := recipeReferencePrefix(analysis.CurrentTable, prefix); ok {
		return recipeReferenceCompletionsWithOptions(ctx, text, analysis.Recipes, recipePrefix, valueBuiltinReferenceContext(analysis.CurrentTable, key), opts)
	}
	if variablePrefix, ok := placeholderPrefix(prefix); ok {
		key, _ := keyBeforeValue(prefix)
		enrichAnalysisWithResolvedRecipes(ctx, text, &analysis, -1, opts)
		return placeholderCompletions(analysis, currentRecipe(analysis.CurrentTable), variablePrefix, variadicPlaceholderCompletionAllowed(analysis, prefix, pos), forEachPlaceholderCompletionAllowed(analysis.CurrentTable, key))
	}
	if inScriptRegion(analysis.Lines, analysis.ScriptRegions, pos) {
		return nil
	}
	if key, ok := keyBeforeValue(prefix); ok {
		if items, ok := workdirValueCompletions(ctx, analysis.CurrentTable, key, prefix, opts); ok {
			return items
		}
		if items, ok := argumentDefaultValueCompletions(ctx, text, analysis.CurrentTable, key, prefix, pos, opts); ok {
			return items
		}
		return valueCompletions(key)
	}
	if syncOutArrayStringValueAt(analysis.Lines, pos) {
		return nil
	}
	return keyCompletions(analysis.CurrentTable)
}

func tablePrefixNeedsResolvedRecipes(prefix string) bool {
	rest, ok := strings.CutPrefix(prefix, "recipes.")
	if !ok {
		return false
	}
	_, rest, ok = strings.Cut(rest, ".")
	return ok && (rest == "arguments" || strings.HasPrefix(rest, "arguments."))
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
	recipes, ok := completionRecipes(ctx, text, opts)
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
		ctx,
		name+"[",
		content,
		rec,
		recipes,
		lspCompletionCandidateOptions(opts),
	)
	return recipeArgumentCompletions(candidates, quote+len("@")+1, pos.Character), true
}

func commandListRecipeReferenceCompletions(ctx context.Context, text string, analysis documentAnalysis, pos lspPosition, opts completionOptions) ([]completion, bool) {
	line := lineAt(analysis.Lines, pos.Line)
	if !strings.Contains(linePrefix(line, pos.Character), "@") {
		return nil, false
	}
	for _, span := range commandReferenceSpansWithScriptRegions(analysis.Lines, analysis.ScriptRegions) {
		if span.Line != pos.Line || span.Key != "pre" && span.Key != "post" {
			continue
		}
		if pos.Character <= span.Start || pos.Character > span.End {
			continue
		}
		prefix := line[span.Start+1 : pos.Character]
		if strings.Contains(prefix, "[") {
			return groupedScriptRecipeReferenceCompletions(ctx, text, prefix, pos, span.Start, opts), true
		}
		if strings.ContainsAny(prefix, " \t") {
			return spacedScriptRecipeReferenceCompletions(ctx, text, prefix, pos, span.Start, opts), true
		}
		items := recipeReferenceCompletionsWithOptions(ctx, text, analysis.Recipes, prefix, false, opts)
		for i := range items {
			items[i].Edit = &completionEdit{Start: span.Start + 1, End: pos.Character}
		}
		return items, true
	}
	return nil, false
}

func scriptRecipeReferenceCompletions(ctx context.Context, text string, analysis documentAnalysis, pos lspPosition, opts completionOptions) ([]completion, bool) {
	region, ok := scriptRegionAt(analysis.Lines, analysis.ScriptRegions, pos)
	if !ok || !recipeScriptReferenceRegion(region) || !scriptref.SupportedShell(region.Shell) {
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
	items := recipeReferenceCompletionsWithOptions(ctx, text, analysis.Recipes, prefix, valueBuiltinReferenceContext(region.Table, region.Key), opts)
	for i := range items {
		items[i].Edit = &completionEdit{Start: start + 1, End: pos.Character}
	}
	return items, true
}

func groupedScriptRecipeReferenceCompletions(ctx context.Context, text, prefix string, pos lspPosition, start int, opts completionOptions) []completion {
	name, content, ok := cutReferenceGroup(prefix)
	if !ok {
		return nil
	}
	recipes, ok := completionRecipes(ctx, text, opts)
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
	candidates := recipecompletion.GroupedArgumentCandidates(
		ctx,
		name+"[",
		content,
		rec,
		recipes,
		lspCompletionCandidateOptions(opts),
	)
	return recipeArgumentCompletions(candidates, start+1, pos.Character)
}

func spacedScriptRecipeReferenceCompletions(ctx context.Context, text, prefix string, pos lspPosition, start int, opts completionOptions) []completion {
	name, argsText, ok := strings.Cut(prefix, " ")
	if !ok {
		name, argsText, ok = strings.Cut(prefix, "\t")
	}
	if !ok || strings.TrimSpace(name) == "" {
		return nil
	}
	recipes, ok := completionRecipes(ctx, text, opts)
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
	candidates := recipecompletion.GroupedArgumentCandidates(ctx, "", content, rec, recipes, lspCompletionCandidateOptions(opts))
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
	if strings.ContainsAny(prefix, "\r\n;|&()<>") {
		return 0, "", false
	}
	return activeStart, prefix, true
}

func completionRecipes(ctx context.Context, text string, opts completionOptions) (map[string]recipe.Recipe, bool) {
	var cfg recipe.Config
	if _, err := toml.Decode(text, &cfg); err != nil {
		return nil, false
	}
	return completionRecipesFromConfig(ctx, cfg, opts)
}

func completionRecipesFromConfig(ctx context.Context, cfg recipe.Config, opts completionOptions) (map[string]recipe.Recipe, bool) {
	path := opts.ConfigPath
	if path == "" {
		path = configfile.Names[0]
	}
	loaded := configfile.Loaded{Path: path, Config: cfg}
	recipes, _, err := configfile.ResolveRecipes(ctx, loaded, completionBaseDir(opts), configfile.ResolveOptions{})
	if err != nil {
		return nil, false
	}
	return recipes, true
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
		suffix = strings.TrimPrefix(strings.TrimPrefix(suffix, `"`), `'`)
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

func recipeReferenceCompletionsWithOptions(ctx context.Context, text string, fallbackRecipeNames []string, prefix string, includeValueBuiltins bool, opts completionOptions) []completion {
	if pathPrefix, recipePrefix, ok := strings.Cut(prefix, ":"); ok {
		return crossConfigRecipeReferenceCompletions(ctx, pathPrefix, recipePrefix, opts)
	}
	recipes, ok := completionRecipes(ctx, text, opts)
	names := slices.Collect(maps.Keys(recipes))
	if !ok {
		names = slices.Clone(fallbackRecipeNames)
	}
	if includeValueBuiltins {
		for _, name := range recipe.BuiltinReferenceNames() {
			names = appendUnique(names, name)
		}
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
		configPath := filepath.Join(base, name, configfile.Names[0])
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
	names := slices.Sorted(maps.Keys(recipes))
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
	if detail := recipe.BuiltinReferenceDetail(name); detail != "" {
		return detail
	}
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
	loaded, err := configfile.Load(filepath.Join(targetDir, configfile.Names[0]))
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

func workdirValueCompletions(ctx context.Context, table, key, prefix string, opts completionOptions) ([]completion, bool) {
	if key != "workdir" || !recipeTable(table) {
		return nil, false
	}
	_, valuePrefix, _ := strings.Cut(prefix, "=")
	valuePrefix = strings.TrimLeft(valuePrefix, " \t")
	argName := "workdir"
	rec := recipe.Recipe{
		Arguments: map[string]recipe.Argument{
			argName: {Type: "rel_path", PathKind: "dir"},
		},
	}
	candidates := recipecompletion.GroupedArgumentCandidates(
		ctx,
		"",
		argName+"="+valuePrefix,
		rec,
		nil,
		lspCompletionCandidateOptions(opts),
	)
	items := argumentDefaultValueCompletionItems(candidates, argName, true)
	for i := range items {
		items[i].Incomplete = true
	}
	return items, true
}

func argumentDefaultValueCompletions(ctx context.Context, text, table, key, prefix string, pos lspPosition, opts completionOptions) ([]completion, bool) {
	if key != "default" {
		return nil, false
	}
	recipeName, argName, ok := recipeArgumentTableParts(table)
	if !ok {
		return nil, false
	}
	recipes, ok := completionRecipesIgnoringLine(ctx, text, pos.Line, opts)
	if !ok {
		return nil, true
	}
	rec, ok := recipes[recipeName]
	if !ok {
		return nil, true
	}
	arg, ok := rec.Arguments[argName]
	if !ok || len(arg.Values) == 0 && arg.Type != "bool" {
		return nil, true
	}
	_, valuePrefix, _ := strings.Cut(prefix, "=")
	valuePrefix = strings.TrimLeft(valuePrefix, " \t")
	candidates := recipecompletion.GroupedArgumentCandidates(
		ctx,
		"",
		argName+"="+valuePrefix,
		rec,
		recipes,
		lspCompletionCandidateOptions(opts),
	)
	return argumentDefaultValueCompletionItems(candidates, argName, defaultValueNeedsQuote(arg)), true
}

func lspCompletionCandidateOptions(opts completionOptions) recipecompletion.Options {
	return recipecompletion.Options{
		Dir:                        opts.Dir,
		ConfigPath:                 opts.ConfigPath,
		DisableCommandBackedValues: true,
	}
}

func completionRecipesIgnoringLine(ctx context.Context, text string, line int, opts completionOptions) (map[string]recipe.Recipe, bool) {
	lines := strings.Split(text, "\n")
	if line < 0 || line >= len(lines) {
		return nil, false
	}
	lines[line] = ""
	return completionRecipes(ctx, strings.Join(lines, "\n"), opts)
}

func argumentDefaultValueCompletionItems(candidates []recipecompletion.Candidate, argName string, quote bool) []completion {
	items := make([]completion, 0, len(candidates))
	prefix := argName + "="
	for _, candidate := range candidates {
		if !strings.HasPrefix(candidate.Value, prefix) {
			continue
		}
		value := recipeArgumentCompletionLabel(candidate.Value)
		items = append(items, completion{
			Label:      value,
			InsertText: value,
			Kind:       completionKindValue,
			Detail:     candidate.Help,
			Quote:      quote,
		})
	}
	return items
}

func defaultValueNeedsQuote(arg recipe.Argument) bool {
	switch arg.Type {
	case "bool", "int", "float":
		return false
	default:
		return true
	}
}

func valueCompletions(key string) []completion {
	switch key {
	case "profile":
		return []completion{
			{Label: recipe.GoProfile, InsertText: recipe.GoProfile, Kind: completionKindValue, Detail: "Go profile", Quote: true},
			{Label: recipe.NodeProfile, InsertText: recipe.NodeProfile, Kind: completionKindValue, Detail: "Node profile", Quote: true},
		}
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

func recipeArgumentTableParts(table string) (string, string, bool) {
	rest, ok := strings.CutPrefix(table, "recipes.")
	if !ok {
		return "", "", false
	}
	recipeName, argName, ok := strings.Cut(rest, ".arguments.")
	if !ok || recipeName == "" || argName == "" || strings.Contains(argName, ".") {
		return "", "", false
	}
	return recipeName, argName, true
}

func placeholderCompletions(analysis documentAnalysis, recipeName, prefix string, allowVariadic, allowForEach bool) []completion {
	var names []string
	names = append(names, analysis.GlobalVars...)
	names = append(names, analysis.RecipeVars[recipeName]...)
	names = append(names, analysis.Arguments[recipeName]...)
	if allowForEach && analysis.FanOutRecipes[recipeName] {
		names = append(names, recipe.ForEachItemPlaceholder, recipe.ForEachItemHelpPlaceholder, recipe.ForEachItemIndexPlaceholder)
	}
	names = uniqueSorted(names)
	detail := map[string]string{}
	for _, name := range analysis.GlobalVars {
		detail[name] = "Shared placeholder"
	}
	for _, name := range analysis.RecipeVars[recipeName] {
		detail[name] = "Recipe placeholder"
	}
	for _, name := range analysis.Arguments[recipeName] {
		detail[name] = "Argument placeholder"
		if help := analysis.ArgumentHelp[recipeName][name]; help != "" {
			detail[name] = help
		}
	}
	if allowForEach && analysis.FanOutRecipes[recipeName] {
		detail[recipe.ForEachItemPlaceholder] = "Current for_each value"
		detail[recipe.ForEachItemHelpPlaceholder] = "Current for_each help text"
		detail[recipe.ForEachItemIndexPlaceholder] = "Current for_each index"
	}
	var items []completion
	if allowVariadic && strings.HasPrefix("@", prefix) {
		insertText := "@"
		if !strings.HasSuffix(prefix, "}") {
			insertText += "}"
		}
		items = append(items, completion{
			Label:       "{@}",
			InsertText:  insertText,
			Kind:        completionKindVariable,
			Detail:      "Remaining recipe args",
			Placeholder: true,
		})
	}
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		insertText := name
		if !strings.HasSuffix(prefix, "}") {
			insertText += "}"
		}
		items = append(items, completion{
			Label:       "{" + name + "}",
			InsertText:  insertText,
			Kind:        completionKindVariable,
			Detail:      detail[name],
			Placeholder: true,
		})
	}
	return items
}

func forEachPlaceholderCompletionAllowed(table, key string) bool {
	return recipeTable(table) && (key == "cmd" || key == "workdir")
}

func variadicPlaceholderCompletionAllowed(analysis documentAnalysis, prefix string, pos lspPosition) bool {
	key, ok := keyBeforeValue(prefix)
	if !ok {
		return false
	}
	_, valuePrefix, _ := strings.Cut(prefix, "=")
	switch key {
	case "cmd":
		return strings.Contains(valuePrefix, "{")
	default:
		return false
	}
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
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		if a.Length != b.Length {
			return a.Length - b.Length
		}
		return a.Type - b.Type
	})
	tokens = slices.Compact(tokens)
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
		region, endLine, ok := stringValueRegion(lines, lineNo, table, key, shell)
		if !ok {
			continue
		}
		regions = append(regions, region)
		lineNo = endLine
	}
	return regions
}

func stringValueRegion(lines []string, lineNo int, table, key, shell string) (scriptRegion, int, bool) {
	raw := lineAt(lines, lineNo)
	start, quote, ok := stringStart(raw)
	if !ok {
		return scriptRegion{}, lineNo, false
	}
	if quote.Triple {
		bodyStart := start + len(quote.Delimiter)
		if end := strings.Index(raw[bodyStart:], quote.Delimiter); end >= 0 {
			return scriptRegion{
				Shell: shell, Table: table, Key: key, StartLine: lineNo, StartCol: bodyStart,
				EndLine: lineNo, EndCol: bodyStart + end,
			}, lineNo, true
		}
		endLine, endCol := findTripleStringEnd(lines, lineNo+1, quote.Delimiter)
		return scriptRegion{
			Shell: shell, Table: table, Key: key, StartLine: lineNo, StartCol: bodyStart,
			EndLine: endLine, EndCol: endCol,
		}, endLine, true
	}
	bodyStart := start + 1
	bodyEnd := findStringEnd(raw, bodyStart, quote.Delimiter[0])
	return scriptRegion{
		Shell: shell, Table: table, Key: key, StartLine: lineNo, StartCol: bodyStart,
		EndLine: lineNo, EndCol: bodyEnd,
	}, lineNo, true
}

func commandListScriptRegions(lines []string, startLine int, table, key, shell string) ([]scriptRegion, int) {
	return commandListStringRegions(lines, startLine, table, key, shell, func(value string) bool {
		return !recipe.IsRecipeReferenceString(value)
	})
}

func commandListStringRegions(lines []string, startLine int, table, key, shell string, includeString func(string) bool) ([]scriptRegion, int) {
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
						if depth == 1 && includeString(raw[bodyStart:bodyEnd]) {
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
				if depth == 1 && includeString(raw[bodyStart:bodyEnd]) {
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
	return key == "cmd" || key == "pre" || key == "post" || key == "for_each" || key == "shell_prelude" || key == "values"
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
	return mergeParsedShellTokens(tokens, parsedShellSemanticTokens(lines, region))
}

func parsedShellSemanticTokens(lines []string, region scriptRegion) []semanticToken {
	parser, err := scriptref.Parser(region.Shell)
	if err != nil {
		return nil
	}
	file, err := parser.Parse(strings.NewReader(scriptRegionText(lines, region)), "shadowtree")
	if err != nil {
		return nil
	}
	var tokens []semanticToken
	var stack []bool
	cmdSubstDepth := 0
	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if last {
				cmdSubstDepth--
			}
			return true
		}
		isCmdSubst := false
		switch node := node.(type) {
		case *syntax.CallExpr:
			if cmdSubstDepth == 0 {
				break
			}
			if len(node.Args) == 0 {
				break
			}
			if token, ok := shellNodeToken(region, node.Args[0], semanticTokenFunction); ok {
				tokens = append(tokens, token)
			}
			for _, arg := range node.Args[1:] {
				if strings.HasPrefix(arg.Lit(), "-") {
					if token, ok := shellNodeToken(region, arg, semanticTokenParameter); ok {
						tokens = append(tokens, token)
					}
				}
			}
		case *syntax.ParamExp:
			if cmdSubstDepth == 0 {
				break
			}
			if token, ok := shellNodeToken(region, node, semanticTokenVariable); ok {
				tokens = append(tokens, token)
			}
		case *syntax.CmdSubst:
			tokens = append(tokens, shellCommandSubstitutionTokens(region, node)...)
			isCmdSubst = true
			cmdSubstDepth++
		}
		stack = append(stack, isCmdSubst)
		return true
	})
	return tokens
}

func mergeParsedShellTokens(tokens, parsedTokens []semanticToken) []semanticToken {
	if len(parsedTokens) == 0 {
		return tokens
	}
	merged := tokens[:0]
	for _, token := range tokens {
		if semanticTokenOverlapsAny(token, parsedTokens) {
			continue
		}
		merged = append(merged, token)
	}
	return append(merged, parsedTokens...)
}

func semanticTokenOverlapsAny(token semanticToken, others []semanticToken) bool {
	for _, other := range others {
		if token.Line == other.Line && spansOverlap(
			span{Start: token.Start, Length: token.Length},
			span{Start: other.Start, Length: other.Length},
		) {
			return true
		}
	}
	return false
}

func shellCommandSubstitutionTokens(region scriptRegion, node *syntax.CmdSubst) []semanticToken {
	leftLine, leftCol := shellSyntaxPosition(region, node.Left)
	rightLine, rightCol := shellSyntaxPosition(region, node.Right)
	leftLength := 2
	if node.Backquotes {
		leftLength = 1
	}
	return []semanticToken{
		{Line: leftLine, Start: leftCol, Length: leftLength, Type: semanticTokenOperator},
		{Line: rightLine, Start: rightCol, Length: 1, Type: semanticTokenOperator},
	}
}

func shellNodeToken(region scriptRegion, node syntax.Node, tokenType int) (semanticToken, bool) {
	startLine, startCol := shellSyntaxPosition(region, node.Pos())
	endLine, endCol := shellSyntaxPosition(region, node.End())
	if endLine != startLine {
		return semanticToken{}, false
	}
	if endCol <= startCol {
		return semanticToken{}, false
	}
	return semanticToken{Line: startLine, Start: startCol, Length: endCol - startCol, Type: tokenType}, true
}

func shellSyntaxPosition(region scriptRegion, pos syntax.Pos) (int, int) {
	return scriptPosition(region, scriptref.Position{
		Line: max(int(pos.Line())-1, 0),
		Col:  max(int(pos.Col())-1, 0),
	})
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
	return word == "if" || word == "then" || word == "do" || word == "else" || word == "elif" || word == "until" || word == "while"
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
	return ch == '-' || ch == '_' || ch == '.' || ch == '/' || ch == '[' || ch == ']' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
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
		if strings.HasPrefix(line[i:], "{@}") {
			spans = append(spans, span{Start: i, Length: len("{@}")})
			i += len("{@}") - 1
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
	for _, ref := range references[lineNo] {
		if spansOverlap(item, ref) {
			return true
		}
	}
	return false
}

func spansOverlap(a, b span) bool {
	return a.Start < b.Start+b.Length && b.Start < a.Start+a.Length
}

type commandReferenceSpan struct {
	Path      string
	Name      string
	Args      []commandReferenceArgumentSpan
	Table     string
	Key       string
	Line      int
	Start     int
	End       int
	TargetEnd int
}

type commandReferenceArgumentSpan struct {
	Text  string
	Line  int
	Start int
	End   int
}

func (ref commandReferenceSpan) Target() string {
	return (recipe.RecipeReferenceTarget{Path: ref.Path, Name: ref.Name}).Target()
}

type commandReferenceScan struct {
	depth       int
	list        bool
	pending     bool
	table       string
	key         string
	tripleQuote string
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
			scan.table = table
			scan.key = key
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
	type spanKey struct {
		path      string
		name      string
		table     string
		key       string
		line      int
		start     int
		end       int
		targetEnd int
	}
	seen := map[spanKey]bool{}
	out := make([]commandReferenceSpan, 0, len(spans))
	for _, span := range spans {
		key := spanKey{
			path:      span.Path,
			name:      span.Name,
			table:     span.Table,
			key:       span.Key,
			line:      span.Line,
			start:     span.Start,
			end:       span.End,
			targetEnd: span.TargetEnd,
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, span)
	}
	return out
}

func commandReferenceSpansInText(text string, lineNo, offset int, scan *commandReferenceScan) []commandReferenceSpan {
	var spans []commandReferenceSpan
	followsOpen := false
	for i := 0; i < len(text); i++ {
		if scan.tripleQuote != "" {
			end := strings.Index(text[i:], scan.tripleQuote)
			if end < 0 {
				return spans
			}
			i += end + len(scan.tripleQuote) - 1
			scan.tripleQuote = ""
			continue
		}
		switch text[i] {
		case '"', '\'':
			quote := quoteAt(text, i)
			if quote.Triple {
				end := strings.Index(text[i+len(quote.Delimiter):], quote.Delimiter)
				if end < 0 {
					scan.tripleQuote = quote.Delimiter
					return spans
				}
				i += len(quote.Delimiter) + end + len(quote.Delimiter) - 1
				followsOpen = false
				continue
			}
			start := i + 1
			end := findStringEnd(text, start, text[i])
			value := text[start:end]
			if scan.list {
				switch {
				case scan.depth == 1 && recipe.IsRecipeReferenceString(value):
					spans = append(spans, commandReferenceSpanFromString(value, scan.table, scan.key, lineNo, offset+start, offset+end))
				case scan.depth == 2 && followsOpen && strings.HasPrefix(value, "@"):
					spans = append(spans, commandReferenceSpanFromString(value, scan.table, scan.key, lineNo, offset+start, offset+end))
				}
			} else {
				switch {
				case scan.depth == 0:
					scan.pending = false
					if recipe.IsRecipeReferenceString(value) {
						spans = append(spans, commandReferenceSpanFromString(value, scan.table, scan.key, lineNo, offset+start, offset+end))
					}
				case scan.depth == 1 && scan.pending:
					scan.pending = false
					if strings.HasPrefix(value, "@") {
						spans = append(spans, commandReferenceSpanFromString(value, scan.table, scan.key, lineNo, offset+start, offset+end))
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

func commandReferenceSpanFromString(value, table, key string, lineNo, start, end int) commandReferenceSpan {
	lookup := recipeReferenceLookupValue(value)
	ref, ok := recipe.ParseRecipeReference(recipe.Command{lookup})
	if !ok {
		ref.Name = strings.TrimPrefix(lookup, "@")
	}
	return commandReferenceSpan{
		Path:      ref.Path,
		Name:      ref.Name,
		Args:      commandReferenceArgumentSpans(value, ref.Args, lineNo, start),
		Table:     table,
		Key:       key,
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

func commandReferenceArgumentSpans(value string, args []string, lineNo, start int) []commandReferenceArgumentSpan {
	if len(args) == 0 {
		return nil
	}
	open := strings.IndexByte(value, '[')
	if open < 0 {
		return nil
	}
	close := strings.LastIndexByte(value, ']')
	if close < open {
		close = len(value)
	}
	contentStart := open + 1
	content := value[contentStart:close]
	spans := make([]commandReferenceArgumentSpan, 0, len(args))
	offset := 0
	for part := range strings.SplitSeq(content, ",") {
		partStart := offset
		partEnd := partStart + len(part)
		trimmedStart := partStart
		for trimmedStart < partEnd && isShellSpace(content[trimmedStart]) {
			trimmedStart++
		}
		trimmedEnd := partEnd
		for trimmedEnd > trimmedStart && isShellSpace(content[trimmedEnd-1]) {
			trimmedEnd--
		}
		text := content[trimmedStart:trimmedEnd]
		if text != "" {
			spans = append(spans, commandReferenceArgumentSpan{
				Text:  text,
				Line:  lineNo,
				Start: start + contentStart + trimmedStart,
				End:   start + contentStart + trimmedEnd,
			})
		}
		offset = partEnd + 1
	}
	return spans
}

func scriptCommandReferenceSpans(lines []string, regions []scriptRegion) []commandReferenceSpan {
	var spans []commandReferenceSpan
	for _, region := range regions {
		if !recipeScriptReferenceRegion(region) {
			continue
		}
		if !scriptref.SupportedShell(region.Shell) {
			continue
		}
		_, refs, err := scriptref.Parse(region.Shell, scriptRegionText(lines, region))
		if err != nil {
			continue
		}
		for _, ref := range refs {
			spans = append(spans, commandReferenceSpanFromScriptReference(lines, region, ref))
		}
	}
	return spans
}

func scriptRegionText(lines []string, region scriptRegion) string {
	if region.StartLine == region.EndLine {
		return lineAt(lines, region.StartLine)[region.StartCol:region.EndCol]
	}
	var b strings.Builder
	b.WriteString(lineAt(lines, region.StartLine)[region.StartCol:])
	for lineNo := region.StartLine + 1; lineNo < region.EndLine; lineNo++ {
		b.WriteByte('\n')
		b.WriteString(lineAt(lines, lineNo))
	}
	b.WriteByte('\n')
	b.WriteString(lineAt(lines, region.EndLine)[:region.EndCol])
	return b.String()
}

func commandReferenceSpanFromScriptReference(lines []string, region scriptRegion, ref scriptref.Reference) commandReferenceSpan {
	startLine, startCol := scriptPosition(region, ref.Start)
	endLine, endCol := scriptPosition(region, ref.End)
	targetEndLine, targetEndCol := scriptPosition(region, ref.TargetEnd)
	if endLine != startLine {
		endCol = len(lineAt(lines, startLine))
	}
	if targetEndLine != startLine {
		targetEndCol = endCol
	}
	span := commandReferenceSpanFromString(ref.Value, region.Table, region.Key, startLine, startCol, endCol)
	span.TargetEnd = targetEndCol
	for _, arg := range ref.Args {
		argStartLine, argStartCol := scriptPosition(region, arg.Start)
		argEndLine, argEndCol := scriptPosition(region, arg.End)
		if argStartLine != argEndLine {
			continue
		}
		span.Args = append(span.Args, commandReferenceArgumentSpan{
			Text:  arg.Value,
			Line:  argStartLine,
			Start: argStartCol,
			End:   argEndCol,
		})
	}
	return span
}

func scriptPosition(region scriptRegion, pos scriptref.Position) (int, int) {
	line := region.StartLine + pos.Line
	col := pos.Col
	if pos.Line == 0 {
		col += region.StartCol
	}
	return line, col
}

func recipeScriptReferenceRegion(region scriptRegion) bool {
	if region.Key == "shell_prelude" {
		return region.Table == "" || recipeTable(region.Table)
	}
	return recipeReferenceKey(region.Table, region.Key)
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
		return recipeReferenceArgumentValueTokens(line, lineNo, start, end)
	}
	eq += start
	tokens := []semanticToken{{Line: lineNo, Start: start, Length: eq - start, Type: semanticTokenParameter}}
	valueStart := eq + 1
	if valueStart < end {
		tokens = append(tokens, recipeReferenceArgumentValueTokens(line, lineNo, valueStart, end)...)
	}
	return tokens
}

func recipeReferenceArgumentValueTokens(line string, lineNo, start, end int) []semanticToken {
	for _, item := range placeholderSpans(line[start:end]) {
		if item.Length == end-start {
			item.Start += start
			return []semanticToken{{Line: lineNo, Start: item.Start, Length: item.Length, Type: semanticTokenVariable}}
		}
	}
	return []semanticToken{{Line: lineNo, Start: start, Length: end - start, Type: semanticTokenString}}
}

func recipeReferencePrefix(table, prefix string) (string, string, bool) {
	key, ok := keyBeforeValue(prefix)
	if !ok || !recipeReferenceKey(table, key) {
		return "", "", false
	}
	quote := lastOpenQuote(prefix)
	if quote < 0 {
		return "", "", false
	}
	value := prefix[quote+1:]
	if !strings.HasPrefix(value, "@") {
		return "", "", false
	}
	return strings.TrimPrefix(value, "@"), key, true
}

func valueBuiltinReferenceContext(table, key string) bool {
	return key == "for_each" && recipeTable(table) || key == "values" && recipeArgumentTable(table)
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
	case "cmd", "pre", "post", "for_each":
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
	_, _, ok := recipeArgumentTableParts(table)
	return ok
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
	if after, ok := strings.CutPrefix(trimmed, "[["); ok {
		trimmed = after
	} else if after, ok := strings.CutPrefix(trimmed, "["); ok {
		trimmed = after
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
	beforeComment, _, _ := strings.Cut(trimmed, "#")
	trimmed = strings.TrimSuffix(strings.TrimSpace(beforeComment), "\r")
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

func syncOutArrayStringValueAt(lines []string, pos lspPosition) bool {
	for lineNo := pos.Line; lineNo >= 0; lineNo-- {
		line := lineAt(lines, lineNo)
		if lineNo == pos.Line {
			line = linePrefix(line, pos.Character)
		}
		if _, ok := completeTableHeader(line); ok {
			return false
		}
		key, ok := pairKey(line)
		if !ok {
			continue
		}
		if key != "sync_out" {
			continue
		}
		regions, endLine := commandListStringRegions(lines, lineNo, "", key, "", func(string) bool {
			return true
		})
		if pos.Line > endLine {
			return false
		}
		return inScriptRegion(lines, regions, pos)
	}
	return false
}

func placeholderPrefix(prefix string) (string, bool) {
	open := strings.LastIndexByte(prefix, '{')
	close := strings.LastIndexByte(prefix, '}')
	if open < 0 || close > open {
		return "", false
	}
	if prefix[open+1:] == "@" {
		return "@", true
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
