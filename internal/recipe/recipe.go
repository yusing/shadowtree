package recipe

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	GoProfile   = "go"
	NodeProfile = "node"
)
const scriptCommand = "__shadowtree_script__"
const scriptArg0 = "shadowtree"
const recipeReferencePrefix = "@"
const variadicArgsPlaceholder = "{@}"
const (
	ForEachItemPlaceholder      = "item"
	ForEachItemHelpPlaceholder  = "item_help"
	ForEachItemIndexPlaceholder = "item_index"
)

var ReservedNames = map[string]bool{
	"recipes":    true,
	"init":       true,
	"config":     true,
	"exec":       true,
	"completion": true,
	"help":       true,
	"version":    true,
	"__complete": true,
}

type Command []string

func (command *Command) UnmarshalTOML(value any) error {
	decoded, err := decodeCommand(value)
	if err != nil {
		return err
	}
	*command = decoded
	return nil
}

type Config struct {
	Profile      string             `toml:"profile"`
	Env          map[string]string  `toml:"env"`
	Vars         map[string]string  `toml:"vars"`
	VarCommands  map[string]Command `toml:"var_commands"`
	Shell        string             `toml:"shell"`
	ShellPrelude string             `toml:"shell_prelude"`
	SyncOut      []string           `toml:"sync_out"`
	Recipes      map[string]Recipe  `toml:"recipes"`
}

type Recipe struct {
	Help         string              `toml:"help"`
	Arguments    map[string]Argument `toml:"arguments"`
	Vars         map[string]string   `toml:"vars"`
	Shell        string              `toml:"shell"`
	ShellPrelude string              `toml:"shell_prelude"`
	Sandboxed    *bool               `toml:"sandboxed"`
	ForEach      Command             `toml:"for_each"`
	Workdir      string              `toml:"workdir"`
	Cmd          Command             `toml:"cmd"`
	Pre          []Command           `toml:"pre"`
	Post         []Command           `toml:"post"`
	Env          map[string]string   `toml:"env"`
	SyncOut      []string            `toml:"sync_out"`
}

type Argument struct {
	Help     string  `toml:"help"`
	Type     string  `toml:"type"`
	PathKind string  `toml:"path_kind"`
	Position int     `toml:"position"`
	Required bool    `toml:"required"`
	Default  any     `toml:"default"`
	Values   Command `toml:"values"`
}

type Resolved struct {
	Name       string
	Recipe     Recipe
	Main       Command
	SyncOut    []string
	Sandboxed  bool
	GlobalEnv  map[string]string
	ConfigPath string
	Profile    string
}

// RecipeReferenceTarget is a parsed @recipe or @path:recipe command target.
type RecipeReferenceTarget struct {
	Path string
	Name string
	Args []string
}

var placeholderPattern = regexp.MustCompile(`\{[A-Za-z_][A-Za-z0-9_]*\}`)
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var (
	supportedProfiles = []string{GoProfile, NodeProfile}
	goBuiltinNames    = []string{"build", "check", "fmt", "generate", "lint", "run", "test", "test-race", "tidy", "vet"}
	nodeBuiltinNames  = []string{"install", "dev", "build", "start", "test", "lint", "fmt", "typecheck", "check"}
)

type BuiltinOptions struct {
	Dir string
}

// BuiltinNames returns the built-in recipe names for profile.
func BuiltinNames(profile string) []string {
	switch profile {
	case GoProfile:
		return slices.Clone(goBuiltinNames)
	case NodeProfile:
		return slices.Clone(nodeBuiltinNames)
	default:
		return nil
	}
}

func SupportsProfile(profile string) bool {
	return slices.Contains(supportedProfiles, profile)
}

