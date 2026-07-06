package recipe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/shadowtree/internal/scriptref"
	"mvdan.cc/sh/v3/syntax"
)

const (
	enumValuesName           = "enum"
	globValuesName           = "glob"
	goMainPackagesValuesName = "go-main-packages"
	goModulesValuesName      = "go-modules"
	goPackagesValuesName     = "go-packages"
	linesValuesName          = "lines"
	recipesValuesName        = "recipes"
	varsValuesName           = "vars"
)

const goListTimeout = 5 * time.Second

var builtinReferenceDetails = map[string]string{
	enumValuesName:           "Static argument values",
	globValuesName:           "Globbed filesystem values",
	goMainPackagesValuesName: "Go main package arguments",
	goModulesValuesName:      "Go module directories",
	goPackagesValuesName:     "Go package arguments",
	linesValuesName:          "Values from a text file",
	recipesValuesName:        "Resolved recipe names",
	varsValuesName:           "Recipe placeholder names",
}

type ValueCandidate struct {
	Value string
	Help  string
}

type ValueBuiltinOptions struct {
	Context     context.Context
	Dir         string
	ConfigPath  string
	Recipe      Recipe
	Recipes     map[string]Recipe
	ValuePrefix string
}

type valueBuiltinCall struct {
	name string
	args []string
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
	calls, ok, err := validatedValueBuiltinInvocations(command)
	if err != nil || !ok {
		return "", ok, err
	}
	return calls[0].name, true, nil
}

// ValueBuiltinUsesFilesystem reports whether command is a filesystem-backed value builtin.
func ValueBuiltinUsesFilesystem(command Command) (bool, bool, error) {
	calls, ok, err := validatedValueBuiltinInvocations(command)
	if err != nil || !ok {
		return false, ok, err
	}
	for _, call := range calls {
		switch call.name {
		case globValuesName, goMainPackagesValuesName, goModulesValuesName, goPackagesValuesName, linesValuesName:
			return true, true, nil
		}
	}
	return false, true, nil
}

func BuiltinValues(command Command, opts ValueBuiltinOptions) ([]ValueCandidate, bool, error) {
	calls, ok, err := validatedValueBuiltinInvocations(command)
	if err != nil || !ok {
		return nil, ok, err
	}
	var values []ValueCandidate
	for _, call := range calls {
		callValues, err := builtinValuesForCall(call, opts)
		if err != nil {
			return nil, true, err
		}
		values = append(values, callValues...)
	}
	return values, true, nil
}

func builtinValuesForCall(call valueBuiltinCall, opts ValueBuiltinOptions) ([]ValueCandidate, error) {
	name := call.name
	args := call.args
	switch name {
	case enumValuesName:
		return filterValueCandidates(enumCandidates(args), opts.ValuePrefix), nil
	case globValuesName:
		return globCandidates(valueBuiltinBaseDir(opts), args[0], opts.ValuePrefix)
	case goMainPackagesValuesName:
		return discoverGoMainPackages(valueBuiltinBaseDir(opts), opts.ValuePrefix)
	case goModulesValuesName:
		return discoverGoModules(valueBuiltinBaseDir(opts), opts.ValuePrefix)
	case goPackagesValuesName:
		return discoverGoPackagesContext(opts.Context, valueBuiltinBaseDir(opts), opts.ValuePrefix)
	case linesValuesName:
		return lineCandidates(valueBuiltinBaseDir(opts), args[0], opts.ValuePrefix)
	case recipesValuesName:
		return filterValueCandidates(recipeCandidates(opts.Recipes), opts.ValuePrefix), nil
	case varsValuesName:
		return filterValueCandidates(varCandidates(opts.Recipe), opts.ValuePrefix), nil
	default:
		return nil, nil
	}
}

func validatedValueBuiltinInvocations(command Command) ([]valueBuiltinCall, bool, error) {
	calls, ok, err := valueBuiltinInvocations(command)
	if err != nil || !ok {
		return nil, ok, err
	}
	for _, call := range calls {
		if _, _, err := validateValueBuiltinArgs(call.name, call.args); err != nil {
			return nil, true, err
		}
	}
	return calls, true, nil
}

