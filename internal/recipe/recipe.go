package recipe

import (
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
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
	Help        string            `toml:"help" yaml:"help"`
	Cmd         Command           `toml:"cmd" yaml:"cmd"`
	Args        []string          `toml:"args" yaml:"args"`
	DefaultArgs []string          `toml:"default_args" yaml:"default_args"`
	Pre         []Command         `toml:"pre" yaml:"pre"`
	Post        []Command         `toml:"post" yaml:"post"`
	Env         map[string]string `toml:"env" yaml:"env"`
	SyncOut     []string          `toml:"sync_out" yaml:"sync_out"`
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
	if err := ValidateCommand(rec.Cmd); err != nil {
		return Resolved{}, fmt.Errorf("recipe %q cmd: %w", name, err)
	}
	for i, command := range rec.Pre {
		if err := ValidateCommand(command); err != nil {
			return Resolved{}, fmt.Errorf("recipe %q pre[%d]: %w", name, i, err)
		}
	}
	for i, command := range rec.Post {
		if err := ValidateCommand(command); err != nil {
			return Resolved{}, fmt.Errorf("recipe %q post[%d]: %w", name, i, err)
		}
	}
	selectedArgs := rec.DefaultArgs
	if len(cliArgs) > 0 {
		selectedArgs = cliArgs
	}
	main := make(Command, 0, len(rec.Cmd)+len(rec.Args)+len(selectedArgs))
	main = append(main, rec.Cmd...)
	main = append(main, rec.Args...)
	main = append(main, selectedArgs...)
	syncOut := slices.Concat(globalSyncOut, rec.SyncOut)
	return Resolved{
		Name:       name,
		Recipe:     rec,
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

func cloneCommands(commands []Command) []Command {
	out := make([]Command, len(commands))
	for i, command := range commands {
		out[i] = slices.Clone(command)
	}
	return out
}