func Builtins(profile string, opts BuiltinOptions) map[string]Recipe {
	if profile != GoProfile {
		if profile == NodeProfile {
			return nodeBuiltins(opts)
		}
		return map[string]Recipe{}
	}
	defaultGoPackageArgument := Argument{Type: "rel_path", Default: "./..."}
	lint := Recipe{
		Help: "Run Go lint checks.",
		Cmd:  Command{"golangci-lint", "run", "{pkg}", "{@}"},
		Arguments: map[string]Argument{
			"pkg": defaultGoPackageArgument,
		},
	}
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		lint = Recipe{
			Help: "Run Go static checks with go vet.",
			Cmd:  Command{"go", "vet", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		}
	}
	return map[string]Recipe{
		"check": {
			Help: "Run vet and tests.",
			Cmd:  ScriptCommand(`set -e; @vet {@}; @test {@}`),
		},
		"test": {
			Help: "Run Go tests.",
			Cmd:  Command{"go", "test", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		},
		"test-race": {
			Help: "Run Go tests with the race detector.",
			Cmd:  Command{"go", "test", "-race", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		},
		"vet": {
			Help: "Run go vet.",
			Cmd:  Command{"go", "vet", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		},
		"lint": lint,
		"fmt": {
			Help: "Format Go source files.",
			Cmd:  Command{"gofmt", "-w", "{target}", "{@}"},
			Arguments: map[string]Argument{
				"target": {Type: "rel_path", Default: "."},
			},
			Sandboxed: new(false),
		},
		"build": {
			Help: "Build Go packages.",
			Cmd:  Command{"go", "build", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		},
		"generate": {
			Help: "Run go generate.",
			Cmd:  Command{"go", "generate", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		},
		"run": {
			Help: "Run a Go command.",
			Cmd:  Command{"go", "run", "{command}", "{@}"},
			Arguments: map[string]Argument{
				"command": {
					Type:     "rel_path",
					Position: 1,
					Required: true,
				},
			},
		},
		"tidy": {
			Help:      "Tidy Go module files.",
			Cmd:       Command{"go", "mod", "tidy"},
			Sandboxed: new(false),
		},
	}
}

func MergeRecipes(base, overrides map[string]Recipe) (map[string]Recipe, error) {
	merged := maps.Clone(base)
	for name, override := range overrides {
		if IsReservedRecipeName(name) {
			return nil, fmt.Errorf("recipe name %q is reserved", name)
		}
		merged[name] = MergeRecipe(merged[name], override)
	}
	return merged, nil
}

func MergeRecipe(base, override Recipe) Recipe {
	out := base
	if override.Help != "" {
		out.Help = override.Help
	}
	if override.Arguments != nil {
		out.Arguments = mergeArguments(out.Arguments, override.Arguments)
	}
	if override.Vars != nil {
		out.Vars = maps.Clone(override.Vars)
	}
	if override.Shell != "" {
		out.Shell = override.Shell
	}
	if override.ShellPrelude != "" {
		out.ShellPrelude = override.ShellPrelude
	}
	if override.Sandboxed != nil {
		out.Sandboxed = new(*override.Sandboxed)
	}
	if len(override.ForEach) > 0 {
		out.ForEach = slices.Clone(override.ForEach)
	}
	if override.Workdir != "" {
		out.Workdir = override.Workdir
	}
	if len(override.Cmd) > 0 {
		out.Cmd = slices.Clone(override.Cmd)
	}
	if override.Pre != nil {
		out.Pre = cloneCommands(override.Pre)
	}
	if override.Post != nil {
		out.Post = cloneCommands(override.Post)
	}
	if override.Env != nil {
		out.Env = maps.Clone(override.Env)
	}
	if override.SyncOut != nil {
		out.SyncOut = slices.Clone(override.SyncOut)
	}
	return out
}

func ApplyGlobals(recipes map[string]Recipe, vars map[string]string, shell, shellPrelude string) map[string]Recipe {
	out := maps.Clone(recipes)
	for name, rec := range out {
		rec.Vars = mergeStringMaps(vars, rec.Vars)
		if rec.Shell == "" {
			rec.Shell = shell
		}
		rec.ShellPrelude = joinShell(shellPrelude, rec.ShellPrelude)
		out[name] = rec
	}
	return out
}

func Resolve(name string, rec Recipe, cliArgs, globalSyncOut []string, globalEnv map[string]string, configPath, profile string) (Resolved, error) {
	if len(rec.Cmd) == 0 {
		return Resolved{}, fmt.Errorf("recipe %q has no cmd", name)
	}
	values, variadicArgs, err := resolveArguments(rec, cliArgs)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q args: %w", name, err)
	}
	values = mergeStringMaps(rec.Vars, values)
	commandValues := values
	if len(rec.ForEach) > 0 {
		commandValues = mergeStringMaps(values, forEachPlaceholderSentinels())
	}
	cmd, err := expandCommand(rec.Cmd, commandValues, variadicArgs)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q cmd: %w", name, err)
	}
	cmd = CommandWithRecipeReference(cmd, rec.Shell, rec.ShellPrelude)
	pre, err := expandCommands(rec.Pre, values, nil)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q pre: %w", name, err)
	}
	for i := range pre {
		pre[i] = CommandWithRecipeReference(pre[i], rec.Shell, rec.ShellPrelude)
	}
	post, err := expandCommands(rec.Post, values, nil)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q post: %w", name, err)
	}
	for i := range post {
		post[i] = CommandWithRecipeReference(post[i], rec.Shell, rec.ShellPrelude)
	}
	if containsVariadicArgsPlaceholder(globalSyncOut) || containsVariadicArgsPlaceholder(rec.SyncOut) {
		return Resolved{}, fmt.Errorf("recipe %q sync_out: %s is not supported in sync_out", name, variadicArgsPlaceholder)
	}
	sandboxed := RecipeSandboxed(rec)
	var syncOut []string
	if sandboxed {
		syncOut, err = expandStrings(slices.Concat(globalSyncOut, rec.SyncOut), values, nil)
		if err != nil {
			return Resolved{}, fmt.Errorf("recipe %q sync_out: %w", name, err)
		}
	}
	var forEach Command
	if len(rec.ForEach) > 0 {
		forEach, err = expandCommand(rec.ForEach, values, nil)
		if err != nil {
			return Resolved{}, fmt.Errorf("recipe %q for_each: %w", name, err)
		}
	}
	workdir := rec.Workdir
	if workdir != "" {
		expanded, err := expandStrings([]string{workdir}, commandValues, nil)
		if err != nil {
			return Resolved{}, fmt.Errorf("recipe %q workdir: %w", name, err)
		}
		workdir = expanded[0]
	}
	if err := ValidateCommand(cmd); err != nil {
		return Resolved{}, fmt.Errorf("recipe %q cmd: %w", name, err)
	}
	if len(forEach) > 0 {
		if err := ValidateCommand(forEach); err != nil {
			return Resolved{}, fmt.Errorf("recipe %q for_each: %w", name, err)
		}
	}
	for i, command := range pre {
		if err := ValidateCommand(command); err != nil {
			return Resolved{}, fmt.Errorf("recipe %q pre[%d]: %w", name, i, err)
		}
	}
	for i, command := range post {
		if err := ValidateCommand(command); err != nil {
			return Resolved{}, fmt.Errorf("recipe %q post[%d]: %w", name, i, err)
		}
	}
	resolvedRecipe := rec
	resolvedRecipe.ForEach = forEach
	resolvedRecipe.Workdir = workdir
	resolvedRecipe.Pre = pre
	resolvedRecipe.Post = post
	return Resolved{
		Name:       name,
		Recipe:     resolvedRecipe,
		Main:       cmd,
		SyncOut:    syncOut,
		Sandboxed:  sandboxed,
		GlobalEnv:  maps.Clone(globalEnv),
		ConfigPath: configPath,
		Profile:    profile,
	}, nil
}

func RecipeSandboxed(rec Recipe) bool {
	return rec.Sandboxed == nil || *rec.Sandboxed
}

func ValidateConfig(cfg Config) error {
	if err := ValidateVarsMap("vars", cfg.Vars); err != nil {
		return err
	}
	if err := ValidateCommandsMap("var_commands", cfg.VarCommands); err != nil {
		return err
	}
	for name, rec := range cfg.Recipes {
		if IsReservedRecipeName(name) {
			return fmt.Errorf("recipe name %q is reserved", name)
		}
		if err := ValidateArguments(rec.Arguments); err != nil {
			return fmt.Errorf("recipe %q arguments: %w", name, err)
		}
		if err := ValidateVarsMap(fmt.Sprintf("recipe %q vars", name), rec.Vars); err != nil {
			return err
		}
		if len(rec.Cmd) > 0 {
			if err := ValidateCommand(rec.Cmd); err != nil {
				return fmt.Errorf("recipe %q cmd: %w", name, err)
			}
		}
		if len(rec.ForEach) > 0 {
			if err := validateValueCommand("for_each", rec.ForEach); err != nil {
				return fmt.Errorf("recipe %q for_each: %w", name, err)
			}
		}
		for i, command := range rec.Pre {
			if containsVariadicArgsPlaceholder(command) {
				return fmt.Errorf("recipe %q pre[%d]: %s is supported only in cmd", name, i, variadicArgsPlaceholder)
			}
			if err := ValidateCommand(command); err != nil {
				return fmt.Errorf("recipe %q pre[%d]: %w", name, i, err)
			}
		}
		for i, command := range rec.Post {
			if containsVariadicArgsPlaceholder(command) {
				return fmt.Errorf("recipe %q post[%d]: %s is supported only in cmd", name, i, variadicArgsPlaceholder)
			}
			if err := ValidateCommand(command); err != nil {
				return fmt.Errorf("recipe %q post[%d]: %w", name, i, err)
			}
		}
	}
	return nil
}

func ResolveArguments(rec Recipe, cliArgs []string) (map[string]string, []string, error) {
	values, variadicArgs, err := resolveArguments(rec, cliArgs)
	return values, variadicArgs, err
}

func resolveArguments(rec Recipe, cliArgs []string) (map[string]string, []string, error) {
	usesVariadicArgs := recipeUsesVariadicArgs(rec)
	if len(rec.Arguments) == 0 {
		if len(cliArgs) == 0 {
			return map[string]string{}, nil, nil
		}
		if usesVariadicArgs {
			variadicArgs := cliArgs
			if cliArgs[0] == "--" {
				variadicArgs = cliArgs[1:]
			}
			return map[string]string{}, slices.Clone(variadicArgs), nil
		}
		if key, _, ok := strings.Cut(cliArgs[0], "="); ok && !strings.HasPrefix(key, "-") {
			return nil, nil, fmt.Errorf("unknown argument %q", key)
		}
		return nil, nil, fmt.Errorf("unexpected positional argument %q", cliArgs[0])
	}
	values := map[string]string{}
	for name, arg := range rec.Arguments {
		if arg.Default != nil {
			value, err := defaultValueString(arg.Default)
			if err != nil {
				return nil, nil, fmt.Errorf("%s default: %w", name, err)
			}
			if err := validateArgumentValue(name, arg, value); err != nil {
				return nil, nil, err
			}
			value, err = expandPathArgumentValue(arg, value)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", name, err)
			}
			values[name] = value
		}
	}
	positionals := PositionalArguments(rec.Arguments)
	nextPositional := 0
	var variadicArgs []string
	for i, token := range cliArgs {
		if token == "" {
			continue
		}
		if token == "--" && usesVariadicArgs {
			variadicArgs = append(variadicArgs, cliArgs[i+1:]...)
			break
		}
		if key, value, ok := strings.Cut(token, "="); ok {
			arg, exists := rec.Arguments[key]
			if !exists {
				if usesVariadicArgs && strings.HasPrefix(key, "-") {
					variadicArgs = append(variadicArgs, token)
					continue
				}
				return nil, nil, fmt.Errorf("unknown argument %q", key)
			}
			if err := validateArgumentValue(key, arg, value); err != nil {
				return nil, nil, err
			}
			expanded, err := expandPathArgumentValue(arg, value)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", key, err)
			}
			values[key] = expanded
			continue
		}
		if usesVariadicArgs && strings.HasPrefix(token, "-") {
			variadicArgs = append(variadicArgs, token)
			continue
		}
		if nextPositional >= len(positionals) {
			if usesVariadicArgs {
				variadicArgs = append(variadicArgs, token)
				continue
			}
			return nil, nil, fmt.Errorf("unexpected positional argument %q", token)
		}
		name := positionals[nextPositional]
		nextPositional++
		arg := rec.Arguments[name]
		if err := validateArgumentValue(name, arg, token); err != nil {
			return nil, nil, err
		}
		expanded, err := expandPathArgumentValue(arg, token)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}
		values[name] = expanded
	}
	for name, arg := range rec.Arguments {
		if arg.Required {
			if _, ok := values[name]; !ok {
				return nil, nil, fmt.Errorf("missing required argument %q", name)
			}
		}
	}
	return values, variadicArgs, nil
}

