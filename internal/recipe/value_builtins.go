package recipe

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/shadowtree/internal/scriptref"
	"mvdan.cc/sh/v3/syntax"
)

const (
	enumValuesName    = "enum"
	globValuesName    = "glob"
	linesValuesName   = "lines"
	recipesValuesName = "recipes"
	varsValuesName    = "vars"
)

var builtinReferenceDetails = map[string]string{
	enumValuesName:    "Static argument values",
	globValuesName:    "Globbed filesystem values",
	linesValuesName:   "Values from a text file",
	recipesValuesName: "Resolved recipe names",
	varsValuesName:    "Recipe placeholder names",
}

type ValueCandidate struct {
	Value string
	Help  string
}

type ValueBuiltinOptions struct {
	Dir         string
	ConfigPath  string
	Recipe      Recipe
	Recipes     map[string]Recipe
	ValuePrefix string
}

// IsReservedRecipeName reports whether name cannot be used for a recipe.
func IsReservedRecipeName(name string) bool {
	return ReservedNames[name] || IsBuiltinReferenceName(name)
}

// BuiltinReferenceNames returns built-in @ command names usable for argument values.
func BuiltinReferenceNames() []string {
	return slices.Sorted(maps.Keys(builtinReferenceDetails))
}

// IsBuiltinReferenceName reports whether name identifies a built-in @ command.
func IsBuiltinReferenceName(name string) bool {
	_, ok := builtinReferenceDetails[name]
	return ok
}

// BuiltinReferenceDetail returns editor/help detail for a built-in @ command.
func BuiltinReferenceDetail(name string) string {
	if detail := builtinReferenceDetails[name]; detail != "" {
		return detail + " (builtin)"
	}
	return ""
}

func ValidateValueBuiltin(command Command) (string, bool, error) {
	name, args, ok, err := valueBuiltinInvocation(command)
	if err != nil || !ok {
		return name, ok, err
	}
	return validateValueBuiltinArgs(name, args)
}

func BuiltinValues(command Command, opts ValueBuiltinOptions) ([]ValueCandidate, bool, error) {
	name, args, ok, err := valueBuiltinInvocation(command)
	if err != nil || !ok {
		return nil, ok, err
	}
	if _, _, err := validateValueBuiltinArgs(name, args); err != nil {
		return nil, true, err
	}
	switch name {
	case enumValuesName:
		return filterValueCandidates(enumCandidates(args), opts.ValuePrefix), true, nil
	case globValuesName:
		values, err := globCandidates(valueBuiltinBaseDir(opts), args[0], opts.ValuePrefix)
		return values, true, err
	case linesValuesName:
		values, err := lineCandidates(valueBuiltinBaseDir(opts), args[0], opts.ValuePrefix)
		return values, true, err
	case recipesValuesName:
		return filterValueCandidates(recipeCandidates(opts.Recipes), opts.ValuePrefix), true, nil
	case varsValuesName:
		return filterValueCandidates(varCandidates(opts.Recipe), opts.ValuePrefix), true, nil
	default:
		return nil, false, nil
	}
}

func validateValueBuiltinArgs(name string, args []string) (string, bool, error) {
	switch name {
	case enumValuesName:
		return name, true, nil
	case globValuesName, linesValuesName:
		if len(args) != 1 {
			return name, true, fmt.Errorf("@%s requires one argument", name)
		}
	case recipesValuesName, varsValuesName:
		if len(args) != 0 {
			return name, true, fmt.Errorf("@%s does not take arguments", name)
		}
	default:
		return name, false, nil
	}
	return name, true, nil
}

func valueBuiltinInvocation(command Command) (string, []string, bool, error) {
	if len(command) == 0 {
		return "", nil, false, nil
	}
	if !IsScriptCommand(command) {
		ref, ok := ParseRecipeReference(command)
		if !ok || ref.Path != "" || !IsBuiltinReferenceName(ref.Name) {
			return "", nil, false, nil
		}
		return ref.Name, slices.Clone(ref.Args), true, nil
	}
	return scriptValueBuiltinInvocation(ScriptBody(command))
}

func scriptValueBuiltinInvocation(body string) (string, []string, bool, error) {
	file, refs, err := scriptref.Parse("", body)
	if err != nil {
		return "", nil, false, nil
	}
	if len(file.Stmts) == 0 || len(refs) == 0 {
		return "", nil, false, nil
	}
	ref, ok := ParseRecipeReference(Command{refs[0].Value})
	if !ok || ref.Path != "" || !IsBuiltinReferenceName(ref.Name) {
		return "", nil, false, nil
	}
	if len(refs) != 1 {
		return ref.Name, nil, true, fmt.Errorf("@%s values must be a single simple command", ref.Name)
	}
	if len(file.Stmts) != 1 {
		return ref.Name, nil, true, fmt.Errorf("@%s values must be a single simple command", ref.Name)
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return ref.Name, nil, true, fmt.Errorf("@%s values must be a single simple command", ref.Name)
	}
	if stmt.Negated || stmt.Background || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) > 0 || len(call.Assigns) > 0 {
		return ref.Name, nil, true, fmt.Errorf("@%s values must be a single simple command", ref.Name)
	}
	args := make([]string, 0, len(call.Args)-1)
	for _, arg := range call.Args[1:] {
		value, err := literalWordValue(arg)
		if err != nil {
			return ref.Name, nil, true, err
		}
		args = append(args, value)
	}
	return ref.Name, args, true, nil
}

