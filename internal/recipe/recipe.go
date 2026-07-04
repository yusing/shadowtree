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

const GoProfile = "go"
const scriptCommand = "__shadowtree_script__"
const recipeReferencePrefix = "@"

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
	Cmd          Command             `toml:"cmd"`
	Args         []string            `toml:"args"`
	DefaultArgs  []string            `toml:"default_args"`
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
var goBuiltinNames = []string{"build", "check", "fmt", "generate", "lint", "run", "test", "test-race", "tidy", "vet"}

// BuiltinNames returns the built-in recipe names for profile.
func BuiltinNames(profile string) []string {
	if profile != GoProfile {
		return nil
	}
	return slices.Clone(goBuiltinNames)
}

func Builtins(profile string) map[string]Recipe {
	if profile != GoProfile {
		return map[string]Recipe{}
	}
	lint := Recipe{
		Help:        "Run Go lint checks.",
		Cmd:         Command{"golangci-lint", "run"},
		DefaultArgs: []string{"./..."},
	}
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		lint = Recipe{
			Help:        "Run Go static checks with go vet.",
			Cmd:         Command{"go", "vet"},
			DefaultArgs: []string{"./..."},
		}
	}
	return map[string]Recipe{
		"check": {
			Help:        "Run vet and tests.",
			Pre:         []Command{{"@vet"}},
			Cmd:         Command{"@test"},
			DefaultArgs: []string{"./..."},
		},
		"test": {
			Help:        "Run Go tests.",
			Cmd:         Command{"go", "test"},
			DefaultArgs: []string{"./..."},
		},
		"test-race": {
			Help:        "Run Go tests with the race detector.",
			Cmd:         Command{"go", "test"},
			Args:        []string{"-race"},
			DefaultArgs: []string{"./..."},
		},
		"vet": {
			Help:        "Run go vet.",
			Cmd:         Command{"go", "vet"},
			DefaultArgs: []string{"./..."},
		},
		"lint": lint,
		"fmt": {
			Help:        "Format Go source files.",
			Cmd:         Command{"gofmt", "-w"},
			DefaultArgs: []string{"."},
			Sandboxed:   new(false),
		},
		"build": {
			Help:        "Build Go packages.",
			Cmd:         Command{"go", "build"},
			DefaultArgs: []string{"./..."},
		},
		"generate": {
			Help:        "Run go generate.",
			Cmd:         Command{"go", "generate"},
			DefaultArgs: []string{"./..."},
		},
		"run": {
			Help:        "Run a Go command.",
			Cmd:         Command{"go", "run"},
			DefaultArgs: []string{"{command}"},
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
		if ReservedNames[name] {
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
	if len(override.Cmd) > 0 {
		out.Cmd = slices.Clone(override.Cmd)
	}
	if override.Args != nil {
		out.Args = slices.Clone(override.Args)
	}
	if override.DefaultArgs != nil {
		out.DefaultArgs = slices.Clone(override.DefaultArgs)
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
	values, selectedArgs, err := ResolveArguments(rec, cliArgs)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q args: %w", name, err)
	}
	values = mergeStringMaps(rec.Vars, values)
	cmd, err := expandCommand(rec.Cmd, values)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q cmd: %w", name, err)
	}
	cmd = CommandWithRecipeReference(cmd, rec.Shell, rec.ShellPrelude)
	fixedArgs, err := expandStrings(rec.Args, values)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q args: %w", name, err)
	}
	selectedArgs, err = expandStrings(selectedArgs, values)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q default_args: %w", name, err)
	}
	pre, err := expandCommands(rec.Pre, values)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q pre: %w", name, err)
	}
	for i := range pre {
		pre[i] = CommandWithRecipeReference(pre[i], rec.Shell, rec.ShellPrelude)
	}
	post, err := expandCommands(rec.Post, values)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q post: %w", name, err)
	}
	for i := range post {
		post[i] = CommandWithRecipeReference(post[i], rec.Shell, rec.ShellPrelude)
	}
	sandboxed := RecipeSandboxed(rec)
	var syncOut []string
	if sandboxed {
		syncOut, err = expandStrings(slices.Concat(globalSyncOut, rec.SyncOut), values)
		if err != nil {
			return Resolved{}, fmt.Errorf("recipe %q sync_out: %w", name, err)
		}
	}
	if err := ValidateCommand(cmd); err != nil {
		return Resolved{}, fmt.Errorf("recipe %q cmd: %w", name, err)
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
	main := make(Command, 0, len(cmd)+len(fixedArgs)+len(selectedArgs))
	main = append(main, cmd...)
	main = append(main, fixedArgs...)
	main = append(main, selectedArgs...)
	resolvedRecipe := rec
	resolvedRecipe.Pre = pre
	resolvedRecipe.Post = post
	return Resolved{
		Name:       name,
		Recipe:     resolvedRecipe,
		Main:       main,
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
		if ReservedNames[name] {
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
		for i, command := range rec.Pre {
			if err := ValidateCommand(command); err != nil {
				return fmt.Errorf("recipe %q pre[%d]: %w", name, i, err)
			}
		}
		for i, command := range rec.Post {
			if err := ValidateCommand(command); err != nil {
				return fmt.Errorf("recipe %q post[%d]: %w", name, i, err)
			}
		}
	}
	return nil
}

func ResolveArguments(rec Recipe, cliArgs []string) (map[string]string, []string, error) {
	if len(rec.Arguments) == 0 {
		selectedArgs := rec.DefaultArgs
		if len(cliArgs) > 0 {
			selectedArgs = cliArgs
		}
		return map[string]string{}, slices.Clone(selectedArgs), nil
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
	for _, token := range cliArgs {
		if token == "" {
			continue
		}
		if key, value, ok := strings.Cut(token, "="); ok {
			arg, exists := rec.Arguments[key]
			if !exists {
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
		if nextPositional >= len(positionals) {
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
	return values, slices.Clone(rec.DefaultArgs), nil
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
		if len(arg.Values) > 0 {
			if err := ValidateCommand(arg.Values); err != nil {
				return fmt.Errorf("%s values: %w", name, err)
			}
		}
	}
	return nil
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
		if len(command) != 2 {
			return errors.New("script command must have one body")
		}
		if strings.TrimSpace(command[1]) == "" {
			return errors.New("empty script")
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
	main := slices.Concat(rec.Cmd, rec.Args, rec.DefaultArgs)
	text := CommandHelpText(main)
	var suffix []string
	if len(rec.Pre) > 0 {
		suffix = append(suffix, fmt.Sprintf("+%d pre", len(rec.Pre)))
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

func expandCommands(commands []Command, values map[string]string) ([]Command, error) {
	out := make([]Command, len(commands))
	for i, command := range commands {
		expanded, err := expandCommand(command, values)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out[i] = expanded
	}
	return out, nil
}

func expandCommand(command Command, values map[string]string) (Command, error) {
	expanded, err := expandStrings(command, values)
	return Command(expanded), err
}

func expandStrings(items []string, values map[string]string) ([]string, error) {
	out := make([]string, len(items))
	for i, item := range items {
		expanded, err := expandPlaceholders(item, values)
		if err != nil {
			return nil, err
		}
		out[i] = expanded
	}
	return out, nil
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

func CommandWithShell(command Command, shell, shellPrelude string) Command {
	shell = defaultShell(shell)
	if IsScriptCommand(command) {
		return Command{shell, "-c", joinShell(shellPrelude, command[1]), "shadowtree"}
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
	case []any:
		out := make(Command, len(value))
		for i, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("command item %d must be string", i)
			}
			out[i] = text
		}
		return out, nil
	case []string:
		return Command(value), nil
	default:
		return nil, fmt.Errorf("command must be string or string array, got %T", value)
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