func PositionalArguments(args map[string]Argument) []string {
	type positional struct {
		name     string
		position int
	}
	var values []positional
	for name, arg := range args {
		if arg.Position > 0 {
			values = append(values, positional{name: name, position: arg.Position})
		}
	}
	slices.SortFunc(values, func(a, b positional) int {
		return a.position - b.position
	})
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, value.name)
	}
	return names
}

func ValidateArguments(args map[string]Argument) error {
	positions := map[int]string{}
	for name, arg := range args {
		if strings.TrimSpace(name) == "" {
			return errors.New("empty argument name")
		}
		if strings.ContainsAny(name, "=()[], \t\r\n") {
			return fmt.Errorf("invalid argument name %q", name)
		}
		if err := validateArgumentType(arg.Type); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := validateArgumentPathKind(name, arg); err != nil {
			return err
		}
		if arg.Position < 0 {
			return fmt.Errorf("%s: position must be positive", name)
		}
		if arg.Position > 0 {
			if existing, ok := positions[arg.Position]; ok {
				return fmt.Errorf("%s: position %d already used by %s", name, arg.Position, existing)
			}
			positions[arg.Position] = name
		}
		if arg.Default != nil {
			value, err := defaultValueString(arg.Default)
			if err != nil {
				return fmt.Errorf("%s default: %w", name, err)
			}
			if err := validateArgumentValue(name, arg, value); err != nil {
				return err
			}
		}
		if arg.Values != nil {
			if err := validateValueCommand("values", arg.Values); err != nil {
				return fmt.Errorf("%s values: %w", name, err)
			}
		}
	}
	return nil
}

