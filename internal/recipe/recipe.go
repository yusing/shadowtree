package recipe

import (
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const GoProfile = "go"

var ReservedNames = map[string]bool{
	"run":        true,
	"recipes":    true,
	"init":       true,
	"config":     true,
	"completion": true,
	"help":       true,
	"version":    true,
	"__complete": true,
}

type Command []string

type Config struct {
	Profile string            `toml:"profile" yaml:"profile"`
	Env     map[string]string `toml:"env" yaml:"env"`
	SyncOut []string          `toml:"sync_out" yaml:"sync_out"`
	Recipes map[string]Recipe `toml:"recipes" yaml:"recipes"`
}

type Recipe struct {
	Help        string              `toml:"help" yaml:"help"`
	Arguments   map[string]Argument `toml:"arguments" yaml:"arguments"`
	Cmd         Command             `toml:"cmd" yaml:"cmd"`
	Args        []string            `toml:"args" yaml:"args"`
	DefaultArgs []string            `toml:"default_args" yaml:"default_args"`
	Pre         []Command           `toml:"pre" yaml:"pre"`
	Post        []Command           `toml:"post" yaml:"post"`
	Env         map[string]string   `toml:"env" yaml:"env"`
	SyncOut     []string            `toml:"sync_out" yaml:"sync_out"`
}

type Argument struct {
	Help     string `toml:"help" yaml:"help"`
	Type     string `toml:"type" yaml:"type"`
	Position int    `toml:"position" yaml:"position"`
	Required bool   `toml:"required" yaml:"required"`
	Default  any    `toml:"default" yaml:"default"`
}

type Resolved struct {
	Name       string
	Recipe     Recipe
	Main       Command
	SyncOut    []string
	GlobalEnv  map[string]string
	ConfigPath string
	Profile    string
}

var placeholderPattern = regexp.MustCompile(`\{[A-Za-z_][A-Za-z0-9_]*\}`)

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
		"tidy": {
			Help:    "Tidy Go module files.",
			Cmd:     Command{"go", "mod", "tidy"},
			SyncOut: []string{"go.mod", "go.sum"},
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

func Resolve(name string, rec Recipe, cliArgs, globalSyncOut []string, globalEnv map[string]string, configPath, profile string) (Resolved, error) {
	if len(rec.Cmd) == 0 {
		return Resolved{}, fmt.Errorf("recipe %q has no cmd", name)
	}
	values, selectedArgs, err := ResolveArguments(rec, cliArgs)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q args: %w", name, err)
	}
	cmd, err := expandCommand(rec.Cmd, values)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q cmd: %w", name, err)
	}
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
	post, err := expandCommands(rec.Post, values)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q post: %w", name, err)
	}
	syncOut, err := expandStrings(slices.Concat(globalSyncOut, rec.SyncOut), values)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q sync_out: %w", name, err)
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
		GlobalEnv:  maps.Clone(globalEnv),
		ConfigPath: configPath,
		Profile:    profile,
	}, nil
}

func ValidateConfig(cfg Config) error {
	for name, rec := range cfg.Recipes {
		if ReservedNames[name] {
			return fmt.Errorf("recipe name %q is reserved", name)
		}
		if err := ValidateArguments(rec.Arguments); err != nil {
			return fmt.Errorf("recipe %q arguments: %w", name, err)
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
			values[key] = value
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
		values[name] = token
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
	}
	return nil
}

func ValidateCommand(command Command) error {
	if len(command) == 0 {
		return errors.New("empty command")
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
	text := strings.Join(rest, " ")
	content := text[open+1:]
	content = strings.TrimSuffix(content, "]")
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
	if override.Position != 0 {
		out.Position = override.Position
	}
	if override.Required {
		out.Required = true
	}
	if override.Default != nil {
		out.Default = override.Default
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
	out := placeholderPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(match, "{"), "}")
		value, ok := values[name]
		if !ok {
			missing = name
			return match
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("missing value for {%s}", missing)
	}
	return out, nil
}

func validateArgumentType(value string) error {
	switch argumentType(Argument{Type: value}) {
	case "string", "int", "float", "bool":
		return nil
	default:
		return fmt.Errorf("unsupported type %q", value)
	}
}

func validateArgumentValue(name string, arg Argument, value string) error {
	switch argumentType(arg) {
	case "string":
		return nil
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
