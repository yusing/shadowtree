package shadowtreelsp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	recipecompletion "github.com/yusing/shadowtree/internal/completion"
	"github.com/yusing/shadowtree/internal/configfile"
	"github.com/yusing/shadowtree/internal/recipe"
)

const (
	diagnosticSeverityError   = 1
	diagnosticSeverityWarning = 2
)

type lspDiagnostic struct {
	Range    map[string]any `json:"range"`
	Severity int            `json:"severity"`
	Source   string         `json:"source"`
	Message  string         `json:"message"`
}

func (server *server) publishDiagnostics(ctx context.Context, uri, text string, version *int) error {
	params := map[string]any{
		"uri":         uri,
		"diagnostics": documentDiagnosticsWithOptions(ctx, text, diagnosticOptions{URI: uri}),
	}
	if version != nil {
		params["version"] = *version
	}
	return writeMessage(server.output, map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/publishDiagnostics",
		"params":  params,
	})
}

type diagnosticOptions struct {
	URI string
}

type placeholderQuoteContextCache struct {
	quotes     map[int]byte
	lineStarts []int
}

func documentDiagnosticsWithOptions(ctx context.Context, text string, opts diagnosticOptions) []lspDiagnostic {
	var cfg recipe.Config
	md, err := toml.Decode(text, &cfg)
	if err != nil {
		return []lspDiagnostic{parseDiagnostic(text, err)}
	}

	resolver := diagnosticRecipeResolver{
		ctx:  ctx,
		cfg:  cfg,
		md:   md,
		opts: completionOptionsForURI(opts.URI),
	}
	diagnostics := append(positionDiagnostics(text), commandReferenceDiagnostics(ctx, text, &resolver)...)
	diagnostics = append(diagnostics, placeholderDiagnostics(text, cfg, &resolver)...)
	diagnostics = append(diagnostics, undecodedDiagnostics(text, md)...)
	if err := recipe.ValidateConfig(cfg); err != nil {
		diagnostic := configValidationDiagnostic(text, err)
		if !overlapsErrorDiagnostic(diagnostics, diagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	if len(diagnostics) == 0 {
		if err := resolver.Err(); err != nil {
			diagnostics = append(diagnostics, documentDiagnostic(text, err.Error()))
		}
	}
	if diagnostics == nil {
		return []lspDiagnostic{}
	}
	return diagnostics
}

type diagnosticRecipeResolver struct {
	ctx      context.Context
	cfg      recipe.Config
	md       toml.MetaData
	opts     completionOptions
	loaded   configfile.Loaded
	recipes  map[string]recipe.Recipe
	err      error
	ok       bool
	resolved bool
}

func (resolver *diagnosticRecipeResolver) Recipes() (map[string]recipe.Recipe, bool) {
	loaded, recipes, ok := resolver.Loaded()
	_ = loaded
	return recipes, ok
}

func (resolver *diagnosticRecipeResolver) Loaded() (configfile.Loaded, map[string]recipe.Recipe, bool) {
	if resolver.resolved {
		return resolver.loaded, resolver.recipes, resolver.ok
	}
	path := resolver.opts.ConfigPath
	if path == "" {
		path = configfile.Names[0]
	}
	loaded, err := configfile.LoadConfigWithMeta(path, resolver.cfg, resolver.md)
	if err != nil {
		resolver.err = err
		resolver.resolved = true
		return resolver.loaded, resolver.recipes, false
	}
	resolver.loaded = loaded
	resolver.recipes, _, resolver.err = configfile.ResolveRecipes(resolver.ctx, loaded, completionBaseDir(resolver.opts), configfile.ResolveOptions{})
	resolver.ok = resolver.err == nil
	resolver.resolved = true
	return resolver.loaded, resolver.recipes, resolver.ok
}

func (resolver *diagnosticRecipeResolver) Err() error {
	if !resolver.resolved {
		resolver.Loaded()
	}
	return resolver.err
}

func commandReferenceDiagnostics(ctx context.Context, text string, resolver *diagnosticRecipeResolver) []lspDiagnostic {
	lines := strings.Split(text, "\n")
	refs := commandReferenceSpans(lines)
	if len(refs) == 0 {
		return nil
	}
	recipes, ok := resolver.Recipes()
	if !ok {
		return nil
	}
	completionOpts := resolver.opts
	crossConfigRecipes := map[string]diagnosticCrossConfigResult{}
	var diagnostics []lspDiagnostic
	for _, ref := range refs {
		if ref.isValueBuiltin() {
			continue
		}
		if ref.Name == "" {
			diagnostics = append(diagnostics, lspDiagnostic{
				Range:    lspRange(lineAt(lines, ref.Line), ref.Line, ref.Start, ref.TargetEnd),
				Severity: diagnosticSeverityError,
				Source:   "shadowtree",
				Message:  "empty recipe reference",
			})
			continue
		}
		if strings.Contains(ref.Name, "{") {
			continue
		}
		if ref.Path != "" {
			if strings.Contains(ref.Path, "{") {
				continue
			}
			crossRecipes, targetOpts, err := diagnosticCrossConfigRecipes(ctx, ref.Path, completionOpts, crossConfigRecipes)
			if err != nil {
				diagnostics = append(diagnostics, lspDiagnostic{
					Range:    lspRange(lineAt(lines, ref.Line), ref.Line, ref.Start, ref.TargetEnd),
					Severity: diagnosticSeverityError,
					Source:   "shadowtree",
					Message:  fmt.Sprintf("invalid recipe reference @%s: %v", ref.Target(), err),
				})
				continue
			}
			if crossRecipes == nil {
				continue
			}
			rec, ok := crossRecipes[ref.Name]
			if !ok {
				diagnostics = append(diagnostics, lspDiagnostic{
					Range:    lspRange(lineAt(lines, ref.Line), ref.Line, ref.Start, ref.TargetEnd),
					Severity: diagnosticSeverityError,
					Source:   "shadowtree",
					Message:  "unknown recipe reference @" + ref.Target(),
				})
				continue
			}
			diagnostics = append(diagnostics, commandReferenceArgumentDiagnostics(ctx, lines, ref, rec, crossRecipes, targetOpts)...)
			continue
		}
		rec, ok := recipes[ref.Name]
		if !ok {
			diagnostics = append(diagnostics, lspDiagnostic{
				Range:    lspRange(lineAt(lines, ref.Line), ref.Line, ref.Start, ref.TargetEnd),
				Severity: diagnosticSeverityError,
				Source:   "shadowtree",
				Message:  "unknown recipe reference @" + ref.Name,
			})
			continue
		}
		diagnostics = append(diagnostics, commandReferenceArgumentDiagnostics(ctx, lines, ref, rec, recipes, completionOpts)...)
	}
	return diagnostics
}

func (ref commandReferenceSpan) isValueBuiltin() bool {
	if ref.Path != "" || !recipe.IsBuiltinReferenceName(ref.Name) {
		return false
	}
	return valueBuiltinReferenceContext(ref.Table, ref.Key)
}

func commandReferenceArgumentDiagnostics(ctx context.Context, lines []string, ref commandReferenceSpan, rec recipe.Recipe, recipes map[string]recipe.Recipe, opts completionOptions) []lspDiagnostic {
	if len(ref.Args) == 0 || len(rec.Arguments) == 0 && len(rec.Profiles) == 0 {
		return nil
	}
	usesVariadicArgs := recipe.RecipeUsesVariadicArgs(rec)
	positionals := recipe.PositionalArguments(rec.Arguments)
	nextPositional := 0
	selectedProfile := ""
	var diagnostics []lspDiagnostic
	for _, argSpan := range ref.Args {
		text := strings.TrimSpace(argSpan.Text)
		if text == "" {
			continue
		}
		usesShellExpansion := argSpan.Dynamic
		if text == "--" && usesVariadicArgs {
			break
		}
		name, value, ok := strings.Cut(text, "=")
		if !ok {
			if usesVariadicArgs && strings.HasPrefix(text, "-") {
				continue
			}
			if nextPositional >= len(positionals) {
				if usesVariadicArgs {
					continue
				}
				diagnostics = append(diagnostics, commandReferenceArgumentDiagnostic(lines, argSpan, "unexpected positional argument "+strconv.Quote(text)))
				continue
			}
			name = positionals[nextPositional]
			value = text
			nextPositional++
			if usesShellExpansion {
				continue
			}
		} else if usesShellExpansion && shellExpansionInArgumentName(name) {
			continue
		}
		if name == recipe.ProfileArgumentName && len(rec.Profiles) > 0 {
			if usesShellExpansion {
				selectedProfile = value
				continue
			}
			value = unquoteRecipeReferenceArgumentValue(value)
			if err := recipe.ValidateProfileSelection(rec, selectedProfile, value); err != nil {
				diagnostics = append(diagnostics, commandReferenceArgumentDiagnostic(lines, argSpan, err.Error()))
				continue
			}
			selectedProfile = value
			continue
		}
		arg, exists := rec.Arguments[name]
		if !exists {
			if usesVariadicArgs && strings.HasPrefix(name, "-") {
				continue
			}
			diagnostics = append(diagnostics, commandReferenceArgumentDiagnostic(lines, argSpan, "unknown argument "+strconv.Quote(name)))
			continue
		}
		if usesShellExpansion {
			continue
		}
		value = unquoteRecipeReferenceArgumentValue(value)
		if err := validateRecipeReferenceArgumentValue(name, arg, value); err != nil {
			diagnostics = append(diagnostics, commandReferenceArgumentDiagnostic(lines, argSpan, err.Error()))
			continue
		}
		if len(arg.Values) > 0 && value != "" && recipeReferenceArgumentValueCheckAllowed(arg.Values) && !recipeReferenceArgumentValueExists(ctx, name, value, rec, recipes, opts) {
			diagnostics = append(diagnostics, commandReferenceArgumentDiagnostic(lines, argSpan, fmt.Sprintf("%s: invalid value %q", name, value)))
		}
	}
	return diagnostics
}

func shellExpansionInArgumentName(name string) bool {
	return strings.ContainsAny(name, "$`")
}

func commandReferenceArgumentDiagnostic(lines []string, arg commandReferenceArgumentSpan, message string) lspDiagnostic {
	return lspDiagnostic{
		Range:    lspRange(lineAt(lines, arg.Line), arg.Line, arg.Start, arg.End),
		Severity: diagnosticSeverityError,
		Source:   "shadowtree",
		Message:  message,
	}
}

func placeholderDiagnostics(text string, cfg recipe.Config, resolver *diagnosticRecipeResolver) []lspDiagnostic {
	lines := strings.Split(text, "\n")
	scriptRegionList := scriptRegions(lines, shellSettings(lines))
	regions := placeholderDiagnosticRegions(lines, scriptRegionList)
	if len(regions) == 0 {
		return nil
	}
	var recipes map[string]recipe.Recipe
	effectiveCfg := cfg
	if len(cfg.Include) > 0 {
		loaded, resolvedRecipes, ok := resolver.Loaded()
		if !ok {
			return nil
		}
		effectiveCfg = loaded.Config
		recipes = resolvedRecipes
	}
	var referenceOverlaps map[int][]span
	quoteContexts := map[scriptRegion]placeholderQuoteContextCache{}
	knownVars := map[string]map[string]bool{}
	unsafeArgs := map[string]map[string]bool{}
	var diagnostics []lspDiagnostic
	for _, region := range regions {
		if (recipeTable(region.Table) || recipeSubtable(region.Table, "env")) && recipes == nil {
			recipes, _ = resolver.Recipes()
		}
		known := placeholderDiagnosticNames(effectiveCfg, recipes, region, knownVars)
		if known == nil {
			continue
		}
		for lineNo := region.StartLine; lineNo <= region.EndLine && lineNo < len(lines); lineNo++ {
			line := lines[lineNo]
			start, end := 0, len(line)
			if lineNo == region.StartLine {
				start = region.StartCol
			}
			if lineNo == region.EndLine {
				end = region.EndCol
			}
			for _, placeholder := range placeholdersInRange(line, start, end) {
				item := span{Start: placeholder.Start, Length: placeholder.End - placeholder.Start}
				if referenceOverlaps == nil {
					referenceOverlaps = dynamicCommandReferenceOverlapIndex(commandReferenceSpansWithScriptRegions(lines, scriptRegionList))
				}
				if overlapsCommandReference(referenceOverlaps, lineNo, item) {
					continue
				}
				if diagnostic, ok := placeholderModeDiagnostic(lines, region, lineNo, placeholder, quoteContexts); ok {
					diagnostics = append(diagnostics, diagnostic)
					continue
				}
				if known[placeholder.Name] {
					if diagnostic, ok := unsafeShellPlaceholderDiagnostic(lines, effectiveCfg, recipes, region, lineNo, placeholder, quoteContexts, unsafeArgs); ok {
						diagnostics = append(diagnostics, diagnostic)
					}
					continue
				}
				diagnostics = append(diagnostics, lspDiagnostic{
					Range:    lspRange(line, lineNo, item.Start, item.Start+item.Length),
					Severity: diagnosticSeverityError,
					Source:   "shadowtree",
					Message:  "unknown variable {" + placeholder.Name + "}",
				})
			}
		}
	}
	diagnostics = append(diagnostics, recursiveVarDiagnostics(lines, cfg, regions)...)
	return diagnostics
}

func placeholderModeDiagnostic(lines []string, region scriptRegion, lineNo int, placeholder recipe.Placeholder, quoteContexts map[scriptRegion]placeholderQuoteContextCache) (lspDiagnostic, bool) {
	line := lineAt(lines, lineNo)
	quote := byte(0)
	if scriptKey(region.Key) && (placeholder.Mode == recipe.PlaceholderModeShell || placeholder.Mode == recipe.PlaceholderModeDQ) {
		var ok bool
		quote, ok = placeholderQuoteContext(lines, region, lineNo, placeholder, quoteContexts)
		if !ok {
			return lspDiagnostic{}, false
		}
	}
	if err := recipe.ValidatePlaceholderMode(placeholder, scriptKey(region.Key), quote); err != nil {
		return placeholderDiagnostic(line, lineNo, placeholder, err.Error(), diagnosticSeverityError), true
	}
	return lspDiagnostic{}, false
}

func unsafeShellPlaceholderDiagnostic(lines []string, cfg recipe.Config, recipes map[string]recipe.Recipe, region scriptRegion, lineNo int, placeholder recipe.Placeholder, quoteContexts map[scriptRegion]placeholderQuoteContextCache, unsafeArgs map[string]map[string]bool) (lspDiagnostic, bool) {
	if !scriptKey(region.Key) || placeholder.Mode != recipe.PlaceholderModeDefault || placeholder.Name == "@" {
		return lspDiagnostic{}, false
	}
	quote, ok := placeholderQuoteContext(lines, region, lineNo, placeholder, quoteContexts)
	if !ok || quote != 0 {
		return lspDiagnostic{}, false
	}
	if !unsafeShellArgumentNames(cfg, recipes, region.Table, unsafeArgs)[placeholder.Name] {
		return lspDiagnostic{}, false
	}
	line := lineAt(lines, lineNo)
	return placeholderDiagnostic(line, lineNo, placeholder, placeholder.Match+" expands raw in shell; use "+placeholderSafeModeSuggestion(placeholder.Name), diagnosticSeverityWarning), true
}

func unsafeShellArgumentNames(cfg recipe.Config, recipes map[string]recipe.Recipe, table string, cache map[string]map[string]bool) map[string]bool {
	if cached, ok := cache[table]; ok {
		return cached
	}
	names := map[string]bool{}
	rec, ok := recipeForDiagnosticTable(cfg, recipes, table)
	if ok {
		for name, arg := range rec.Arguments {
			if unsafeDirectShellArgument(arg) {
				names[name] = true
			}
		}
	}
	cache[table] = names
	return names
}

func unsafeDirectShellArgument(arg recipe.Argument) bool {
	switch recipe.ArgumentType(arg) {
	case "path", "rel_path":
		return true
	case "string":
		return !recipe.ArgumentHasEnumValues(arg)
	default:
		return false
	}
}

func placeholderSafeModeSuggestion(name string) string {
	return `"` + "{" + name + "}" + `" or {` + name + ":" + string(recipe.PlaceholderModeRaw) + "}; use {" + name + ":" + string(recipe.PlaceholderModeShell) + "} only inside an unquoted shell word"
}

func placeholderDiagnostic(line string, lineNo int, placeholder recipe.Placeholder, message string, severity int) lspDiagnostic {
	return lspDiagnostic{
		Range:    lspRange(line, lineNo, placeholder.Start, placeholder.End),
		Severity: severity,
		Source:   "shadowtree",
		Message:  message,
	}
}

func placeholderQuoteContext(lines []string, region scriptRegion, lineNo int, placeholder recipe.Placeholder, quoteContexts map[scriptRegion]placeholderQuoteContextCache) (byte, bool) {
	cache, ok := quoteContexts[region]
	if !ok {
		text, lineStarts := scriptRegionTextAndLineStarts(lines, region)
		quotes, err := recipe.ScriptPlaceholderQuoteContexts(text, region.Shell)
		if err != nil {
			return 0, false
		}
		cache = placeholderQuoteContextCache{quotes: quotes, lineStarts: lineStarts}
		quoteContexts[region] = cache
	}
	offset := scriptRegionOffset(region, lineNo, placeholder.Start, cache.lineStarts)
	quote := cache.quotes[offset]
	if quote == 0 {
		quote = recipe.PlaceholderSurroundingQuote(lineAt(lines, lineNo), placeholder.Start, placeholder.End-1)
	}
	return quote, true
}

func scriptRegionOffset(region scriptRegion, lineNo, col int, lineStarts []int) int {
	relativeLine := lineNo - region.StartLine
	if relativeLine < 0 || relativeLine >= len(lineStarts) {
		return 0
	}
	offset := lineStarts[relativeLine] + col
	if lineNo == region.StartLine {
		offset -= region.StartCol
	}
	return offset
}

func dynamicCommandReferenceOverlapIndex(references []commandReferenceSpan) map[int][]span {
	overlaps := map[int][]span{}
	for _, ref := range references {
		if !strings.Contains(ref.Name, "{") && !strings.Contains(ref.Path, "{") {
			continue
		}
		overlaps[ref.Line] = append(overlaps[ref.Line], span{Start: ref.Start, Length: ref.End - ref.Start})
	}
	return overlaps
}

func placeholderDiagnosticNames(cfg recipe.Config, recipes map[string]recipe.Recipe, region scriptRegion, knownNames map[string]map[string]bool) map[string]bool {
	names := map[string]bool{}
	names[recipe.RunIDPlaceholder] = true
	for name := range cfg.Vars {
		names[name] = true
	}
	for name := range cfg.VarCommands {
		names[name] = true
	}
	if varsTable(region.Table) {
		if cached, ok := knownNames[region.Table]; ok {
			return cached
		}
		if region.Table == "vars" {
			knownNames[region.Table] = names
			return names
		}
		rec, ok := recipeForDiagnosticTable(cfg, recipes, region.Table)
		if !ok {
			return nil
		}
		for name := range rec.Vars {
			names[name] = true
		}
		knownNames[region.Table] = names
		return names
	}
	if envTable(region.Table) {
		if cached, ok := knownNames[region.Table]; ok {
			return cached
		}
		if region.Table == "env" {
			knownNames[region.Table] = names
			return names
		}
		rec, ok := recipeForDiagnosticTable(cfg, recipes, region.Table)
		if !ok {
			return nil
		}
		for name := range rec.Vars {
			names[name] = true
		}
		for name := range rec.Arguments {
			names[name] = true
		}
		knownNames[region.Table] = names
		return names
	}
	if !placeholderDiagnosticKey(region.Key) || !recipeTable(region.Table) {
		return nil
	}
	rec, ok := recipeForDiagnosticTable(cfg, recipes, region.Table)
	if !ok {
		return nil
	}
	for name := range rec.Vars {
		names[name] = true
	}
	for name := range rec.Arguments {
		names[name] = true
	}
	if len(rec.ForEach) > 0 && (region.Key == "cmd" || region.Key == "workdir") {
		names[recipe.ForEachItemPlaceholder] = true
		names[recipe.ForEachItemHelpPlaceholder] = true
		names[recipe.ForEachItemIndexPlaceholder] = true
	}
	if region.Key == "cmd" {
		names["@"] = true
	}
	return names
}

func recipeForDiagnosticTable(cfg recipe.Config, recipes map[string]recipe.Recipe, table string) (recipe.Recipe, bool) {
	recipeName := currentRecipe(table)
	rec, ok := recipes[recipeName]
	if !ok {
		rec, ok = cfg.Recipes[recipeName]
	}
	return rec, ok
}

func placeholderDiagnosticRegions(lines []string, scriptRegions []scriptRegion) []scriptRegion {
	regions := slices.Clone(scriptRegions)
	table := ""
	for lineNo := 0; lineNo < len(lines); lineNo++ {
		raw := lines[lineNo]
		if parsed, ok := completeTableHeader(raw); ok {
			table = parsed
			continue
		}
		key, ok := pairKey(raw)
		if !ok || !placeholderDiagnosticValueKey(table, key) {
			continue
		}
		if key == "sync_out" {
			listRegions, endLine := commandListStringRegions(lines, lineNo, table, key, "", func(string) bool {
				return true
			})
			regions = append(regions, listRegions...)
			lineNo = endLine
			continue
		}
		region, endLine, ok := stringValueRegion(lines, lineNo, table, key, "")
		if ok {
			regions = append(regions, region)
			lineNo = endLine
		}
	}
	return regions
}

func placeholderDiagnosticKey(key string) bool {
	switch key {
	case "cmd", "pre", "post", "for_each", "workdir", "sync_out", "shell_prelude", "log":
		return true
	default:
		return false
	}
}

func placeholderDiagnosticValueKey(table, key string) bool {
	if envTable(table) {
		return true
	}
	if varsTable(table) {
		return true
	}
	return key == "workdir" || key == "sync_out" || key == "log"
}

func envTable(table string) bool {
	return table == "env" || recipeSubtable(table, "env")
}

func varsTable(table string) bool {
	return table == "vars" || recipeSubtable(table, "vars")
}

func recipeSubtable(table, suffix string) bool {
	rest, ok := strings.CutPrefix(table, "recipes.")
	if !ok {
		return false
	}
	recipeName, gotSuffix, ok := strings.Cut(rest, ".")
	return ok && recipeName != "" && gotSuffix == suffix
}

func recursiveVarDiagnostics(lines []string, cfg recipe.Config, regions []scriptRegion) []lspDiagnostic {
	definitions := map[string]map[string]string{"vars": cfg.Vars}
	for name, rec := range cfg.Recipes {
		if len(rec.Vars) > 0 {
			definitions["recipes."+name+".vars"] = rec.Vars
		}
	}
	regionsByTable := map[string]map[string]scriptRegion{}
	for _, region := range regions {
		if !varsTable(region.Table) {
			continue
		}
		if regionsByTable[region.Table] == nil {
			regionsByTable[region.Table] = map[string]scriptRegion{}
		}
		regionsByTable[region.Table][region.Key] = region
	}
	var diagnostics []lspDiagnostic
	for table, vars := range definitions {
		for name := range recursiveVarNames(vars) {
			region, ok := regionsByTable[table][name]
			if !ok {
				continue
			}
			line := lineAt(lines, region.StartLine)
			end := region.EndCol
			if region.EndLine != region.StartLine || end > len(line) {
				end = len(line)
			}
			diagnostics = append(diagnostics, lspDiagnostic{
				Range:    lspRange(line, region.StartLine, region.StartCol, end),
				Severity: diagnosticSeverityError,
				Source:   "shadowtree",
				Message:  "recursive variable {" + name + "}",
			})
		}
	}
	return diagnostics
}

func recursiveVarNames(vars map[string]string) map[string]bool {
	const (
		visiting = 1
		done     = 2
	)
	recursive := map[string]bool{}
	state := map[string]uint8{}
	var stack []string
	var visit func(string) bool
	visit = func(name string) bool {
		switch state[name] {
		case visiting:
			for i := len(stack) - 1; i >= 0; i-- {
				recursive[stack[i]] = true
				if stack[i] == name {
					break
				}
			}
			return true
		case done:
			return recursive[name]
		}
		state[name] = visiting
		stack = append(stack, name)
		hasCycle := false
		for _, dep := range placeholderNames(vars[name]) {
			if _, ok := vars[dep]; ok && visit(dep) {
				hasCycle = true
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		if hasCycle {
			recursive[name] = true
		}
		return recursive[name]
	}
	for name := range vars {
		visit(name)
	}
	return recursive
}

func placeholderNames(value string) []string {
	var names []string
	for _, item := range placeholderSpans(value) {
		if placeholder, ok := recipe.ParsePlaceholderAt(value, item.Start); ok && placeholder.Name != "@" {
			names = append(names, placeholder.Name)
		}
	}
	return names
}

func validateRecipeReferenceArgumentValue(name string, arg recipe.Argument, value string) error {
	arg.Default = nil
	arg.Required = false
	_, _, err := recipe.ResolveArguments(recipe.Recipe{Arguments: map[string]recipe.Argument{name: arg}}, []string{name + "=" + value})
	return err
}

func recipeReferenceArgumentValueExists(ctx context.Context, name, value string, rec recipe.Recipe, recipes map[string]recipe.Recipe, opts completionOptions) bool {
	candidates := recipecompletion.GroupedArgumentCandidates(
		ctx,
		"",
		name+"="+value,
		rec,
		recipes,
		lspCompletionCandidateOptions(opts),
	)
	for _, candidate := range candidates {
		if candidate.Value == name+"="+value {
			return true
		}
	}
	return false
}

func recipeReferenceArgumentValueCheckAllowed(values recipe.Command) bool {
	usesFilesystem, ok, err := recipe.ValueBuiltinUsesFilesystem(values)
	return ok && err == nil && !usesFilesystem
}

func unquoteRecipeReferenceArgumentValue(value string) string {
	if len(value) < 2 {
		return value
	}
	quote := value[0]
	if quote != '\'' && quote != '"' || value[len(value)-1] != quote {
		return value
	}
	return value[1 : len(value)-1]
}

type diagnosticCrossConfigResult struct {
	recipes map[string]recipe.Recipe
	opts    completionOptions
	err     error
}

func diagnosticCrossConfigRecipes(ctx context.Context, path string, opts completionOptions, cache map[string]diagnosticCrossConfigResult) (map[string]recipe.Recipe, completionOptions, error) {
	base := completionBaseDir(opts)
	if base == "" {
		return nil, completionOptions{}, nil
	}
	keyPath := path
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(base, keyPath)
	}
	key := filepath.Clean(keyPath)
	if result, ok := cache[key]; ok {
		return result.recipes, result.opts, result.err
	}
	target, err := configfile.ResolveCrossConfigReference(ctx, path, opts.ConfigPath, base, configfile.ResolveOptions{})
	if err != nil {
		cache[key] = diagnosticCrossConfigResult{err: err}
		return nil, completionOptions{}, err
	}
	targetOpts := completionOptions{Dir: target.Dir, ConfigPath: target.Loaded.Path}
	cache[key] = diagnosticCrossConfigResult{recipes: target.Recipes, opts: targetOpts}
	return target.Recipes, targetOpts, nil
}

func parseDiagnostic(text string, err error) lspDiagnostic {
	lines := strings.Split(text, "\n")
	if parseErr, ok := errors.AsType[toml.ParseError](err); ok {
		lineNo := max(parseErr.Position.Line-1, 0)
		line := lineAt(lines, lineNo)
		start := max(parseErr.Position.Col-1, 0)
		length := max(parseErr.Position.Len, 1)
		return lspDiagnostic{
			Range:    lspRange(line, lineNo, start, start+length),
			Severity: diagnosticSeverityError,
			Source:   "shadowtree",
			Message:  parseErr.Message,
		}
	}
	return documentDiagnostic(text, err.Error())
}

func documentDiagnostic(text, message string) lspDiagnostic {
	lines := strings.Split(text, "\n")
	line := lineAt(lines, 0)
	return lspDiagnostic{
		Range:    lspRange(line, 0, 0, len(line)),
		Severity: diagnosticSeverityError,
		Source:   "shadowtree",
		Message:  message,
	}
}

func configValidationDiagnostic(text string, err error) lspDiagnostic {
	message := err.Error()
	if pathErr, ok := errors.AsType[*recipe.ConfigPathError](err); ok {
		index := newTOMLRangeIndex(text)
		if diagnostic, ok := configPathDiagnostic(index, pathErr.ConfigPath(), pathErr.Target(), message); ok {
			return diagnostic
		}
	}
	return documentDiagnostic(text, message)
}

func overlapsErrorDiagnostic(diagnostics []lspDiagnostic, candidate lspDiagnostic) bool {
	if candidate.Severity != diagnosticSeverityError {
		return false
	}
	candidateRange, ok := diagnosticRange(candidate)
	if !ok {
		return false
	}
	return slices.ContainsFunc(diagnostics, func(existing lspDiagnostic) bool {
		if existing.Severity != diagnosticSeverityError {
			return false
		}
		existingRange, ok := diagnosticRange(existing)
		return ok && existingRange.overlaps(candidateRange)
	})
}

type diagnosticTextRange struct {
	line  int
	start int
	end   int
}

func (r diagnosticTextRange) overlaps(other diagnosticTextRange) bool {
	return r.line == other.line && r.start < other.end && other.start < r.end
}

func diagnosticRange(diagnostic lspDiagnostic) (diagnosticTextRange, bool) {
	start, ok := diagnostic.Range["start"].(lspPosition)
	if !ok {
		return diagnosticTextRange{}, false
	}
	end, ok := diagnostic.Range["end"].(lspPosition)
	if !ok || start.Line != end.Line {
		return diagnosticTextRange{}, false
	}
	return diagnosticTextRange{line: start.Line, start: start.Character, end: end.Character}, true
}

func configPathDiagnostic(index tomlRangeIndex, path []string, target recipe.ConfigErrorTarget, message string) (lspDiagnostic, bool) {
	key := tomlPathKey(path)
	switch target {
	case recipe.ConfigErrorTargetKey:
		if item, ok := index.fields[key]; ok {
			return item.diagnostic(item.key, message), true
		}
	case recipe.ConfigErrorTargetTable:
		if item, ok := index.tables[key]; ok {
			return item.diagnostic(item.header, message), true
		}
	default:
		if item, ok := index.fields[key]; ok {
			return item.diagnostic(item.valueOrKey(), message), true
		}
	}
	if item, ok := index.tables[key]; ok {
		return item.diagnostic(item.header, message), true
	}
	if target == recipe.ConfigErrorTargetValue && len(path) > 0 {
		if item, ok := index.tables[tomlPathKey(path[:len(path)-1])]; ok {
			return item.diagnostic(item.header, message), true
		}
	}
	return lspDiagnostic{}, false
}

type tomlRangeIndex struct {
	fields map[string]tomlFieldRange
	tables map[string]tomlTableRange
}

type tomlTextRange struct {
	line  int
	start int
	end   int
}

type tomlFieldRange struct {
	lineText string
	key      tomlTextRange
	value    tomlTextRange
}

func (field tomlFieldRange) valueOrKey() tomlTextRange {
	if field.value.end > field.value.start {
		return field.value
	}
	return field.key
}

func (field tomlFieldRange) diagnostic(r tomlTextRange, message string) lspDiagnostic {
	return lspDiagnostic{
		Range:    lspRange(field.lineText, r.line, r.start, r.end),
		Severity: diagnosticSeverityError,
		Source:   "shadowtree",
		Message:  message,
	}
}

type tomlTableRange struct {
	lineText string
	header   tomlTextRange
}

func (table tomlTableRange) diagnostic(r tomlTextRange, message string) lspDiagnostic {
	return lspDiagnostic{
		Range:    lspRange(table.lineText, r.line, r.start, r.end),
		Severity: diagnosticSeverityError,
		Source:   "shadowtree",
		Message:  message,
	}
}

func newTOMLRangeIndex(text string) tomlRangeIndex {
	lines := strings.Split(text, "\n")
	index := tomlRangeIndex{
		fields: map[string]tomlFieldRange{},
		tables: map[string]tomlTableRange{},
	}
	var tablePath []string
	inMultiline := ""
	for lineNo, line := range lines {
		if inMultiline != "" {
			if strings.Contains(line, inMultiline) {
				inMultiline = ""
			}
			continue
		}
		if path, start, end, ok := tomlTableHeaderPath(line); ok {
			tablePath = path
			index.tables[tomlPathKey(path)] = tomlTableRange{
				lineText: line,
				header:   tomlTextRange{line: lineNo, start: start, end: end},
			}
			continue
		}
		keyPath, keyStart, keyEnd, valueStart, valueEnd, ok := tomlKeyValueSpan(line)
		if !ok {
			continue
		}
		path := slices.Concat(tablePath, keyPath)
		index.fields[tomlPathKey(path)] = tomlFieldRange{
			lineText: line,
			key:      tomlTextRange{line: lineNo, start: keyStart, end: keyEnd},
			value:    tomlTextRange{line: lineNo, start: valueStart, end: valueEnd},
		}
		inMultiline = openedMultilineDelimiter(line[valueStart:valueEnd])
	}
	return index
}

func tomlPathKey(path []string) string {
	return strings.Join(path, "\x00")
}

func tomlTableHeaderPath(line string) ([]string, int, int, bool) {
	trimmed := strings.TrimSpace(stripTOMLComment(line))
	if strings.HasPrefix(trimmed, "[[") {
		return nil, 0, 0, false
	}
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return nil, 0, 0, false
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
	path, ok := parseTOMLPath(body)
	if !ok {
		return nil, 0, 0, false
	}
	start := strings.Index(line, body)
	if start < 0 {
		return nil, 0, 0, false
	}
	return path, start, start + len(body), true
}

func tomlKeyValueSpan(line string) ([]string, int, int, int, int, bool) {
	eq := tomlEqualsIndex(line)
	if eq < 0 {
		return nil, 0, 0, 0, 0, false
	}
	keyText := strings.TrimSpace(line[:eq])
	keyPath, ok := parseTOMLPath(keyText)
	if !ok || len(keyPath) == 0 {
		return nil, 0, 0, 0, 0, false
	}
	keyStart := strings.Index(line, keyText)
	if keyStart < 0 {
		return nil, 0, 0, 0, 0, false
	}
	valueStart, valueEnd, ok := tomlValueSpan(line, eq+1)
	return keyPath, keyStart, keyStart + len(keyText), valueStart, valueEnd, ok
}

func tomlEqualsIndex(line string) int {
	quote := byte(0)
	escape := false
	for i := range len(line) {
		ch := line[i]
		if quote != 0 {
			if quote == '"' && ch == '\\' && !escape {
				escape = true
				continue
			}
			if ch == quote && !escape {
				quote = 0
			}
			escape = false
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case '=':
			return i
		case '#':
			return -1
		}
	}
	return -1
}

func tomlValueSpan(line string, start int) (int, int, bool) {
	for start < len(line) && (line[start] == ' ' || line[start] == '\t') {
		start++
	}
	if start >= len(line) {
		return 0, 0, false
	}
	if strings.HasPrefix(line[start:], `"""`) || strings.HasPrefix(line[start:], `'''`) {
		delimiter := line[start : start+3]
		if end := strings.Index(line[start+3:], delimiter); end >= 0 {
			return start, start + 3 + end + 3, true
		}
		return start, len(line), true
	}
	switch line[start] {
	case '"', '\'':
		quote := line[start]
		escape := false
		for end := start + 1; end < len(line); end++ {
			ch := line[end]
			if quote == '"' && ch == '\\' && !escape {
				escape = true
				continue
			}
			if ch == quote && !escape {
				return start, end + 1, true
			}
			escape = false
		}
		return start, len(line), true
	default:
		end := start
		for end < len(line) {
			switch line[end] {
			case ' ', '\t', '\r', '#':
				return start, end, end > start
			default:
				end++
			}
		}
		return start, end, end > start
	}
}

func stripTOMLComment(line string) string {
	quote := byte(0)
	escape := false
	for i := range len(line) {
		ch := line[i]
		if quote != 0 {
			if quote == '"' && ch == '\\' && !escape {
				escape = true
				continue
			}
			if ch == quote && !escape {
				quote = 0
			}
			escape = false
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case '#':
			return line[:i]
		}
	}
	return line
}

func parseTOMLPath(text string) ([]string, bool) {
	var path []string
	for {
		text = strings.TrimSpace(text)
		if text == "" {
			return path, len(path) > 0
		}
		var part string
		switch text[0] {
		case '"', '\'':
			value, rest, ok := cutQuotedTOMLKey(text)
			if !ok {
				return nil, false
			}
			part = value
			text = rest
		default:
			idx := strings.IndexByte(text, '.')
			if idx < 0 {
				part = strings.TrimSpace(text)
				text = ""
			} else {
				part = strings.TrimSpace(text[:idx])
				text = text[idx:]
			}
		}
		if part == "" {
			return nil, false
		}
		path = append(path, part)
		text = strings.TrimSpace(text)
		if text == "" {
			return path, true
		}
		if text[0] != '.' {
			return nil, false
		}
		text = text[1:]
	}
}

func cutQuotedTOMLKey(text string) (string, string, bool) {
	quote := text[0]
	escape := false
	for end := 1; end < len(text); end++ {
		ch := text[end]
		if quote == '"' && ch == '\\' && !escape {
			escape = true
			continue
		}
		if ch == quote && !escape {
			raw := text[:end+1]
			if quote == '\'' {
				return raw[1 : len(raw)-1], text[end+1:], true
			}
			value, err := strconv.Unquote(raw)
			return value, text[end+1:], err == nil
		}
		escape = false
	}
	return "", "", false
}

func openedMultilineDelimiter(value string) string {
	value = strings.TrimSpace(value)
	for _, delimiter := range []string{`"""`, `'''`} {
		if strings.HasPrefix(value, delimiter) && strings.Count(value, delimiter)%2 == 1 {
			return delimiter
		}
	}
	return ""
}

func positionDiagnostics(text string) []lspDiagnostic {
	lines := strings.Split(text, "\n")
	var diagnostics []lspDiagnostic
	table := ""
	for lineNo, raw := range lines {
		if parsed, ok := completeTableHeader(raw); ok {
			table = parsed
			continue
		}
		if !argumentTable(table) {
			continue
		}
		key, ok := pairKey(raw)
		if !ok || key != "position" {
			continue
		}
		start, end, value, ok := valueSpan(raw)
		if !ok {
			continue
		}
		position, err := strconv.Atoi(value)
		if err != nil || position >= 1 {
			continue
		}
		diagnostics = append(diagnostics, lspDiagnostic{
			Range:    lspRange(raw, lineNo, start, end),
			Severity: diagnosticSeverityError,
			Source:   "shadowtree",
			Message:  "position must be 1 or greater",
		})
	}
	return diagnostics
}

func undecodedDiagnostics(text string, md toml.MetaData) []lspDiagnostic {
	var diagnostics []lspDiagnostic
	for _, key := range md.Undecoded() {
		if len(key) == 0 {
			continue
		}
		if len(key) == 1 && key[0] == "$schema" {
			continue
		}
		keyText := key[len(key)-1]
		diagnostics = append(diagnostics, keyDiagnostic(text, keyText, "unknown field "+key.String()))
	}
	return diagnostics
}

func keyDiagnostic(text, key, message string) lspDiagnostic {
	lines := strings.Split(text, "\n")
	for lineNo, line := range lines {
		pair, ok := pairKey(line)
		if !ok || pair != key {
			continue
		}
		start := strings.Index(line, key)
		if start < 0 {
			break
		}
		return lspDiagnostic{
			Range:    lspRange(line, lineNo, start, start+len(key)),
			Severity: diagnosticSeverityError,
			Source:   "shadowtree",
			Message:  message,
		}
	}
	return documentDiagnostic(text, message)
}

func argumentTable(table string) bool {
	parts := strings.Split(table, ".")
	return len(parts) == 4 && parts[0] == "recipes" && parts[2] == "arguments"
}

func valueSpan(line string) (int, int, string, bool) {
	_, value, ok := strings.Cut(line, "=")
	if !ok {
		return 0, 0, "", false
	}
	start := len(line) - len(value)
	for start < len(line) && (line[start] == ' ' || line[start] == '\t') {
		start++
	}
	end := start
	for end < len(line) {
		switch line[end] {
		case ' ', '\t', '\r', '#':
			return start, end, line[start:end], end > start
		default:
			end++
		}
	}
	return start, end, line[start:end], end > start
}