func validateValueCommand(field string, command Command) error {
	if !IsScriptCommand(command) {
		return fmt.Errorf("%s must be a shell string", field)
	}
	if _, ok, err := ValidateValueBuiltin(command); ok && err != nil {
		return err
	}
	return ValidateCommand(command)
}

func ValidateCommandsMap(name string, commands map[string]Command) error {
	for key, command := range commands {
		if err := validateIdentifierKey(name, key); err != nil {
			return err
		}
		if err := ValidateCommand(command); err != nil {
			return fmt.Errorf("%s.%s: %w", name, key, err)
		}
	}
	return nil
}

func ValidateVarsMap(name string, vars map[string]string) error {
	for key := range vars {
		if err := validateIdentifierKey(name, key); err != nil {
			return err
		}
	}
	return nil
}

func validateIdentifierKey(section, key string) error {
	if !identifierPattern.MatchString(key) {
		return fmt.Errorf("%s has invalid key %q", section, key)
	}
	return nil
}

func ValidateCommand(command Command) error {
	if len(command) == 0 {
		return errors.New("empty command")
	}
	if IsScriptCommand(command) {
		switch len(command) {
		case 2:
			if strings.TrimSpace(command[1]) == "" {
				return errors.New("empty script")
			}
		default:
			if len(command) < 4 {
				return errors.New("script command must have shell, body, and name")
			}
			if strings.TrimSpace(command[1]) == "" {
				return errors.New("empty shell")
			}
			if strings.TrimSpace(command[2]) == "" {
				return errors.New("empty script")
			}
		}
		return nil
	}
	if ref, ok := ParseRecipeReference(command); ok {
		if ref.Path == "" && ref.Name == "" {
			return errors.New("empty recipe reference")
		}
		if strings.TrimSpace(ref.Path) != ref.Path || strings.TrimSpace(ref.Name) != ref.Name ||
			(ref.Path != "" && ref.Name == "") {
			return fmt.Errorf("invalid recipe reference %q", ref.Target())
		}
		return nil
	}
	if strings.TrimSpace(command[0]) == "" {
		return errors.New("empty executable")
	}
	return nil
}

