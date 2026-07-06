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

const diagnosticSeverityError = 1

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

func documentDiagnostics(ctx context.Context, text string) []lspDiagnostic {
	return documentDiagnosticsWithOptions(ctx, text, diagnosticOptions{})
}

type diagnosticOptions struct {
	URI string
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
		opts: completionOptionsForURI(opts.URI),
	}
	diagnostics := append(positionDiagnostics(text), commandReferenceDiagnostics(ctx, text, &resolver)...)
	diagnostics = append(diagnostics, placeholderDiagnostics(text, cfg, &resolver)...)
	diagnostics = append(diagnostics, undecodedDiagnostics(text, md)...)
	if err := recipe.ValidateConfig(cfg); err != nil && len(diagnostics) == 0 {
		diagnostics = append(diagnostics, documentDiagnostic(text, err.Error()))
	}
	if diagnostics == nil {
		return []lspDiagnostic{}
	}
	return diagnostics
}

type diagnosticRecipeResolver struct {
	ctx      context.Context
	cfg      recipe.Config
	opts     completionOptions
	recipes  map[string]recipe.Recipe
	ok       bool
	resolved bool
}

func (resolver *diagnosticRecipeResolver) Recipes() (map[string]recipe.Recipe, bool) {
	if resolver.resolved {
		return resolver.recipes, resolver.ok
	}
	resolver.recipes, resolver.ok = completionRecipesFromConfig(resolver.ctx, resolver.cfg, resolver.opts)
	resolver.resolved = true
	return resolver.recipes, resolver.ok
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
			targetOpts := crossConfigDiagnosticCompletionOptions(ref.Path, completionOpts)
			crossRecipes, err := diagnosticCrossConfigRecipes(ctx, targetOpts, crossConfigRecipes)
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

func crossConfigDiagnosticCompletionOptions(path string, opts completionOptions) completionOptions {
	base := completionBaseDir(opts)
	if base == "" {
		return completionOptions{}
	}
	targetDir := path
	if !filepath.IsAbs(targetDir) {
		targetDir = filepath.Join(base, targetDir)
	}
	return completionOptions{
		Dir:        targetDir,
		ConfigPath: filepath.Join(targetDir, configfile.Names[0]),
	}
}

func commandReferenceArgumentDiagnostics(ctx context.Context, lines []string, ref commandReferenceSpan, rec recipe.Recipe, recipes map[string]recipe.Recipe, opts completionOptions) []lspDiagnostic {
	if len(ref.Args) == 0 || len(rec.Arguments) == 0 {
		return nil
	}
	usesVariadicArgs := recipe.RecipeUsesVariadicArgs(rec)
	positionals := recipe.PositionalArguments(rec.Arguments)
	nextPositional := 0
	var diagnostics []lspDiagnostic
	for _, argSpan := range ref.Args {
		text := strings.TrimSpace(argSpan.Text)
		if text == "" {
			continue
		}
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
		}
		arg, exists := rec.Arguments[name]
		if !exists {
			if usesVariadicArgs && strings.HasPrefix(name, "-") {
				continue
			}
			diagnostics = append(diagnostics, commandReferenceArgumentDiagnostic(lines, argSpan, "unknown argument "+strconv.Quote(name)))
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
	var recipes map[string]recipe.Recipe
	var referenceOverlaps map[int][]span
	knownVars := map[string]map[string]bool{}
	var diagnostics []lspDiagnostic
	for _, region := range regions {
		if recipeTable(region.Table) && recipes == nil {
			recipes, _ = resolver.Recipes()
		}
		known := placeholderDiagnosticNames(cfg, recipes, region, knownVars)
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
			for _, item := range placeholderSpansInRange(line, start, end) {
				name := line[item.Start+1 : item.Start+item.Length-1]
				if known[name] {
					continue
				}
				if referenceOverlaps == nil {
					referenceOverlaps = dynamicCommandReferenceOverlapIndex(commandReferenceSpansWithScriptRegions(lines, scriptRegionList))
				}
				if overlapsCommandReference(referenceOverlaps, lineNo, item) {
					continue
				}
				diagnostics = append(diagnostics, lspDiagnostic{
					Range:    lspRange(line, lineNo, item.Start, item.Start+item.Length),
					Severity: diagnosticSeverityError,
					Source:   "shadowtree",
					Message:  "unknown variable {" + name + "}",
				})
			}
		}
	}
	diagnostics = append(diagnostics, recursiveVarDiagnostics(lines, cfg, regions)...)
	return diagnostics
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

func placeholderDiagnosticNames(cfg recipe.Config, recipes map[string]recipe.Recipe, region scriptRegion, knownVars map[string]map[string]bool) map[string]bool {
	names := map[string]bool{}
	for name := range cfg.Vars {
		names[name] = true
	}
	for name := range cfg.VarCommands {
		names[name] = true
	}
	if varsTable(region.Table) {
		if cached, ok := knownVars[region.Table]; ok {
			return cached
		}
		if region.Table == "vars" {
			knownVars[region.Table] = names
			return names
		}
		rec, ok := recipeForDiagnosticTable(cfg, recipes, region.Table)
		if !ok {
			return nil
		}
		for name := range rec.Vars {
			names[name] = true
		}
		knownVars[region.Table] = names
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
	case "cmd", "pre", "post", "for_each", "workdir", "sync_out":
		return true
	default:
		return false
	}
}

func placeholderDiagnosticValueKey(table, key string) bool {
	if varsTable(table) {
		return true
	}
	return key == "workdir" || key == "sync_out"
}

func varsTable(table string) bool {
	if table == "vars" {
		return true
	}
	rest, ok := strings.CutPrefix(table, "recipes.")
	if !ok {
		return false
	}
	recipeName, suffix, ok := strings.Cut(rest, ".")
	return ok && recipeName != "" && suffix == "vars"
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
		names = append(names, value[item.Start+1:item.Start+item.Length-1])
	}
	return names
}

func placeholderSpansInRange(line string, start, end int) []span {
	var out []span
	for _, item := range placeholderSpans(line[start:end]) {
		item.Start += start
		if item.Start > 0 && line[item.Start-1] == '$' {
			continue
		}
		out = append(out, item)
	}
	return out
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
	err     error
}

func diagnosticCrossConfigRecipes(ctx context.Context, opts completionOptions, cache map[string]diagnosticCrossConfigResult) (map[string]recipe.Recipe, error) {
	if opts.Dir == "" || opts.ConfigPath == "" {
		return nil, nil
	}
	key := filepath.Clean(opts.Dir)
	if result, ok := cache[key]; ok {
		return result.recipes, result.err
	}
	loaded, err := configfile.Load(opts.ConfigPath)
	if err != nil {
		cache[key] = diagnosticCrossConfigResult{err: err}
		return nil, err
	}
	recipes, _, err := configfile.ResolveRecipes(ctx, loaded, opts.Dir, configfile.ResolveOptions{})
	if err != nil {
		cache[key] = diagnosticCrossConfigResult{err: err}
		return nil, err
	}
	cache[key] = diagnosticCrossConfigResult{recipes: recipes}
	return recipes, nil
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