func validateValueBuiltinArgs(name string, args []string) (string, bool, error) {
	switch name {
	case enumValuesName:
		return name, true, nil
	case globValuesName, linesValuesName:
		if len(args) != 1 {
			return name, true, fmt.Errorf("@%s requires one argument", name)
		}
	case goMainPackagesValuesName, goModulesValuesName, goPackagesValuesName:
		if len(args) != 0 {
			return name, true, fmt.Errorf("@%s does not take arguments", name)
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

func valueBuiltinInvocations(command Command) ([]valueBuiltinCall, bool, error) {
	if len(command) == 0 {
		return nil, false, nil
	}
	if !IsScriptCommand(command) {
		return nil, false, nil
	}
	return scriptValueBuiltinInvocations(ScriptBody(command))
}

func scriptValueBuiltinInvocations(body string) ([]valueBuiltinCall, bool, error) {
	file, refs, err := scriptref.Parse("", body)
	if err != nil {
		return nil, false, nil
	}
	if len(file.Stmts) == 0 || len(refs) == 0 {
		return nil, false, nil
	}
	hasBuiltinRef := false
	for _, refSpan := range refs {
		ref, ok := ParseRecipeReference(Command{refSpan.Value})
		if ok && ref.Path == "" && IsBuiltinReferenceName(ref.Name) {
			hasBuiltinRef = true
			break
		}
	}
	if !hasBuiltinRef {
		return nil, false, nil
	}
	calls := make([]valueBuiltinCall, 0, len(file.Stmts))
	for _, stmt := range file.Stmts {
		call, err := scriptValueBuiltinCall(stmt)
		if err != nil {
			return nil, true, err
		}
		calls = append(calls, call)
	}
	if len(calls) != len(refs) {
		return nil, true, fmt.Errorf("@%s values must contain only simple builtin commands", calls[0].name)
	}
	return calls, true, nil
}

func scriptValueBuiltinCall(stmt *syntax.Stmt) (valueBuiltinCall, error) {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return valueBuiltinCall{}, errors.New("builtin values must contain only simple builtin commands")
	}
	first, err := literalWordValue(call.Args[0])
	if err != nil {
		return valueBuiltinCall{}, err
	}
	ref, ok := ParseRecipeReference(Command{first})
	if !ok || ref.Path != "" || !IsBuiltinReferenceName(ref.Name) {
		return valueBuiltinCall{}, errors.New("builtin values must contain only simple builtin commands")
	}
	if stmt.Negated || stmt.Background || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) > 0 || len(call.Assigns) > 0 {
		return valueBuiltinCall{}, fmt.Errorf("@%s values must be a simple command", ref.Name)
	}
	args := slices.Clone(ref.Args)
	for _, arg := range call.Args[1:] {
		value, err := literalWordValue(arg)
		if err != nil {
			return valueBuiltinCall{}, err
		}
		args = append(args, value)
	}
	return valueBuiltinCall{name: ref.Name, args: args}, nil
}

func enumCandidates(values []string) []ValueCandidate {
	candidates := make([]ValueCandidate, 0, len(values))
	for _, value := range values {
		candidateValue, help := enumCandidateValueHelp(value)
		candidates = append(candidates, ValueCandidate{Value: candidateValue, Help: help})
	}
	return candidates
}

func enumCandidateValueHelp(value string) (string, string) {
	candidateValue, help, ok := strings.Cut(value, "=")
	if !ok || !strings.ContainsAny(help, " \t") {
		return value, ""
	}
	return candidateValue, help
}

func discoverGoModules(baseDir, prefix string) ([]ValueCandidate, error) {
	var candidates []ValueCandidate
	if err := filepath.WalkDir(baseDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != baseDir && skipGoDiscoveryDir(entry.Name()) {
				return filepath.SkipDir
			}
			skip, err := skipGoDiscoveryPrefix(baseDir, path, prefix)
			if err != nil {
				return err
			}
			if skip {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "go.mod" {
			return nil
		}
		dir := filepath.Dir(path)
		value, err := relativeSlashPath(baseDir, dir)
		if err != nil {
			return err
		}
		if prefix != "" && !strings.HasPrefix(value, prefix) {
			return nil
		}
		candidates = append(candidates, ValueCandidate{
			Value: value,
			Help:  goModuleHelp(path, value),
		})
		return nil
	}); err != nil {
		return nil, err
	}
	slices.SortFunc(candidates, func(a, b ValueCandidate) int {
		return strings.Compare(a.Value, b.Value)
	})
	return candidates, nil
}

func goModuleHelp(path, fallback string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	return fallback
}

func discoverGoMainPackages(baseDir, prefix string) ([]ValueCandidate, error) {
	packageHelp := map[string]string{}
	finalDirs := map[string]bool{}
	fset := token.NewFileSet()
	if err := filepath.WalkDir(baseDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != baseDir && skipGoDiscoveryDir(entry.Name()) {
				return filepath.SkipDir
			}
			skip, err := skipGoPackagePrefix(baseDir, path, prefix)
			if err != nil {
				return err
			}
			if skip {
				return filepath.SkipDir
			}
			return nil
		}
		if !goSourceFile(entry.Name()) {
			return nil
		}
		dir := filepath.Dir(path)
		if finalDirs[dir] {
			return nil
		}
		value, err := goPackageValue(baseDir, dir)
		if err != nil {
			return err
		}
		if prefix != "" && !strings.HasPrefix(value, prefix) {
			return nil
		}
		help, ok := mainPackageFileHelp(fset, path)
		if !ok {
			return nil
		}
		if help != "" {
			packageHelp[dir] = help
			finalDirs[dir] = true
			return nil
		}
		if _, exists := packageHelp[dir]; !exists {
			packageHelp[dir] = ""
		}
		finalDirs[dir] = true
		return nil
	}); err != nil {
		return nil, err
	}
	dirs := slices.Sorted(maps.Keys(packageHelp))
	candidates := make([]ValueCandidate, 0, len(dirs))
	for _, dir := range dirs {
		value, err := goPackageValue(baseDir, dir)
		if err != nil {
			return nil, err
		}
		help := packageHelp[dir]
		if help == "" {
			help = value
		}
		candidates = append(candidates, ValueCandidate{Value: value, Help: help})
	}
	return candidates, nil
}