func Help(rec Recipe) string {
	if rec.Help != "" {
		return SingleLine(rec.Help)
	}
	return CommandSummary(rec)
}

func ArgumentHelp(arg Argument) string {
	if arg.Help != "" {
		return SingleLine(arg.Help)
	}
	if typ := argumentType(arg); typ != "" {
		return typ
	}
	return "string"
}

func CommandSummary(rec Recipe) string {
	text := CommandHelpText(rec.Cmd)
	var suffix []string
	if len(rec.Pre) > 0 {
		suffix = append(suffix, fmt.Sprintf("+%d pre", len(rec.Pre)))
	}
	if len(rec.ForEach) > 0 {
		suffix = append(suffix, "for_each")
	}
	if len(rec.Post) > 0 {
		suffix = append(suffix, fmt.Sprintf("+%d post", len(rec.Post)))
	}
	if len(suffix) > 0 {
		text += "  " + strings.Join(suffix, " ")
	}
	return text
}

func CommandHelpText(command Command) string {
	if IsScriptCommand(command) {
		if len(command) >= 2 && IsRecipeReferenceString(command[1]) {
			return strings.Join(command[1:], " ")
		}
		if body := strings.TrimSpace(ScriptBody(command)); body != "" && !strings.ContainsAny(body, "\r\n") {
			return body
		}
		return "<script>"
	}
	var parts []string
	for _, arg := range command {
		if strings.ContainsAny(arg, "\r\n") {
			parts = append(parts, "<script>")
			continue
		}
		parts = append(parts, arg)
	}
	return strings.Join(parts, " ")
}

func RecipeReference(command Command) (string, []string, bool) {
	ref, ok := ParseRecipeReference(command)
	if !ok {
		return "", nil, false
	}
	return ref.Name, slices.Clone(ref.Args), true
}

// ParseRecipeReference parses a command whose first item starts with @.
func ParseRecipeReference(command Command) (RecipeReferenceTarget, bool) {
	if len(command) == 0 || !strings.HasPrefix(command[0], recipeReferencePrefix) {
		return RecipeReferenceTarget{}, false
	}
	target := strings.TrimPrefix(command[0], recipeReferencePrefix)
	name, args, ok := parseGroupedInvocation([]string{target})
	if ok {
		ref := splitRecipeReferenceTarget(name)
		ref.Args = slices.Concat(args, command[1:])
		return ref, true
	}
	ref := splitRecipeReferenceTarget(target)
	ref.Args = slices.Clone(command[1:])
	return ref, true
}

func splitRecipeReferenceTarget(target string) RecipeReferenceTarget {
	path, name, ok := strings.Cut(target, ":")
	if !ok {
		return RecipeReferenceTarget{Name: target}
	}
	return RecipeReferenceTarget{Path: path, Name: name}
}

func (ref RecipeReferenceTarget) Target() string {
	if ref.Path == "" {
		return ref.Name
	}
	return ref.Path + ":" + ref.Name
}

func SingleLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func Invocation(rest []string) (string, []string) {
	if len(rest) == 0 {
		return "", nil
	}
	name, args, ok := parseGroupedInvocation(rest)
	if ok {
		return name, args
	}
	return rest[0], slices.Clone(rest[1:])
}

func parseGroupedInvocation(rest []string) (string, []string, bool) {
	first := rest[0]
	open := strings.IndexByte(first, '[')
	if open < 1 {
		return "", nil, false
	}
	name := first[:open]
	text := strings.TrimSpace(strings.Join(rest, " "))
	if !strings.HasSuffix(text, "]") {
		return "", nil, false
	}
	content := text[open+1 : len(text)-1]
	if strings.TrimSpace(content) == "" {
		return name, nil, true
	}
	var args []string
	for part := range strings.SplitSeq(content, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			args = append(args, part)
		}
	}
	return name, args, true
}

func mergeArguments(base, override map[string]Argument) map[string]Argument {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]Argument{}
	}
	for name, arg := range override {
		out[name] = MergeArgument(out[name], arg)
	}
	return out
}

func MergeArgument(base, override Argument) Argument {
	out := base
	if override.Help != "" {
		out.Help = override.Help
	}
	if override.Type != "" {
		out.Type = override.Type
	}
	if override.PathKind != "" {
		out.PathKind = override.PathKind
	}
	if override.Position != 0 {
		out.Position = override.Position
	}
	if override.Required {
		out.Required = true
	}
	if override.Default != nil {
		out.Default = override.Default
	}
	if len(override.Values) > 0 {
		out.Values = slices.Clone(override.Values)
	}
	return out
}

func expandCommands(commands []Command, values map[string]string, variadicArgs []string) ([]Command, error) {
	out := make([]Command, len(commands))
	for i, command := range commands {
		expanded, err := expandCommand(command, values, variadicArgs)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out[i] = expanded
	}
	return out, nil
}

func expandCommand(command Command, values map[string]string, variadicArgs []string) (Command, error) {
	if IsScriptCommand(command) {
		body, err := expandScript(ScriptBody(command), values, variadicArgs)
		if err != nil {
			return nil, err
		}
		if len(command) >= 4 {
			return append(Command{scriptCommand, command[1], body}, command[3:]...), nil
		}
		return ScriptCommand(body), nil
	}
	expanded, err := expandStrings(command, values, variadicArgs)
	return Command(expanded), err
}

// ExpandForEachCommand expands for_each placeholders in command.
func ExpandForEachCommand(command Command, item ValueCandidate, index int) (Command, error) {
	return expandCommand(command, forEachPlaceholderValues(item, index), nil)
}

// ExpandForEachString expands for_each placeholders in value.
func ExpandForEachString(value string, item ValueCandidate, index int) (string, error) {
	expanded, err := expandStrings([]string{value}, forEachPlaceholderValues(item, index), nil)
	if err != nil {
		return "", err
	}
	return expanded[0], nil
}

func forEachPlaceholderValues(item ValueCandidate, index int) map[string]string {
	return map[string]string{
		ForEachItemPlaceholder:      item.Value,
		ForEachItemHelpPlaceholder:  item.Help,
		ForEachItemIndexPlaceholder: strconv.Itoa(index),
	}
}

func forEachPlaceholderSentinels() map[string]string {
	return map[string]string{
		ForEachItemPlaceholder:      "{" + ForEachItemPlaceholder + "}",
		ForEachItemHelpPlaceholder:  "{" + ForEachItemHelpPlaceholder + "}",
		ForEachItemIndexPlaceholder: "{" + ForEachItemIndexPlaceholder + "}",
	}
}

func expandStrings(items []string, values map[string]string, variadicArgs []string) ([]string, error) {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if !strings.Contains(item, "{") {
			out = append(out, item)
			continue
		}
		if item == variadicArgsPlaceholder {
			out = append(out, variadicArgs...)
			continue
		}
		if strings.Contains(item, variadicArgsPlaceholder) {
			return nil, fmt.Errorf("%s must be a whole argument item", variadicArgsPlaceholder)
		}
		expanded, err := expandPlaceholders(item, values)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded)
	}
	return out, nil
}

func recipeUsesVariadicArgs(rec Recipe) bool {
	return RecipeUsesVariadicArgs(rec)
}

// RecipeUsesVariadicArgs reports whether rec references the leftover CLI args placeholder.
func RecipeUsesVariadicArgs(rec Recipe) bool {
	return containsVariadicArgsPlaceholder(rec.Cmd)
}

func containsVariadicArgsPlaceholder(items []string) bool {
	return slices.ContainsFunc(items, func(item string) bool {
		return strings.Contains(item, variadicArgsPlaceholder)
	})
}

