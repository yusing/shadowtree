package shadowtreelsp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	diagnostics := append(positionDiagnostics(text), commandReferenceDiagnostics(ctx, text, cfg, opts)...)
	diagnostics = append(diagnostics, undecodedDiagnostics(text, md)...)
	if err := recipe.ValidateConfig(cfg); err != nil && len(diagnostics) == 0 {
		diagnostics = append(diagnostics, documentDiagnostic(text, err.Error()))
	}
	if diagnostics == nil {
		return []lspDiagnostic{}
	}
	return diagnostics
}

func commandReferenceDiagnostics(ctx context.Context, text string, cfg recipe.Config, opts diagnosticOptions) []lspDiagnostic {
	lines := strings.Split(text, "\n")
	recipes, ok := diagnosticRecipes(cfg)
	if !ok {
		return nil
	}
	completionOpts := completionOptionsForURI(opts.URI)
	var diagnostics []lspDiagnostic
	for _, ref := range commandReferenceSpans(lines) {
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
			if err := validateCrossConfigReference(ctx, ref, opts); err != nil {
				diagnostics = append(diagnostics, lspDiagnostic{
					Range:    lspRange(lineAt(lines, ref.Line), ref.Line, ref.Start, ref.TargetEnd),
					Severity: diagnosticSeverityError,
					Source:   "shadowtree",
					Message:  err.Error(),
				})
				continue
			}
			crossRecipes, ok := crossConfigCompletionRecipes(ctx, ref.Path, completionOpts)
			if !ok {
				continue
			}
			rec, ok := crossRecipes[ref.Name]
			if !ok {
				continue
			}
			targetOpts := crossConfigDiagnosticCompletionOptions(ref.Path, completionOpts)
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

func diagnosticRecipes(cfg recipe.Config) (map[string]recipe.Recipe, bool) {
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
		ConfigPath: filepath.Join(targetDir, ".shadowtree.toml"),
	}
}

func commandReferenceArgumentDiagnostics(ctx context.Context, lines []string, ref commandReferenceSpan, rec recipe.Recipe, recipes map[string]recipe.Recipe, opts completionOptions) []lspDiagnostic {
	if len(ref.Args) == 0 || len(rec.Arguments) == 0 {
		return nil
	}
	positionals := recipe.PositionalArguments(rec.Arguments)
	nextPositional := 0
	var diagnostics []lspDiagnostic
	for _, argSpan := range ref.Args {
		text := strings.TrimSpace(argSpan.Text)
		if text == "" {
			continue
		}
		name, value, ok := strings.Cut(text, "=")
		if !ok {
			if nextPositional >= len(positionals) {
				diagnostics = append(diagnostics, commandReferenceArgumentDiagnostic(lines, argSpan, "unexpected positional argument "+strconv.Quote(text)))
				continue
			}
			name = positionals[nextPositional]
			value = text
			nextPositional++
		}
		arg, exists := rec.Arguments[name]
		if !exists {
			diagnostics = append(diagnostics, commandReferenceArgumentDiagnostic(lines, argSpan, "unknown argument "+strconv.Quote(name)))
			continue
		}
		value = unquoteRecipeReferenceArgumentValue(value)
		if err := validateRecipeReferenceArgumentValue(name, arg, value); err != nil {
			diagnostics = append(diagnostics, commandReferenceArgumentDiagnostic(lines, argSpan, err.Error()))
			continue
		}
		if len(arg.Values) > 0 && value != "" && !recipeReferenceArgumentValueExists(ctx, name, value, rec, recipes, opts) {
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
		recipecompletion.Options{Dir: opts.Dir, ConfigPath: opts.ConfigPath},
	)
	for _, candidate := range candidates {
		if candidate.Value == name+"="+value {
			return true
		}
	}
	return false
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

func validateCrossConfigReference(ctx context.Context, ref commandReferenceSpan, opts diagnosticOptions) error {
	base, ok := lspConfigDir(opts.URI)
	if !ok {
		return nil
	}
	targetDir := ref.Path
	if !filepath.IsAbs(targetDir) {
		targetDir = filepath.Join(base, targetDir)
	}
	info, err := os.Stat(targetDir)
	if err != nil {
		return fmt.Errorf("invalid recipe reference @%s: %w", ref.Target(), err)
	}
	if !info.IsDir() {
		return fmt.Errorf("invalid recipe reference @%s: not a directory", ref.Target())
	}
	loaded, err := configfile.Load(filepath.Join(targetDir, ".shadowtree.toml"))
	if err != nil {
		return fmt.Errorf("invalid recipe reference @%s: %w", ref.Target(), err)
	}
	recipes, _, err := configfile.ResolveRecipes(ctx, loaded, targetDir, configfile.ResolveOptions{})
	if err != nil {
		return fmt.Errorf("invalid recipe reference @%s: %w", ref.Target(), err)
	}
	if _, ok := recipes[ref.Name]; !ok {
		return fmt.Errorf("unknown recipe reference @%s", ref.Target())
	}
	return nil
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