func discoverGoPackages(baseDir, prefix string) ([]ValueCandidate, error) {
	if !goPackagePrefixCanMatch(prefix) {
		return nil, nil
	}
	return discoverGoPackagesContext(context.Background(), baseDir, prefix)
}

func discoverGoPackagesContext(parent context.Context, baseDir, prefix string) ([]ValueCandidate, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, goListTimeout)
	defer cancel()
	seen := map[string]ValueCandidate{}
	addModulePackages := func(moduleDir, moduleValue string) {
		modulePrefix := goModulePackagePrefix(moduleValue, prefix)
		values, err := goListPackageCandidates(ctx, baseDir, moduleDir, goListPackagePattern(modulePrefix))
		if err != nil {
			return
		}
		for _, value := range values {
			seen[value.Value] = value
		}
	}
	addModulePackages(baseDir, ".")
	if _, err := os.Stat(filepath.Join(baseDir, "go.work")); err == nil {
		modules, err := goWorkModules(ctx, baseDir)
		if err != nil {
			return nil, err
		}
		for _, module := range modules {
			if module.Value == "." {
				continue
			}
			moduleValue := "./" + module.Value
			if !valuePrefixOverlaps(moduleValue, prefix) {
				continue
			}
			addModulePackages(filepath.Join(baseDir, filepath.FromSlash(module.Value)), moduleValue)
		}
	}
	return filterValueCandidates(slices.SortedFunc(maps.Values(seen), func(a, b ValueCandidate) int {
		return strings.Compare(a.Value, b.Value)
	}), prefix), nil
}

func goListPackageCandidates(ctx context.Context, baseDir, moduleDir, pattern string) ([]ValueCandidate, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-buildvcs=false", "-f", "{{if not .Error}}{{.Dir}}\t{{.ImportPath}}{{end}}", pattern)
	cmd.Dir = moduleDir
	output, err := cmd.Output()
	if err != nil && len(output) == 0 {
		return nil, err
	}
	return parseGoListPackageCandidates(baseDir, output)
}