func expandPlaceholders(text string, values map[string]string) (string, error) {
	var missing string
	if !strings.Contains(text, "{") {
		return text, nil
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] != '{' || (i > 0 && text[i-1] == '$') {
			b.WriteByte(text[i])
			i++
			continue
		}
		end := strings.IndexByte(text[i:], '}')
		if end < 0 {
			b.WriteByte(text[i])
			i++
			continue
		}
		end += i
		match := text[i : end+1]
		if !placeholderPattern.MatchString(match) {
			b.WriteString(match)
			i = end + 1
			continue
		}
		name := text[i+1 : end]
		value, ok := values[name]
		if !ok {
			missing = name
			b.WriteString(match)
			i = end + 1
			continue
		}
		b.WriteString(value)
		i = end + 1
	}
	out := b.String()
	if missing != "" {
		return "", fmt.Errorf("missing value for {%s}", missing)
	}
	return out, nil
}

func expandScript(body string, values map[string]string, variadicArgs []string) (string, error) {
	expanded, err := expandPlaceholders(body, values)
	if err != nil {
		return "", err
	}
	if !strings.Contains(expanded, variadicArgsPlaceholder) {
		return expanded, nil
	}
	replacement := shellQuotedWords(variadicArgs)
	var out strings.Builder
	for i := 0; i < len(expanded); {
		start, end, ok := scriptVariadicPlaceholder(expanded, i)
		if !ok {
			out.WriteByte(expanded[i])
			i++
			continue
		}
		if start > i {
			out.WriteString(expanded[i:start])
		}
		out.WriteString(replacement)
		i = end
	}
	if strings.Contains(out.String(), variadicArgsPlaceholder) {
		return "", fmt.Errorf("%s must be a whole shell word in cmd", variadicArgsPlaceholder)
	}
	return out.String(), nil
}

func scriptVariadicPlaceholder(text string, offset int) (int, int, bool) {
	index := strings.Index(text[offset:], variadicArgsPlaceholder)
	if index < 0 {
		return 0, 0, false
	}
	start := offset + index
	end := start + len(variadicArgsPlaceholder)
	if start > 0 && end < len(text) && text[start-1] == '"' && text[end] == '"' {
		start--
		end++
	}
	if start > 0 && end < len(text) && text[start-1] == '\'' && text[end] == '\'' {
		start--
		end++
	}
	if !scriptWordBoundary(text, start-1) || !scriptWordBoundary(text, end) {
		return 0, 0, false
	}
	return start, end, true
}

func scriptWordBoundary(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	switch text[index] {
	case ' ', '\t', '\r', '\n', ';', '&', '|', '(', ')', '<', '>', '{', '}':
		return true
	default:
		return false
	}
}

func shellQuotedWords(words []string) string {
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		quoted = append(quoted, shellQuote(word))
	}
	return strings.Join(quoted, " ")
}

func CommandWithShell(command Command, shell, shellPrelude string) Command {
	shell = defaultShell(shell)
	if IsScriptCommand(command) {
		if len(command) >= 4 {
			return command
		}
		return Command{scriptCommand, shell, joinShell(shellPrelude, command[1]), scriptArg0}
	}
	if strings.TrimSpace(shellPrelude) == "" || !isShellScriptCommand(command) {
		return command
	}
	out := slices.Clone(command)
	out[2] = joinShell(shellPrelude, out[2])
	return out
}

func CommandWithRecipeReference(command Command, shell, shellPrelude string) Command {
	if IsScriptCommand(command) && IsRecipeReferenceString(command[1]) {
		return Command{command[1]}
	}
	return CommandWithShell(command, shell, shellPrelude)
}

func IsScriptCommand(command Command) bool {
	return len(command) > 0 && command[0] == scriptCommand
}

func ScriptCommand(script string) Command {
	return Command{scriptCommand, script}
}

func ScriptShell(command Command) string {
	if len(command) >= 4 && IsScriptCommand(command) {
		return command[1]
	}
	return ""
}

func ScriptBody(command Command) string {
	if len(command) >= 4 && IsScriptCommand(command) {
		return command[2]
	}
	if len(command) == 2 && IsScriptCommand(command) {
		return command[1]
	}
	return ""
}

func ShellCommand(command Command) Command {
	if len(command) >= 4 && IsScriptCommand(command) {
		return append(Command{command[1], "-c", command[2]}, command[3:]...)
	}
	if len(command) == 2 && IsScriptCommand(command) {
		return Command{defaultShell(""), "-c", command[1], scriptArg0}
	}
	return command
}

func isShellScriptCommand(command Command) bool {
	return len(command) >= 3 && (command[0] == "sh" || strings.HasSuffix(command[0], "/sh")) && command[1] == "-c"
}