func enumCandidates(values []string) []ValueCandidate {
	candidates := make([]ValueCandidate, 0, len(values))
	for _, value := range values {
		candidates = append(candidates, ValueCandidate{Value: value})
	}
	return candidates
}

func lineCandidates(baseDir, name, prefix string) ([]ValueCandidate, error) {
	path := valueBuiltinPath(baseDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseValueCandidateBytes(data, prefix), nil
}

func globCandidates(baseDir, pattern, prefix string) ([]ValueCandidate, error) {
	glob := pattern
	if !filepath.IsAbs(glob) {
		glob = filepath.Join(baseDir, filepath.FromSlash(pattern))
	}
	matches, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	candidates := make([]ValueCandidate, 0, len(matches))
	for _, match := range matches {
		value := match
		if !filepath.IsAbs(pattern) {
			if rel, err := filepath.Rel(baseDir, match); err == nil {
				value = rel
			}
		}
		value = filepath.ToSlash(value)
		if prefix != "" && !strings.HasPrefix(value, prefix) && !strings.HasPrefix(value+"/", prefix) {
			continue
		}
		help := "file"
		if info, err := os.Stat(match); err == nil && info.IsDir() {
			value += "/"
			help = "directory"
		}
		if prefix != "" && !strings.HasPrefix(value, prefix) {
			continue
		}
		candidates = append(candidates, ValueCandidate{Value: value, Help: help})
	}
	return candidates, nil
}

func recipeCandidates(recipes map[string]Recipe) []ValueCandidate {
	names := slices.Sorted(maps.Keys(recipes))
	candidates := make([]ValueCandidate, 0, len(names))
	for _, name := range names {
		candidates = append(candidates, ValueCandidate{Value: name, Help: Help(recipes[name])})
	}
	return candidates
}

func filterValueCandidates(candidates []ValueCandidate, prefix string) []ValueCandidate {
	if prefix == "" {
		return candidates
	}
	out := candidates[:0]
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate.Value, prefix) {
			out = append(out, candidate)
		}
	}
	return out
}

func varCandidates(rec Recipe) []ValueCandidate {
	names := make([]string, 0, len(rec.Vars)+len(rec.Arguments))
	help := map[string]string{}
	for name := range rec.Vars {
		names = append(names, name)
		help[name] = "placeholder"
	}
	for name, arg := range rec.Arguments {
		names = append(names, name)
		help[name] = ArgumentHelp(arg)
	}
	names = slices.Sorted(slices.Values(names))
	names = slices.Compact(names)
	candidates := make([]ValueCandidate, 0, len(names))
	for _, name := range names {
		candidates = append(candidates, ValueCandidate{Value: name, Help: help[name]})
	}
	return candidates
}

func valueBuiltinBaseDir(opts ValueBuiltinOptions) string {
	if opts.ConfigPath != "" {
		return filepath.Dir(opts.ConfigPath)
	}
	if opts.Dir != "" {
		return opts.Dir
	}
	return "."
}

func valueBuiltinPath(baseDir, name string) string {
	name = filepath.FromSlash(name)
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(baseDir, name)
}

// ParseValueCandidates parses command output lines in value<TAB>help format.
func ParseValueCandidates(output string) []ValueCandidate {
	return parseValueCandidateLines(strings.SplitSeq(output, "\n"), "")
}

// ParseValueCandidateBytes parses command output bytes in value<TAB>help format.
func ParseValueCandidateBytes(output []byte, prefix string) []ValueCandidate {
	lines := func(yield func(string) bool) {
		for line := range bytes.SplitSeq(output, []byte("\n")) {
			if !yield(string(line)) {
				return
			}
		}
	}
	return parseValueCandidateLines(lines, prefix)
}

func parseValueCandidateLines(lines func(func(string) bool), prefix string) []ValueCandidate {
	var values []ValueCandidate
	for line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		value, help, _ := strings.Cut(line, "\t")
		if prefix != "" && !strings.HasPrefix(value, prefix) {
			continue
		}
		values = append(values, ValueCandidate{Value: value, Help: help})
	}
	return values
}

func literalWordValue(word *syntax.Word) (string, error) {
	return literalWordParts(word.Parts)
}

func literalWordParts(parts []syntax.WordPart) (string, error) {
	var b strings.Builder
	for _, part := range parts {
		switch part := part.(type) {
		case *syntax.Lit:
			b.WriteString(part.Value)
		case *syntax.SglQuoted:
			b.WriteString(part.Value)
		case *syntax.DblQuoted:
			value, err := literalWordParts(part.Parts)
			if err != nil {
				return "", err
			}
			b.WriteString(value)
		default:
			return "", errors.New("builtin values must be literal strings")
		}
	}
	return b.String(), nil
}