func goWorkModules(ctx context.Context, baseDir string) ([]ValueCandidate, error) {
	cmd := exec.CommandContext(ctx, "go", "work", "edit", "-json", "go.work")
	cmd.Dir = baseDir
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var work struct {
		Use []struct {
			DiskPath   string
			ModulePath string
		}
	}
	if err := json.Unmarshal(output, &work); err != nil {
		return nil, err
	}
	candidates := make([]ValueCandidate, 0, len(work.Use))
	for _, use := range work.Use {
		moduleDir := filepath.FromSlash(use.DiskPath)
		if !filepath.IsAbs(moduleDir) {
			moduleDir = filepath.Join(baseDir, moduleDir)
		}
		value, err := relativeSlashPath(baseDir, moduleDir)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, ValueCandidate{
			Value: value,
			Help:  goModuleHelp(filepath.Join(moduleDir, "go.mod"), value),
		})
	}
	return candidates, nil
}

func parseGoListPackageCandidates(baseDir string, output []byte) ([]ValueCandidate, error) {
	var candidates []ValueCandidate
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		dir, importPath, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		value, err := goPackageValue(baseDir, dir)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, ValueCandidate{Value: value, Help: importPath})
	}
	return candidates, nil
}

func mainPackageFileHelp(fset *token.FileSet, path string) (string, bool) {
	file, err := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return "", false
	}
	if file.Name == nil || file.Name.Name != "main" {
		return "", false
	}
	if file.Doc == nil {
		return "", true
	}
	return SingleLine(file.Doc.Text()), true
}

func goSourceFile(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

func skipGoDiscoveryDir(name string) bool {
	switch name {
	case ".git", ".shadowtree", "vendor", "node_modules", "bin", "build", "dist", "out", "target":
		return true
	default:
		return false
	}
}

func skipGoDiscoveryPrefix(baseDir, path, prefix string) (bool, error) {
	return skipValuePrefix(baseDir, path, prefix, relativeSlashPath)
}

func skipGoPackagePrefix(baseDir, path, prefix string) (bool, error) {
	return skipValuePrefix(baseDir, path, prefix, goPackageValue)
}

func skipValuePrefix(baseDir, path, prefix string, valueForPath func(string, string) (string, error)) (bool, error) {
	if prefix == "" || path == baseDir {
		return false, nil
	}
	value, err := valueForPath(baseDir, path)
	if err != nil {
		return false, err
	}
	return !valuePrefixOverlaps(value, prefix), nil
}

func goPackageValue(baseDir, path string) (string, error) {
	value, err := relativeSlashPath(baseDir, path)
	if err != nil {
		return "", err
	}
	if value == "." {
		return value, nil
	}
	return "./" + value, nil
}

func goPackagePrefixCanMatch(prefix string) bool {
	return valuePrefixOverlaps(".", prefix)
}

func goModulePackagePrefix(moduleValue, prefix string) string {
	if moduleValue == "." || prefix == "" {
		return prefix
	}
	if prefix == moduleValue || prefix == moduleValue+"/" || strings.HasPrefix(moduleValue, strings.TrimSuffix(prefix, "/")) {
		return ""
	}
	if strings.HasPrefix(prefix, moduleValue+"/") {
		return "." + strings.TrimPrefix(prefix, moduleValue)
	}
	return prefix
}

func goListPackagePattern(prefix string) string {
	if prefix == "" || strings.HasPrefix(".", prefix) || prefix == "." || prefix == "./" {
		return "./..."
	}
	if !strings.HasPrefix(prefix, "./") {
		return "./..."
	}
	trimmed := strings.TrimPrefix(prefix, "./")
	if trimmed == "" {
		return "./..."
	}
	if strings.HasSuffix(trimmed, "/") {
		return "./" + trimmed + "..."
	}
	parent := path.Dir(trimmed)
	if parent == "." {
		return "./..."
	}
	return "./" + parent + "/..."
}

func valuePrefixOverlaps(value, prefix string) bool {
	return prefix == "" || strings.HasPrefix(value, prefix) || strings.HasPrefix(prefix, value+"/")
}

func relativeSlashPath(baseDir, path string) (string, error) {
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
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
			if rel, err := relativeSlashPath(baseDir, match); err == nil {
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