func EvalVarCommands(ctx context.Context, dir string, env map[string]string, commands map[string]Command, shell, shellPrelude string) (map[string]string, error) {
	values := map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(commands)) {
		value, err := commandOutput(ctx, dir, env, CommandWithShell(commands[key], shell, shellPrelude))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		values[key] = strings.TrimSpace(value)
	}
	return values, nil
}

func CommandOutput(ctx context.Context, dir string, env map[string]string, command Command) (string, error) {
	return commandOutput(ctx, dir, env, command)
}

func commandOutput(ctx context.Context, dir string, env map[string]string, command Command) (string, error) {
	if err := ValidateCommand(command); err != nil {
		return "", err
	}
	command = CommandWithShell(command, "", "")
	command = ShellCommand(command)
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = mergedEnv(os.Environ(), env)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func mergedEnv(base []string, overlays ...map[string]string) []string {
	env := map[string]string{}
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	for _, overlay := range overlays {
		maps.Copy(env, overlay)
	}
	out := make([]string, 0, len(env))
	for _, key := range slices.Sorted(maps.Keys(env)) {
		out = append(out, key+"="+env[key])
	}
	return out
}

func joinShell(parts ...string) string {
	var nonempty []string
	for _, part := range parts {
		part = strings.TrimRight(part, "\r\n")
		if strings.TrimSpace(part) != "" {
			nonempty = append(nonempty, part)
		}
	}
	return strings.Join(nonempty, "\n")
}

func defaultShell(shell string) string {
	if strings.TrimSpace(shell) == "" {
		return "sh"
	}
	return shell
}

func decodeCommand(value any) (Command, error) {
	switch value := value.(type) {
	case string:
		return ScriptCommand(value), nil
	case []any, []string:
		return nil, errors.New("command arrays are no longer supported; use a shell string")
	default:
		return nil, fmt.Errorf("command must be a shell string, got %T", value)
	}
}

// IsRecipeReferenceString reports whether value is exactly an @recipe script string.
func IsRecipeReferenceString(value string) bool {
	if !strings.HasPrefix(value, recipeReferencePrefix) ||
		strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\r\n") {
		return false
	}
	target := strings.TrimPrefix(value, recipeReferencePrefix)
	if !strings.ContainsAny(value, " \t") {
		return true
	}
	open := strings.IndexByte(target, '[')
	return open > 0 &&
		!strings.ContainsAny(target[:open], " \t") &&
		strings.HasSuffix(target, "]")
}

func mergeStringMaps(base, override map[string]string) map[string]string {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, override)
	return out
}

func validateArgumentType(value string) error {
	switch argumentType(Argument{Type: value}) {
	case "string", "int", "float", "bool", "path", "rel_path":
		return nil
	default:
		return fmt.Errorf("unsupported type %q", value)
	}
}

func validateArgumentPathKind(name string, arg Argument) error {
	if arg.PathKind == "" {
		return nil
	}
	switch argumentType(arg) {
	case "path", "rel_path":
	default:
		return fmt.Errorf("%s: path_kind requires type path or rel_path", name)
	}
	switch arg.PathKind {
	case "any", "file", "dir", "executable":
		return nil
	default:
		return fmt.Errorf("%s: unsupported path_kind %q", name, arg.PathKind)
	}
}

func validateArgumentValue(name string, arg Argument, value string) error {
	switch argumentType(arg) {
	case "string", "path":
		return nil
	case "rel_path":
		if filepath.IsAbs(value) || strings.HasPrefix(value, "~") {
			return fmt.Errorf("%s: want relative path, got %q", name, value)
		}
	case "int":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("%s: want int, got %q", name, value)
		}
	case "float":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return fmt.Errorf("%s: want float, got %q", name, value)
		}
	case "bool":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s: want bool, got %q", name, value)
		}
	}
	return nil
}

func expandPathArgumentValue(arg Argument, value string) (string, error) {
	if argumentType(arg) != "path" || value != "~" && !strings.HasPrefix(value, "~/") {
		return value, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if value == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(value, "~/")), nil
}

func argumentType(arg Argument) string {
	if arg.Type == "" {
		return "string"
	}
	return arg.Type
}

func defaultValueString(value any) (string, error) {
	switch value := value.(type) {
	case string:
		return value, nil
	case bool:
		return strconv.FormatBool(value), nil
	case int:
		return strconv.FormatInt(int64(value), 10), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), nil
	case fmt.Stringer:
		return value.String(), nil
	default:
		return "", fmt.Errorf("unsupported value type %T", value)
	}
}

func cloneCommands(commands []Command) []Command {
	out := make([]Command, len(commands))
	for i, command := range commands {
		out[i] = slices.Clone(command)
	}
	return out
}
