package recipe

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"go/version"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/shadowtree/internal/scriptref"
	"mvdan.cc/sh/v3/syntax"
)

const (
	GoProfile   = "go"
	NodeProfile = "node"
)

const (
	GoPackageValuesCommand     = "@go-packages"
	GoFmtTargetValuesCommand   = `@go-packages; @glob "*.go"`
	GoMainPackageValuesCommand = "@go-main-packages"
	GoModuleValuesCommand      = "@go-modules"
	GoRunCommandValuesCommand  = GoMainPackageValuesCommand + `; @glob "*.go"`
)

const scriptCommand = "__shadowtree_script__"
const scriptArg0 = "shadowtree"
const recipeReferencePrefix = "@"
const variadicArgsPlaceholder = "{@}"
const (
	ForEachItemPlaceholder      = "item"
	ForEachItemHelpPlaceholder  = "item_help"
	ForEachItemIndexPlaceholder = "item_index"
	RunIDPlaceholder            = "run_id"
	ProfileArgumentName         = "profile"
)

const (
	LogStagePre  = "pre"
	LogStageCmd  = "cmd"
	LogStagePost = "post"
)

const (
	varStateVisiting = 1
	varStateDone     = 2
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

var allLogStages = []string{LogStagePre, LogStageCmd, LogStagePost}

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
	Include      []string           `toml:"include"`
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
	Help         string                   `toml:"help"`
	Arguments    map[string]Argument      `toml:"arguments"`
	Profiles     map[string]RecipeProfile `toml:"profiles"`
	Vars         map[string]string        `toml:"vars"`
	Shell        string                   `toml:"shell"`
	ShellPrelude string                   `toml:"shell_prelude"`
	Sandboxed    *bool                    `toml:"sandboxed"`
	ForEach      Command                  `toml:"for_each"`
	Workdir      string                   `toml:"workdir"`
	Cmd          Command                  `toml:"cmd"`
	Pre          []Command                `toml:"pre"`
	Post         []Command                `toml:"post"`
	Env          map[string]string        `toml:"env"`
	SyncOut      []string                 `toml:"sync_out"`
	Log          string                   `toml:"log"`
	LogStages    []string                 `toml:"log_stages"`
	LogTee       *bool                    `toml:"log_tee"`
	varsExpanded bool
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

// RecipeProfile sets argument defaults selected with profile=<name>.
type RecipeProfile struct {
	Arguments map[string]any `toml:"arguments"`
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
	RunID      string
	LogPath    string
	LogStages  []string
	LogTee     bool
}

type ResolveOptions struct {
	RunID string
}

// RecipeReferenceTarget is a parsed @recipe or @path:recipe command target.
type RecipeReferenceTarget struct {
	Path string
	Name string
	Args []string
}

// PlaceholderMode controls how a Shadowtree placeholder is expanded.
type PlaceholderMode string

const (
	PlaceholderModeDefault PlaceholderMode = ""
	PlaceholderModeShell   PlaceholderMode = "shell"
	PlaceholderModeRaw     PlaceholderMode = "raw"
	PlaceholderModeDQ      PlaceholderMode = "dq"
)

// Placeholder is a parsed {name} or {name:mode} reference.
type Placeholder struct {
	Name  string
	Mode  PlaceholderMode
	Start int
	End   int
	Match string
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var (
	supportedProfiles = []string{GoProfile, NodeProfile}
)

type BuiltinOptions struct {
	Dir string
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
	moduleWide := func(rec Recipe) Recipe {
		rec.ForEach = ScriptCommand(GoModuleValuesCommand)
		rec.Workdir = "{" + ForEachItemPlaceholder + "}"
		return rec
	}
	defaultGoPackageArgument := Argument{
		Type:     "rel_path",
		Position: 1,
		Default:  "./...",
		Values:   ScriptCommand(GoPackageValuesCommand),
	}
	defaultGoMainPackageArgument := defaultGoPackageArgument
	defaultGoMainPackageArgument.Values = ScriptCommand(GoMainPackageValuesCommand)
	lint := moduleWide(Recipe{
		Help: "Run Go lint checks.",
		Cmd:  Command{"golangci-lint", "run", "{pkg}", "{@}"},
		Arguments: map[string]Argument{
			"pkg": defaultGoPackageArgument,
		},
	})
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		lint = moduleWide(Recipe{
			Help: "Run Go static checks with go vet.",
			Cmd:  Command{"go", "vet", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		})
	}
	recipes := map[string]Recipe{
		"check": {
			Help: "Run vet and tests.",
			Cmd:  ScriptCommand(`set -e; @vet {@}; @test {@}`),
		},
		"test": moduleWide(Recipe{
			Help: "Run Go tests.",
			Cmd:  Command{"go", "test", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		}),
		"test-race": moduleWide(Recipe{
			Help: "Run Go tests with the race detector.",
			Cmd:  Command{"go", "test", "-race", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		}),
		"vet": moduleWide(Recipe{
			Help: "Run go vet.",
			Cmd:  Command{"go", "vet", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		}),
		"lint": lint,
		"fmt": moduleWide(Recipe{
			Help: "Format Go source files.",
			Cmd:  Command{"go", "fmt", "{target}", "{@}"},
			Arguments: map[string]Argument{"target": {
				Type:     "rel_path",
				Position: 1,
				Default:  "./...",
				Values:   ScriptCommand(GoFmtTargetValuesCommand),
			}},
			Sandboxed: new(false),
		}),
		"build": moduleWide(Recipe{
			Help: "Build Go packages.",
			Cmd:  Command{"go", "build", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoMainPackageArgument,
			},
		}),
		"generate": moduleWide(Recipe{
			Help: "Run go generate.",
			Cmd:  Command{"go", "generate", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		}),
		"run": {
			Help: "Run a Go command.",
			Cmd:  Command{"go", "-C", "{cwd}", "run", "{command}", "{@}"},
			Arguments: map[string]Argument{
				"cwd": {
					Type:    "rel_path",
					Default: ".",
					Values:  ScriptCommand(GoModuleValuesCommand),
				},
				"command": {
					Type:     "rel_path",
					Position: 1,
					Required: true,
					Values:   ScriptCommand(GoRunCommandValuesCommand),
				},
			},
		},
		"tidy": moduleWide(Recipe{
			Help:      "Tidy Go module files.",
			Cmd:       Command{"go", "mod", "tidy"},
			Post:      []Command{ScriptCommand("if test -f go.work; then go work sync; fi")},
			Sandboxed: new(false),
		}),
	}
	if goVersionAfter(mostCommonGoDirectiveVersion(opts.Dir), "1.26") {
		recipes["fix"] = moduleWide(Recipe{
			Help: "Update Go source with go fix.",
			Cmd:  Command{"go", "fix", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
			Sandboxed: new(false),
		})
	}
	return recipes
}

func mostCommonGoDirectiveVersion(dir string) string {
	if dir == "" {
		return "unknown"
	}
	modules, err := discoverGoModules(dir, "")
	if err != nil {
		return "unknown"
	}
	counts := map[string]int{}
	for _, module := range modules {
		modPath := filepath.Join(dir, filepath.FromSlash(module.Value), "go.mod")
		if modVersion := goDirectiveVersion(modPath); modVersion != "" {
			counts[modVersion]++
		}
	}
	best := "unknown"
	bestCount := 0
	for modVersion, count := range counts {
		if count > bestCount || count == bestCount && goVersionAfter(modVersion, best) {
			best = modVersion
			bestCount = count
		}
	}
	return best
}

func goDirectiveVersion(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		versionText, ok := strings.CutPrefix(scanner.Text(), "go ")
		if !ok {
			continue
		}
		fields := strings.Fields(versionText)
		if len(fields) > 0 {
			return fields[0]
		}
	}
	return ""
}

func goVersionAfter(current, minimum string) bool {
	current = "go" + current
	minimum = "go" + minimum
	if !version.IsValid(current) || !version.IsValid(minimum) {
		return false
	}
	return version.Compare(current, minimum) > 0
}

func MergeRecipes(base, overrides map[string]Recipe) (map[string]Recipe, error) {
	merged := maps.Clone(base)
	for name, override := range overrides {
		if IsReservedRecipeName(name) {
			return nil, fmt.Errorf("recipe name %q is reserved", name)
		}
		baseRecipe := merged[name]
		baseRecipe.ForEach = nil
		baseRecipe.Workdir = ""
		merged[name] = MergeRecipe(baseRecipe, override)
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
	if override.Profiles != nil {
		out.Profiles = mergeProfiles(out.Profiles, override.Profiles)
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
	if override.Log != "" {
		out.Log = override.Log
	}
	if override.LogStages != nil {
		out.LogStages = slices.Clone(override.LogStages)
	}
	if override.LogTee != nil {
		out.LogTee = new(*override.LogTee)
	}
	return out
}

// ApplyGlobalsExpanded applies globals after expanding static vars.
func ApplyGlobalsExpanded(recipes map[string]Recipe, vars, dynamicVars map[string]string, shell, shellPrelude string) (map[string]Recipe, error) {
	staticVars := maps.Clone(vars)
	for name := range dynamicVars {
		delete(staticVars, name)
	}
	expandedStaticVars, err := expandVarsWithBase(staticVars, runtimePlaceholderSentinels(dynamicVars))
	if err != nil {
		return nil, fmt.Errorf("vars: %w", err)
	}
	globalVars := mergeStringMaps(expandedStaticVars, dynamicVars)
	globalVarsForExpansion := runtimePlaceholderSentinels(globalVars)
	out := maps.Clone(recipes)
	for name, rec := range out {
		recipeVars, err := expandVarsWithBase(rec.Vars, globalVarsForExpansion)
		if err != nil {
			return nil, fmt.Errorf("recipe %q vars: %w", name, err)
		}
		rec.Vars = mergeStringMaps(globalVars, recipeVars)
		rec.varsExpanded = true
		if rec.Shell == "" {
			rec.Shell = shell
		}
		rec.ShellPrelude = JoinShell(shellPrelude, rec.ShellPrelude)
		out[name] = rec
	}
	return out, nil
}

func Resolve(name string, rec Recipe, cliArgs, globalSyncOut []string, globalEnv map[string]string, configPath, profile string) (Resolved, error) {
	return ResolveWithOptions(name, rec, cliArgs, globalSyncOut, globalEnv, configPath, profile, ResolveOptions{})
}

func ResolveWithOptions(name string, rec Recipe, cliArgs, globalSyncOut []string, globalEnv map[string]string, configPath, profile string, opts ResolveOptions) (Resolved, error) {
	if len(rec.Cmd) == 0 {
		return Resolved{}, fmt.Errorf("recipe %q has no cmd", name)
	}
	runID := opts.RunID
	if runID == "" {
		var err error
		runID, err = NewRunID()
		if err != nil {
			return Resolved{}, fmt.Errorf("recipe %q run_id: %w", name, err)
		}
	}
	runtimeValues := map[string]string{RunIDPlaceholder: runID}
	values, variadicArgs, err := resolveArguments(rec, cliArgs)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q args: %w", name, err)
	}
	vars := rec.Vars
	if !rec.varsExpanded {
		var err error
		vars, err = expandVarsWithBase(rec.Vars, runtimeValues)
		if err != nil {
			return Resolved{}, fmt.Errorf("recipe %q vars: %w", name, err)
		}
	} else {
		var err error
		vars, err = expandRuntimePlaceholdersInMap(vars, runtimeValues)
		if err != nil {
			return Resolved{}, fmt.Errorf("recipe %q vars: %w", name, err)
		}
	}
	values = mergeStringMaps(vars, values)
	values[RunIDPlaceholder] = runID
	commandValues := values
	if len(rec.ForEach) > 0 {
		commandValues = mergeStringMaps(values, forEachPlaceholderSentinels())
	}
	cmd, err := expandCommand(rec.Cmd, commandValues, variadicArgs, rec.Shell)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q cmd: %w", name, err)
	}
	pre, err := expandCommands(rec.Pre, values, nil, rec.Shell)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q pre: %w", name, err)
	}
	post, err := expandCommands(rec.Post, values, nil, rec.Shell)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q post: %w", name, err)
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
	logPath := rec.Log
	var logStages []string
	logTee := true
	if logPath != "" {
		expanded, err := expandStrings([]string{logPath}, values, nil)
		if err != nil {
			return Resolved{}, fmt.Errorf("recipe %q log: %w", name, err)
		}
		logPath = expanded[0]
		logStages = EffectiveLogStages(rec)
		if rec.LogTee != nil {
			logTee = *rec.LogTee
		}
	}
	var forEach Command
	if len(rec.ForEach) > 0 {
		forEach, err = expandCommand(rec.ForEach, values, nil, rec.Shell)
		if err != nil {
			return Resolved{}, fmt.Errorf("recipe %q for_each: %w", name, err)
		}
	}
	shellPrelude := rec.ShellPrelude
	if containsScriptPlaceholder(shellPrelude) && commandsUseShellPrelude(cmd, pre, post, forEach, rec.Shell, shellPrelude) {
		shellPrelude, err = expandScriptPlaceholders(shellPrelude, values, rec.Shell)
		if err != nil {
			return Resolved{}, fmt.Errorf("recipe %q shell_prelude: %w", name, err)
		}
	}
	cmd = CommandWithRecipeReference(cmd, rec.Shell, shellPrelude)
	for i := range pre {
		pre[i] = CommandWithRecipeReference(pre[i], rec.Shell, shellPrelude)
	}
	for i := range post {
		post[i] = CommandWithRecipeReference(post[i], rec.Shell, shellPrelude)
	}
	workdir := rec.Workdir
	if workdir != "" {
		expanded, err := expandStrings([]string{workdir}, commandValues, nil)
		if err != nil {
			return Resolved{}, fmt.Errorf("recipe %q workdir: %w", name, err)
		}
		workdir = expanded[0]
	}
	expandedGlobalEnv, err := expandStringMap(globalEnv, values)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q env: %w", name, err)
	}
	env, err := expandStringMap(rec.Env, values)
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q env: %w", name, err)
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
	resolvedRecipe.Vars = vars
	resolvedRecipe.ShellPrelude = shellPrelude
	resolvedRecipe.ForEach = forEach
	resolvedRecipe.Workdir = workdir
	resolvedRecipe.Pre = pre
	resolvedRecipe.Post = post
	resolvedRecipe.Env = env
	return Resolved{
		Name:       name,
		Recipe:     resolvedRecipe,
		Main:       cmd,
		SyncOut:    syncOut,
		Sandboxed:  sandboxed,
		GlobalEnv:  expandedGlobalEnv,
		ConfigPath: configPath,
		Profile:    profile,
		RunID:      runID,
		LogPath:    logPath,
		LogStages:  logStages,
		LogTee:     logTee,
	}, nil
}

func NewRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func commandsUseShellPrelude(cmd Command, pre, post []Command, forEach Command, shell, shellPrelude string) bool {
	if commandUsesShellPrelude(cmd, shell, shellPrelude) || forEachUsesShellPrelude(forEach, shell, shellPrelude) {
		return true
	}
	for _, command := range pre {
		if commandUsesShellPrelude(command, shell, shellPrelude) {
			return true
		}
	}
	for _, command := range post {
		if commandUsesShellPrelude(command, shell, shellPrelude) {
			return true
		}
	}
	return false
}

func forEachUsesShellPrelude(command Command, shell, shellPrelude string) bool {
	if len(command) == 0 {
		return false
	}
	if _, ok, _ := ValidateValueBuiltin(command); ok {
		return false
	}
	return commandUsesShellPrelude(command, shell, shellPrelude)
}

func commandUsesShellPrelude(command Command, shell, shellPrelude string) bool {
	_, usesPrelude := commandWithRecipeReference(command, shell, shellPrelude)
	return usesPrelude
}

// CommandWithRecipeReferenceExpandedPrelude expands placeholders in shellPrelude
// only when command will consume the prelude, then applies recipe-reference and
// shell wrapping.
func CommandWithRecipeReferenceExpandedPrelude(command Command, shell, shellPrelude string, values map[string]string) (Command, error) {
	if containsScriptPlaceholder(shellPrelude) && commandUsesShellPrelude(command, shell, shellPrelude) {
		expanded, err := expandScriptPlaceholders(shellPrelude, values, shell)
		if err != nil {
			return nil, err
		}
		shellPrelude = expanded
	}
	return CommandWithRecipeReference(command, shell, shellPrelude), nil
}

func RecipeSandboxed(rec Recipe) bool {
	return rec.Sandboxed == nil || *rec.Sandboxed
}

func ValidateConfig(cfg Config) error {
	if !scriptref.SupportedShell(cfg.Shell) {
		return fmt.Errorf("shell must be sh or bash, got %q", cfg.Shell)
	}
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
		if !scriptref.SupportedShell(rec.Shell) {
			return fmt.Errorf("recipe %q shell must be sh or bash, got %q", name, rec.Shell)
		}
		if err := ValidateArguments(rec.Arguments); err != nil {
			return fmt.Errorf("recipe %q arguments: %w", name, err)
		}
		if err := validateProfiles(rec.Arguments, rec.Profiles); err != nil {
			return fmt.Errorf("recipe %q profiles: %w", name, err)
		}
		if err := ValidateVarsMap(fmt.Sprintf("recipe %q vars", name), rec.Vars); err != nil {
			return err
		}
		if err := ValidateLogSettings(name, rec); err != nil {
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

func extractRecipeProfile(rec Recipe, cliArgs []string) (string, []string, error) {
	if len(rec.Profiles) == 0 || len(cliArgs) == 0 {
		return "", cliArgs, nil
	}
	profile := ""
	selectorIndex := -1
	passthrough := false
	for i, token := range cliArgs {
		if passthrough {
			continue
		}
		if token == "--" {
			passthrough = true
			continue
		}
		key, value, ok := strings.Cut(token, "=")
		if !ok || key != ProfileArgumentName {
			continue
		}
		if err := ValidateProfileSelection(rec, profile, value); err != nil {
			return "", nil, err
		}
		if selectorIndex < 0 {
			selectorIndex = i
		}
		profile = value
	}
	if selectorIndex < 0 {
		return "", cliArgs, nil
	}
	out := make([]string, 0, len(cliArgs)-1)
	passthrough = false
	for _, token := range cliArgs {
		if passthrough {
			out = append(out, token)
			continue
		}
		if token == "--" {
			passthrough = true
			out = append(out, token)
			continue
		}
		key, _, ok := strings.Cut(token, "=")
		if ok && key == ProfileArgumentName {
			continue
		}
		out = append(out, token)
	}
	return profile, out, nil
}

// ValidateProfileSelection validates a profile=<name> selector against a recipe.
func ValidateProfileSelection(rec Recipe, selectedProfile, value string) error {
	if selectedProfile != "" {
		return errors.New("profile specified multiple times")
	}
	if value == "" {
		return errors.New("profile must not be empty")
	}
	if _, ok := rec.Profiles[value]; !ok {
		return fmt.Errorf("unknown profile %q", value)
	}
	return nil
}

func resolveArguments(rec Recipe, cliArgs []string) (map[string]string, []string, error) {
	profileName, cliArgs, err := extractRecipeProfile(rec, cliArgs)
	if err != nil {
		return nil, nil, err
	}
	usesVariadicArgs := RecipeUsesVariadicArgs(rec)
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
			value, err := resolvedArgumentValueString(name, arg, arg.Default)
			if err != nil {
				return nil, nil, fmt.Errorf("%s default: %w", name, err)
			}
			values[name] = value
		}
	}
	if profileName != "" {
		profile := rec.Profiles[profileName]
		for name, raw := range profile.Arguments {
			arg := rec.Arguments[name]
			value, err := resolvedArgumentValueString(name, arg, raw)
			if err != nil {
				return nil, nil, fmt.Errorf("profile %q: %w", profileName, err)
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
		if name == RunIDPlaceholder {
			return fmt.Errorf("argument name %q is reserved", name)
		}
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
			_, err := argumentValueString(name, arg, arg.Default)
			if err != nil {
				return fmt.Errorf("%s default: %w", name, err)
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

func validateProfiles(args map[string]Argument, profiles map[string]RecipeProfile) error {
	if len(profiles) == 0 {
		return nil
	}
	if _, ok := args[ProfileArgumentName]; ok {
		return fmt.Errorf("argument name %q is reserved when profiles are configured", ProfileArgumentName)
	}
	for name, profile := range profiles {
		if err := validateIdentifierKey("profiles", name); err != nil {
			return err
		}
		if len(profile.Arguments) == 0 {
			continue
		}
		for argName, raw := range profile.Arguments {
			arg, ok := args[argName]
			if !ok {
				return fmt.Errorf("%s: unknown argument %q", name, argName)
			}
			if _, err := argumentValueString(argName, arg, raw); err != nil {
				return fmt.Errorf("%s: %w", name, err)
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
	if key == RunIDPlaceholder {
		return fmt.Errorf("%s key %q is reserved", section, key)
	}
	return nil
}

func ValidateLogSettings(name string, rec Recipe) error {
	if rec.Log == "" {
		if len(rec.LogStages) > 0 {
			return fmt.Errorf("recipe %q log_stages requires log", name)
		}
		if rec.LogTee != nil {
			return fmt.Errorf("recipe %q log_tee requires log", name)
		}
		return nil
	}
	if rec.LogStages != nil && len(rec.LogStages) == 0 {
		return fmt.Errorf("recipe %q log_stages must not be empty", name)
	}
	for _, stage := range rec.LogStages {
		if !ValidLogStage(stage) {
			return fmt.Errorf("recipe %q log_stages: unsupported stage %q", name, stage)
		}
	}
	return nil
}

func EffectiveLogStages(rec Recipe) []string {
	if len(rec.LogStages) == 0 {
		return slices.Clone(allLogStages)
	}
	return slices.Clone(rec.LogStages)
}

func ValidLogStage(stage string) bool {
	return slices.Contains(allLogStages, stage)
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
	if typ := ArgumentType(arg); typ != "" {
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

func mergeProfiles(base, override map[string]RecipeProfile) map[string]RecipeProfile {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]RecipeProfile{}
	}
	for name, profile := range override {
		out[name] = mergeRecipeProfile(out[name], profile)
	}
	return out
}

func mergeRecipeProfile(base, override RecipeProfile) RecipeProfile {
	out := base
	if override.Arguments != nil {
		out.Arguments = maps.Clone(base.Arguments)
		if out.Arguments == nil {
			out.Arguments = map[string]any{}
		}
		maps.Copy(out.Arguments, override.Arguments)
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

func expandCommands(commands []Command, values map[string]string, variadicArgs []string, shell string) ([]Command, error) {
	out := make([]Command, len(commands))
	for i, command := range commands {
		expanded, err := expandCommand(command, values, variadicArgs, shell)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out[i] = expanded
	}
	return out, nil
}

func expandCommand(command Command, values map[string]string, variadicArgs []string, shell string) (Command, error) {
	if IsScriptCommand(command) {
		body, err := expandScript(ScriptBody(command), values, variadicArgs, scriptExpansionShell(command, shell))
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
	return expandCommand(command, forEachPlaceholderValues(item, index), nil, ScriptShell(command))
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

func expandStringMap(items map[string]string, values map[string]string) (map[string]string, error) {
	if items == nil {
		return nil, nil
	}
	if !mapContainsPlaceholder(items) {
		return maps.Clone(items), nil
	}
	out := maps.Clone(items)
	for _, key := range slices.Sorted(maps.Keys(items)) {
		value := items[key]
		if !strings.Contains(value, "{") {
			continue
		}
		expanded, err := expandPlaceholders(value, values)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out[key] = expanded
	}
	return out, nil
}

func expandVars(vars map[string]string) (map[string]string, error) {
	return expandVarsWithBase(vars, nil)
}

func runtimePlaceholderSentinels(base map[string]string) map[string]string {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	out[RunIDPlaceholder] = "{" + RunIDPlaceholder + "}"
	return out
}

func expandRuntimePlaceholdersInMap(items map[string]string, runtimeValues map[string]string) (map[string]string, error) {
	if items == nil {
		return nil, nil
	}
	if !mapContainsPlaceholder(items) {
		return maps.Clone(items), nil
	}
	out := maps.Clone(items)
	for _, key := range slices.Sorted(maps.Keys(items)) {
		value := items[key]
		if !strings.Contains(value, "{") {
			continue
		}
		expanded, err := expandPlaceholderNames(value, func(placeholder Placeholder) (string, error) {
			if placeholder.Name == "@" {
				if placeholder.Mode == PlaceholderModeDefault {
					return placeholder.Match, nil
				}
				return "", fmt.Errorf("%s does not support placeholder modes", placeholder.Match)
			}
			if err := validateStringPlaceholderMode(placeholder); err != nil {
				return "", err
			}
			if value, ok := runtimeValues[placeholder.Name]; ok {
				return value, nil
			}
			return placeholder.Match, nil
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out[key] = expanded
	}
	return out, nil
}

func expandVarsWithBase(vars, base map[string]string) (map[string]string, error) {
	if len(vars) == 0 {
		return nil, nil
	}
	if !mapContainsPlaceholder(vars) {
		return vars, nil
	}
	out := maps.Clone(vars)
	state := make(map[string]uint8, len(vars))
	var expandName func(string) (string, error)
	expandName = func(name string) (string, error) {
		switch state[name] {
		case varStateVisiting:
			return "", fmt.Errorf("recursive reference to {%s}", name)
		case varStateDone:
			return out[name], nil
		}
		raw, ok := vars[name]
		if !ok {
			value, ok := base[name]
			if !ok {
				return "", fmt.Errorf("missing value for {%s}", name)
			}
			return value, nil
		}
		state[name] = varStateVisiting
		expanded, err := expandVarValue(raw, expandName)
		if err != nil {
			return "", err
		}
		state[name] = varStateDone
		out[name] = expanded
		return expanded, nil
	}
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if _, err := expandName(name); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return out, nil
}

func mapContainsPlaceholder(values map[string]string) bool {
	for _, value := range values {
		if strings.Contains(value, "{") {
			return true
		}
	}
	return false
}

func containsScriptPlaceholder(text string) bool {
	found := false
	_, _ = expandPlaceholderNames(text, func(placeholder Placeholder) (string, error) {
		if placeholder.Name != "@" || placeholder.Mode != PlaceholderModeDefault {
			found = true
		}
		return placeholder.Match, nil
	})
	return found
}

func expandVarValue(text string, value func(string) (string, error)) (string, error) {
	return expandPlaceholderNames(text, func(placeholder Placeholder) (string, error) {
		if err := validateStringPlaceholderMode(placeholder); err != nil {
			return "", err
		}
		if placeholder.Name == "@" {
			return placeholder.Match, nil
		}
		return value(placeholder.Name)
	})
}

func expandPlaceholderNames(text string, replace func(Placeholder) (string, error)) (string, error) {
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
		placeholder, ok := ParsePlaceholderAt(text, i)
		if !ok {
			b.WriteByte(text[i])
			i++
			continue
		}
		replacement, err := replace(placeholder)
		if err != nil {
			return "", err
		}
		b.WriteString(replacement)
		i = placeholder.End
	}
	return b.String(), nil
}

// ParsePlaceholderAt parses a Shadowtree placeholder starting at start.
func ParsePlaceholderAt(text string, start int) (Placeholder, bool) {
	if start < 0 || start >= len(text) || text[start] != '{' {
		return Placeholder{}, false
	}
	cursor := start + 1
	if cursor >= len(text) {
		return Placeholder{}, false
	}
	name := ""
	if text[cursor] == '@' {
		name = "@"
		cursor++
	} else {
		if !identifierStart(text[cursor]) {
			return Placeholder{}, false
		}
		nameStart := cursor
		cursor++
		for cursor < len(text) && identifierPart(text[cursor]) {
			cursor++
		}
		name = text[nameStart:cursor]
	}
	mode := PlaceholderModeDefault
	if cursor < len(text) && text[cursor] == ':' {
		modeStart := cursor + 1
		cursor = modeStart
		if cursor >= len(text) || !identifierStart(text[cursor]) {
			return Placeholder{}, false
		}
		cursor++
		for cursor < len(text) && identifierPart(text[cursor]) {
			cursor++
		}
		mode = PlaceholderMode(text[modeStart:cursor])
	}
	if cursor >= len(text) || text[cursor] != '}' {
		return Placeholder{}, false
	}
	end := cursor + 1
	return Placeholder{
		Name:  name,
		Mode:  mode,
		Start: start,
		End:   end,
		Match: text[start:end],
	}, true
}

// ValidPlaceholderMode reports whether mode is supported.
func ValidPlaceholderMode(mode PlaceholderMode) bool {
	switch mode {
	case PlaceholderModeDefault, PlaceholderModeShell, PlaceholderModeRaw, PlaceholderModeDQ:
		return true
	default:
		return false
	}
}

// ValidatePlaceholderMode reports whether placeholder may be used in the given context.
func ValidatePlaceholderMode(placeholder Placeholder, shell bool, quote byte) error {
	if placeholder.Name == "@" && placeholder.Mode != PlaceholderModeDefault {
		return fmt.Errorf("%s does not support placeholder modes", placeholder.Match)
	}
	if !ValidPlaceholderMode(placeholder.Mode) {
		return fmt.Errorf("%s uses unsupported placeholder mode %q", placeholder.Match, placeholder.Mode)
	}
	if placeholder.Mode == PlaceholderModeDefault || placeholder.Mode == PlaceholderModeRaw {
		return nil
	}
	if !shell {
		return fmt.Errorf("%s mode is supported only in shell commands", placeholder.Match)
	}
	switch placeholder.Mode {
	case PlaceholderModeShell:
		if quote != 0 {
			return fmt.Errorf("%s must not be inside quotes", placeholder.Match)
		}
	case PlaceholderModeDQ:
		if quote != '"' {
			return fmt.Errorf("%s must be inside double quotes", placeholder.Match)
		}
	}
	return nil
}

func validateStringPlaceholderMode(placeholder Placeholder) error {
	return ValidatePlaceholderMode(placeholder, false, 0)
}

func identifierStart(ch byte) bool {
	return ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch == '_'
}

func identifierPart(ch byte) bool {
	return identifierStart(ch) || ch >= '0' && ch <= '9'
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
	out, err := expandPlaceholderNames(text, func(placeholder Placeholder) (string, error) {
		if err := validateStringPlaceholderMode(placeholder); err != nil {
			return "", err
		}
		if placeholder.Name == "@" {
			return placeholder.Match, nil
		}
		value, ok := values[placeholder.Name]
		if !ok {
			missing = placeholder.Name
			return placeholder.Match, nil
		}
		return value, nil
	})
	if err != nil {
		return "", err
	}
	if missing != "" {
		return "", fmt.Errorf("missing value for {%s}", missing)
	}
	return out, nil
}

func scriptExpansionShell(command Command, shell string) string {
	if commandShell := ScriptShell(command); commandShell != "" {
		return commandShell
	}
	return shell
}

func expandScript(body string, values map[string]string, variadicArgs []string, shell string) (string, error) {
	expanded, err := expandScriptPlaceholders(body, values, shell)
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

func expandScriptPlaceholders(text string, values map[string]string, shell string) (string, error) {
	if !strings.Contains(text, "{") {
		return text, nil
	}
	contexts, err := scriptPlaceholderQuoteContexts(text, shell)
	if err != nil {
		return "", err
	}
	var missing string
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] == '{' && (i == 0 || text[i-1] != '$') {
			if placeholder, ok := ParsePlaceholderAt(text, i); ok {
				if placeholder.Name == "@" {
					if placeholder.Mode != PlaceholderModeDefault {
						return "", fmt.Errorf("%s does not support placeholder modes", placeholder.Match)
					}
					out.WriteString(placeholder.Match)
					i = placeholder.End
					continue
				}
				value, ok := values[placeholder.Name]
				if !ok {
					missing = placeholder.Name
					out.WriteString(placeholder.Match)
				} else {
					quote := contexts[i]
					if quote == 0 {
						quote = PlaceholderSurroundingQuote(text, i, placeholder.End-1)
					}
					replacement, err := scriptPlaceholderReplacement(placeholder, value, quote)
					if err != nil {
						return "", err
					}
					out.WriteString(replacement)
				}
				i = placeholder.End
				continue
			}
		}
		out.WriteByte(text[i])
		i++
	}
	if missing != "" {
		return "", fmt.Errorf("missing value for {%s}", missing)
	}
	return out.String(), nil
}

func scriptPlaceholderQuoteContexts(text, shell string) (map[int]byte, error) {
	contexts := map[int]byte{}
	if scriptref.SupportedShell(defaultShell(shell)) {
		parser, err := scriptref.Parser(shell)
		if err != nil {
			return nil, err
		}
		file, err := parser.Parse(strings.NewReader(text), "shadowtree")
		if err != nil {
			return nil, err
		}
		recordShellPlaceholderContexts(text, file, 0, contexts)
		return contexts, nil
	}
	recordSimplePlaceholderContexts(text, contexts)
	return contexts, nil
}

// ScriptPlaceholderQuoteContexts reports quote context for script placeholders.
func ScriptPlaceholderQuoteContexts(text, shell string) (map[int]byte, error) {
	return scriptPlaceholderQuoteContexts(text, shell)
}

// PlaceholderSurroundingQuote reports a quote that directly wraps one placeholder.
func PlaceholderSurroundingQuote(text string, start, end int) byte {
	if start == 0 || end+1 >= len(text) {
		return 0
	}
	quote := text[start-1]
	if quote != '\'' && quote != '"' || text[end+1] != quote {
		return 0
	}
	return quote
}

func recordShellPlaceholderContexts(text string, node syntax.Node, quote byte, contexts map[int]byte) {
	syntax.Walk(node, func(node syntax.Node) bool {
		switch node := node.(type) {
		case nil:
			return true
		case *syntax.CmdSubst:
			for _, stmt := range node.Stmts {
				recordShellPlaceholderContexts(text, stmt, 0, contexts)
			}
			return false
		case *syntax.SglQuoted:
			recordPlaceholderContexts(text, int(node.Pos().Offset())+1, int(node.End().Offset())-1, '\'', contexts)
			return false
		case *syntax.Lit:
			recordPlaceholderContexts(text, int(node.Pos().Offset()), int(node.End().Offset()), quote, contexts)
			return false
		case *syntax.DblQuoted:
			for _, part := range node.Parts {
				recordShellPlaceholderContexts(text, part, '"', contexts)
			}
			return false
		default:
			return true
		}
	})
}

func recordSimplePlaceholderContexts(text string, contexts map[int]byte) {
	quote := byte(0)
	for i := 0; i < len(text); i++ {
		if text[i] == '{' {
			recordPlaceholderContexts(text, i, len(text), quote, contexts)
		}
		switch quote {
		case 0:
			if text[i] == '\'' || text[i] == '"' {
				quote = text[i]
			}
		case '\'':
			if text[i] == '\'' {
				quote = 0
			}
		case '"':
			if text[i] == '"' {
				quote = 0
			}
		}
	}
}

func recordPlaceholderContexts(text string, start, end int, quote byte, contexts map[int]byte) {
	start = max(start, 0)
	end = min(end, len(text))
	for i := start; i < end; i++ {
		if text[i] != '{' || (i > 0 && text[i-1] == '$') {
			continue
		}
		close := strings.IndexByte(text[i:end], '}')
		if close < 0 {
			continue
		}
		close += i
		if _, ok := ParsePlaceholderAt(text, i); ok {
			contexts[i] = quote
		}
		i = close
	}
}

func scriptPlaceholderReplacement(placeholder Placeholder, value string, quote byte) (string, error) {
	if err := ValidatePlaceholderMode(placeholder, true, quote); err != nil {
		return "", err
	}
	switch placeholder.Mode {
	case PlaceholderModeDefault:
		switch quote {
		case '\'':
			return shellSingleQuoteEscaped(value), nil
		case '"':
			return shellDoubleQuoteEscaped(value), nil
		default:
			return value, nil
		}
	case PlaceholderModeRaw:
		return value, nil
	case PlaceholderModeShell:
		return shellQuote(value), nil
	case PlaceholderModeDQ:
		return shellDoubleQuoteEscaped(value), nil
	default:
		return "", fmt.Errorf("%s uses unsupported placeholder mode %q", placeholder.Match, placeholder.Mode)
	}
}

func shellSingleQuoteEscaped(value string) string {
	return strings.ReplaceAll(value, "'", "'\\''")
}

var shellDoubleQuoteReplacer = strings.NewReplacer(
	"\\", "\\\\",
	`"`, `\"`,
	`$`, `\$`,
	"`", "\\`",
)

func shellDoubleQuoteEscaped(value string) string {
	return shellDoubleQuoteReplacer.Replace(value)
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
	out, _ := commandWithShell(command, shell, shellPrelude)
	return out
}

func commandWithShell(command Command, shell, shellPrelude string) (Command, bool) {
	shell = defaultShell(shell)
	usesPrelude := strings.TrimSpace(shellPrelude) != ""
	if IsScriptCommand(command) {
		if len(command) >= 4 {
			return command, false
		}
		return Command{scriptCommand, shell, JoinShell(shellPrelude, command[1]), scriptArg0}, usesPrelude
	}
	if !usesPrelude || !isShellScriptCommand(command) {
		return command, false
	}
	out := slices.Clone(command)
	out[2] = JoinShell(shellPrelude, out[2])
	return out, true
}

func CommandWithRecipeReference(command Command, shell, shellPrelude string) Command {
	out, _ := commandWithRecipeReference(command, shell, shellPrelude)
	return out
}

func commandWithRecipeReference(command Command, shell, shellPrelude string) (Command, bool) {
	if IsScriptCommand(command) && IsRecipeReferenceString(command[1]) {
		return Command{command[1]}, false
	}
	return commandWithShell(command, shell, shellPrelude)
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

// JoinShell joins shell prelude parts using Shadowtree's runtime shell semantics.
func JoinShell(parts ...string) string {
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
	switch ArgumentType(Argument{Type: value}) {
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
	switch ArgumentType(arg) {
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
	switch ArgumentType(arg) {
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

func argumentValueString(name string, arg Argument, raw any) (string, error) {
	value, err := ScalarValueString(raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if err := validateArgumentValue(name, arg, value); err != nil {
		return "", err
	}
	return value, nil
}

func resolvedArgumentValueString(name string, arg Argument, raw any) (string, error) {
	value, err := argumentValueString(name, arg, raw)
	if err != nil {
		return "", err
	}
	value, err = expandPathArgumentValue(arg, value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return value, nil
}

func expandPathArgumentValue(arg Argument, value string) (string, error) {
	if ArgumentType(arg) != "path" || value != "~" && !strings.HasPrefix(value, "~/") {
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

// ArgumentType reports arg.Type with Shadowtree's default type applied.
func ArgumentType(arg Argument) string {
	if arg.Type == "" {
		return "string"
	}
	return arg.Type
}

// ScalarValueString converts a TOML scalar value to its argument string form.
func ScalarValueString(value any) (string, error) {
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
