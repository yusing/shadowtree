package shadowtreelsp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
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

func (server *server) publishDiagnostics(uri, text string, version *int) error {
	params := map[string]any{
		"uri":         uri,
		"diagnostics": documentDiagnosticsWithOptions(text, diagnosticOptions{URI: uri}),
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

func documentDiagnostics(text string) []lspDiagnostic {
	return documentDiagnosticsWithOptions(text, diagnosticOptions{})
}

type diagnosticOptions struct {
	URI string
}

func documentDiagnosticsWithOptions(text string, opts diagnosticOptions) []lspDiagnostic {
	var cfg recipe.Config
	md, err := toml.Decode(text, &cfg)
	if err != nil {
		return []lspDiagnostic{parseDiagnostic(text, err)}
	}

	diagnostics := append(positionDiagnostics(text), commandReferenceDiagnostics(text, cfg, opts)...)
	diagnostics = append(diagnostics, undecodedDiagnostics(text, md)...)
	if err := recipe.ValidateConfig(cfg); err != nil && len(diagnostics) == 0 {
		diagnostics = append(diagnostics, documentDiagnostic(text, err.Error()))
	}
	if diagnostics == nil {
		return []lspDiagnostic{}
	}
	return diagnostics
}

func commandReferenceDiagnostics(text string, cfg recipe.Config, opts diagnosticOptions) []lspDiagnostic {
	lines := strings.Split(text, "\n")
	valid := map[string]bool{}
	for _, name := range recipe.BuiltinNames(recipe.GoProfile) {
		valid[name] = true
	}
	for name := range cfg.Recipes {
		valid[name] = true
	}
	var diagnostics []lspDiagnostic
	for _, ref := range commandReferenceSpans(lines) {
		if ref.Name == "" {
			diagnostics = append(diagnostics, lspDiagnostic{
				Range:    lspRange(lineAt(lines, ref.Line), ref.Line, ref.Start, ref.End),
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
			if err := validateCrossConfigReference(ref, opts); err != nil {
				diagnostics = append(diagnostics, lspDiagnostic{
					Range:    lspRange(lineAt(lines, ref.Line), ref.Line, ref.Start, ref.End),
					Severity: diagnosticSeverityError,
					Source:   "shadowtree",
					Message:  err.Error(),
				})
			}
			continue
		}
		if !valid[ref.Name] {
			diagnostics = append(diagnostics, lspDiagnostic{
				Range:    lspRange(lineAt(lines, ref.Line), ref.Line, ref.Start, ref.End),
				Severity: diagnosticSeverityError,
				Source:   "shadowtree",
				Message:  "unknown recipe reference @" + ref.Name,
			})
		}
	}
	return diagnostics
}

func validateCrossConfigReference(ref commandReferenceSpan, opts diagnosticOptions) error {
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
	recipes, _, err := configfile.ResolveRecipes(nil, loaded, targetDir, configfile.ResolveOptions{})
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
