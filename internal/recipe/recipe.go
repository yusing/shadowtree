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
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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

// RetryCommandHelper is the built-in @ command helper that retries another command.
const RetryCommandHelper = "retry"
const (
	ForEachItemPlaceholder      = "item"
	ForEachItemHelpPlaceholder  = "item_help"
	ForEachItemIndexPlaceholder = "item_index"
	RunIDPlaceholder            = "run_id"
	StatusPlaceholder           = "status"
	PresetArgumentName          = "preset"
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
	"recipes":          true,
	"init":             true,
	"config":           true,
	"exec":             true,
	"completion":       true,
	"help":             true,
	"version":          true,
	"__complete":       true,
	RetryCommandHelper: true,
}

var allLogStages = []string{LogStagePre, LogStageCmd, LogStagePost}
var (
	cmdStatusPlaceholderStages  = []string{LogStagePre}
	postStatusPlaceholderStages = []string{LogStagePre, LogStageCmd}
)

type Command []string

func (command *Command) UnmarshalTOML(value any) error {
	decoded, err := decodeCommand(value)
	if err != nil {
		return err
	}
	*command = decoded
	return nil
}

// StageCommand is a pre/post command plus optional execution controls.
type StageCommand struct {
	Cmd     Command `toml:"cmd"`
	Timeout string  `toml:"timeout"`
}

// IsStageCommandField reports whether name is a supported structured stage command field.
func IsStageCommandField(name string) bool {
	return name == "cmd" || name == "timeout"
}

func (stage *StageCommand) UnmarshalTOML(value any) error {
	decoded, err := decodeStageCommand(value)
	if err != nil {
		return err
	}
	*stage = decoded
	return nil
}

// StageCommands is the TOML representation of a pre or post command list.
type StageCommands []StageCommand

func (commands *StageCommands) UnmarshalTOML(value any) error {
	decoded, err := decodeStageCommands(value)
	if err != nil {
		return err
	}
	*commands = decoded
	return nil
}

type Config struct {
	Include      []string           `toml:"include"`
	Profile      string             `toml:"profile"`
	Env          map[string]string  `toml:"env"`
	Vars         map[string]string  `toml:"vars"`
	VarCommands  map[string]Command `toml:"var_commands"`
	EnumSets     map[string]Command `toml:"enum_sets"`
	Shell        string             `toml:"shell"`
	ShellPrelude string             `toml:"shell_prelude"`
	Recipes      map[string]Recipe  `toml:"recipes"`
}

type Recipe struct {
	Help         string                  `toml:"help"`
	Arguments    map[string]Argument     `toml:"arguments"`
	Presets      map[string]RecipePreset `toml:"presets"`
	Requires     Requirements            `toml:"requires"`
	Vars         map[string]string       `toml:"vars"`
	Shell        string                  `toml:"shell"`
	ShellPrelude string                  `toml:"shell_prelude"`
	Sandboxed    *bool                   `toml:"sandboxed"`
	ForEach      Command                 `toml:"for_each"`
	Workdir      string                  `toml:"workdir"`
	Cmd          Command                 `toml:"cmd"`
	Pre          StageCommands           `toml:"pre"`
	Post         StageCommands           `toml:"post"`
	Env          map[string]string       `toml:"env"`
	SyncOut      []string                `toml:"sync_out"`
	Log          string                  `toml:"log"`
	LogStages    []string                `toml:"log_stages"`
	LogTee       *bool                   `toml:"log_tee"`
	varsExpanded bool
	all          *allPlan
}

// Requirements declares external tools a recipe expects before commands run.
type Requirements struct {
	Commands         []string          `toml:"commands"`
	OptionalCommands []string          `toml:"optional_commands"`
	GoCommands       map[string]string `toml:"go_commands"`
	NodeCommands     map[string]string `toml:"node_commands"`
}

type Argument struct {
	Help     string  `toml:"help"`
	Type     string  `toml:"type"`
	PathKind string  `toml:"path_kind"`
	Position int     `toml:"position"`
	Required bool    `toml:"required"`
	Default  any     `toml:"default"`
	Min      any     `toml:"min"`
	Max      any     `toml:"max"`
	Values   Command `toml:"values"`
}

// RecipePreset sets argument defaults selected with preset=<name>.
type RecipePreset struct {
	Arguments map[string]any `toml:"arguments"`
}

type Resolved struct {
	Name         string
	Recipe       Recipe
	Main         Command
	Preset       string
	Arguments    map[string]string
	VariadicArgs []string
	SyncOut      []string
	Sandboxed    bool
	GlobalEnv    map[string]string
	ConfigPath   string
	Profile      string
	RunID        string
	LogPath      string
	LogStages    []string
	LogTee       bool
	Warnings     []string
	Scope        Scope
	TargetDomain string
	TargetSource TargetSource
}

type ResolveOptions struct {
	RunID        string
	Recipes      map[string]Recipe
	EnumSets     map[string]Command
	Scope        Scope
	TargetDomain string
	TargetSource TargetSource
}

type ConfigErrorTarget string

const (
	ConfigErrorTargetValue ConfigErrorTarget = "value"
	ConfigErrorTargetKey   ConfigErrorTarget = "key"
	ConfigErrorTargetTable ConfigErrorTarget = "table"
)

// ConfigPathError reports the TOML config path associated with a validation error.
type ConfigPathError struct {
	path   []string
	target ConfigErrorTarget
	err    error
}

func (err *ConfigPathError) Error() string {
	return err.err.Error()
}

func (err *ConfigPathError) Unwrap() error {
	return err.err
}

func (err *ConfigPathError) ConfigPath() []string {
	return slices.Clone(err.path)
}

func (err *ConfigPathError) Target() ConfigErrorTarget {
	if err.target == "" {
		return ConfigErrorTargetValue
	}
	return err.target
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
var supportedProfiles = []string{GoProfile, NodeProfile}

type BuiltinOptions struct {
	Context context.Context
	Dir     string
}

func SupportsProfile(profile string) bool {
	return slices.Contains(supportedProfiles, profile)
}

// IsCommandHelperName reports whether name identifies a built-in @ command helper.
func IsCommandHelperName(name string) bool {
	return name == RetryCommandHelper
}

func Builtins(profile string, opts BuiltinOptions) map[string]Recipe {
	if profile != GoProfile {
		if profile == NodeProfile {
			return nodeBuiltins(opts)
		}
		return map[string]Recipe{}
	}
	defaultGoPackageArgument := Argument{
		Type:     "rel_path",
		Position: 1,
		Default:  "./...",
		Values:   ScriptCommand(GoPackageValuesCommand),
	}
	defaultGoMainPackageArgument := defaultGoPackageArgument
	defaultGoMainPackageArgument.Values = ScriptCommand(GoMainPackageValuesCommand)
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
	recipes := map[string]Recipe{
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
			Cmd:  Command{"go", "fmt", "{target}", "{@}"},
			Arguments: map[string]Argument{"target": {
				Type:     "rel_path",
				Position: 1,
				Default:  "./...",
				Values:   ScriptCommand(GoFmtTargetValuesCommand),
			}},
			Sandboxed: new(false),
		},
		"build": {
			Help: "Build Go packages.",
			Cmd:  Command{"go", "build", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoMainPackageArgument,
			},
		},
		"generate": {
			Help: "Run go generate.",
			Cmd:  Command{"go", "generate", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
		},
		"install": {
			Help: "Install Go package.",
			Cmd:  Command{"go", "install", "-ldflags={ldflags}", "{pkg}"},
			Arguments: map[string]Argument{
				"ldflags": {Default: "-s -w"},
				"pkg":     defaultGoMainPackageArgument,
			},
		},
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
		"tidy": {
			Help:      "Tidy Go module files.",
			Cmd:       Command{"go", "mod", "tidy"},
			Post:      StageCommands{{Cmd: ScriptCommand("if test -f go.work; then go work sync; fi")}},
			Sandboxed: new(false),
		},
	}
	if goVersionAfter(mostCommonGoDirectiveVersion(opts.Context, opts.Dir), "1.26") {
		recipes["fix"] = Recipe{
			Help: "Update Go source with go fix.",
			Cmd:  Command{"go", "fix", "{pkg}", "{@}"},
			Arguments: map[string]Argument{
				"pkg": defaultGoPackageArgument,
			},
			Sandboxed: new(false),
		}
	}
	for _, name := range []string{"check", "lint", "test", "test-race", "vet"} {
		rec := recipes[name]
		argument := "pkg"
		if name == "check" {
			argument = ""
		}
		all := rec
		if argument != "" {
			all = allTargetRecipe(rec, argument)
		}
		recipes[name] = withAllPlan(rec, "packages", GoPackageTargets, all)
	}
	for _, name := range []string{"fmt", "generate"} {
		rec := recipes[name]
		argument := "pkg"
		if name == "fmt" {
			argument = "target"
		}
		recipes[name] = withAllPlan(rec, "packages", GoPackageTargets, allTargetRecipe(rec, argument))
	}
	for _, name := range []string{"build", "install"} {
		rec := recipes[name]
		recipes[name] = withAllPlan(rec, "main packages", GoMainPackageTargets, allTargetRecipe(rec, "pkg"))
	}
	tidy := recipes["tidy"]
	recipes["tidy"] = withAllPlan(tidy, "modules", GoModuleTargets, tidy)
	if fix, ok := recipes["fix"]; ok {
		recipes["fix"] = withAllPlan(fix, "packages", GoPackageTargets, allTargetRecipe(fix, "pkg"))
	}
	recipes["run"] = withUnsupportedAll(recipes["run"], "running multiple main packages has no defined process policy")
	return recipes
}

func mostCommonGoDirectiveVersion(ctx context.Context, dir string) string {
	if dir == "" {
		return "unknown"
	}
	modules, err := discoverGoModules(ctx, dir, "")
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
		baseRecipe.all = nil
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
	if override.Presets != nil {
		out.Presets = mergePresets(out.Presets, override.Presets)
	}
	if !override.Requires.Empty() {
		out.Requires = cloneRequirements(override.Requires)
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
		out.Pre = cloneStageCommands(override.Pre)
	}
	if override.Post != nil {
		out.Post = cloneStageCommands(override.Post)
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

// Empty reports whether req declares no requirements.
func (req Requirements) Empty() bool {
	return len(req.Commands) == 0 &&
		len(req.OptionalCommands) == 0 &&
		len(req.GoCommands) == 0 &&
		len(req.NodeCommands) == 0
}

func cloneRequirements(req Requirements) Requirements {
	return Requirements{
		Commands:         slices.Clone(req.Commands),
		OptionalCommands: slices.Clone(req.OptionalCommands),
		GoCommands:       maps.Clone(req.GoCommands),
		NodeCommands:     maps.Clone(req.NodeCommands),
	}
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
	if opts.Scope == ScopeAll {
		if err := validateAllCLIArgs(rec, cliArgs); err != nil {
			return Resolved{}, fmt.Errorf("recipe %q args: %w", name, err)
		}
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
	presetName, argValues, variadicArgs, warnings, err := resolveArgumentsWithPreset(rec, cliArgs, ValueBuiltinOptions{Recipes: opts.Recipes, EnumSets: opts.EnumSets})
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
	values := mergeStringMaps(vars, argValues)
	values[RunIDPlaceholder] = runID
	commandValues := values
	if len(rec.ForEach) > 0 || opts.TargetSource != "" {
		commandValues = mergeStringMaps(values, forEachPlaceholderSentinels())
	}
	cmd, err := expandCommandWithOptions(rec.Cmd, commandValues, variadicArgs, rec.Shell, placeholderExpansionOptions{commandStage: LogStageCmd})
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q cmd: %w", name, err)
	}
	pre, err := expandStageCommands(rec.Pre, values, nil, rec.Shell, "")
	if err != nil {
		return Resolved{}, fmt.Errorf("recipe %q pre: %w", name, err)
	}
	post, err := expandStageCommands(rec.Post, values, nil, rec.Shell, LogStagePost)
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
		pre[i].Cmd = CommandWithRecipeReference(pre[i].Cmd, rec.Shell, shellPrelude)
	}
	for i := range post {
		post[i].Cmd = CommandWithRecipeReference(post[i].Cmd, rec.Shell, shellPrelude)
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
		if err := ValidateStageCommand(command); err != nil {
			return Resolved{}, fmt.Errorf("recipe %q pre[%d]: %w", name, i, err)
		}
	}
	for i, command := range post {
		if err := ValidateStageCommand(command); err != nil {
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
		Name:         name,
		Recipe:       resolvedRecipe,
		Main:         cmd,
		Preset:       presetName,
		Arguments:    argValues,
		VariadicArgs: variadicArgs,
		SyncOut:      syncOut,
		Sandboxed:    sandboxed,
		GlobalEnv:    expandedGlobalEnv,
		ConfigPath:   configPath,
		Profile:      profile,
		RunID:        runID,
		LogPath:      logPath,
		LogStages:    logStages,
		LogTee:       logTee,
		Warnings:     warnings,
		Scope:        opts.Scope,
		TargetDomain: opts.TargetDomain,
		TargetSource: opts.TargetSource,
	}, nil
}

func NewRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func commandsUseShellPrelude(cmd Command, pre, post []StageCommand, forEach Command, shell, shellPrelude string) bool {
	if commandUsesShellPrelude(cmd, shell, shellPrelude) || forEachUsesShellPrelude(forEach, shell, shellPrelude) {
		return true
	}
	for _, stage := range pre {
		if commandUsesShellPrelude(stage.Cmd, shell, shellPrelude) {
			return true
		}
	}
	for _, stage := range post {
		if commandUsesShellPrelude(stage.Cmd, shell, shellPrelude) {
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
	if cfg.Profile != "" && !SupportsProfile(cfg.Profile) {
		return configValuePathError(fmt.Errorf("unsupported profile: %s", cfg.Profile), "profile")
	}
	if !scriptref.SupportedShell(cfg.Shell) {
		return configValuePathError(fmt.Errorf("shell must be sh or bash, got %q", cfg.Shell), "shell")
	}
	if err := validateVarsMapAt("vars", []string{"vars"}, cfg.Vars); err != nil {
		return err
	}
	if err := validateCommandsMapAt("var_commands", []string{"var_commands"}, cfg.VarCommands); err != nil {
		return err
	}
	for name, command := range cfg.EnumSets {
		if err := validateIdentifierKey("enum_sets", name); err != nil {
			return configKeyPathError(err, "enum_sets", name)
		}
		if err := validateEnumSet(command); err != nil {
			return configValuePathError(fmt.Errorf("enum set %q: %w", name, err), "enum_sets", name)
		}
	}
	for name, rec := range cfg.Recipes {
		if IsReservedRecipeName(name) {
			return configTablePathError(fmt.Errorf("recipe name %q is reserved", name), "recipes", name)
		}
		if !scriptref.SupportedShell(rec.Shell) {
			return configValuePathError(fmt.Errorf("recipe %q shell must be sh or bash, got %q", name, rec.Shell), "recipes", name, "shell")
		}
		if err := ValidateArguments(rec.Arguments); err != nil {
			return prefixConfigPath(fmt.Errorf("recipe %q arguments: %w", name, err), err, "recipes", name)
		}
		if err := validatePresets(rec.Arguments, rec.Presets); err != nil {
			wrapped := fmt.Errorf("recipe %q presets: %w", name, err)
			if pathErr, ok := errors.AsType[*ConfigPathError](err); ok && len(pathErr.path) > 0 && pathErr.path[0] == "arguments" {
				return prefixConfigPath(wrapped, err, "recipes", name)
			}
			return prefixConfigPath(wrapped, err, "recipes", name, "presets")
		}
		if err := validateRequirements(rec.Requires); err != nil {
			return prefixConfigPath(fmt.Errorf("recipe %q requires: %w", name, err), err, "recipes", name, "requires")
		}
		if err := validateVarsMapAt(fmt.Sprintf("recipe %q vars", name), []string{"recipes", name, "vars"}, rec.Vars); err != nil {
			return err
		}
		if err := ValidateLogSettings(name, rec); err != nil {
			return err
		}
		if len(rec.Cmd) > 0 {
			if err := ValidateCommand(rec.Cmd); err != nil {
				return configValuePathError(fmt.Errorf("recipe %q cmd: %w", name, err), "recipes", name, "cmd")
			}
		}
		if len(rec.ForEach) > 0 {
			if err := validateValueCommand("for_each", rec.ForEach); err != nil {
				return configValuePathError(fmt.Errorf("recipe %q for_each: %w", name, err), "recipes", name, "for_each")
			}
		}
		for i, command := range rec.Pre {
			if containsVariadicArgsPlaceholder(command.Cmd) {
				return configValuePathError(fmt.Errorf("recipe %q pre[%d]: %s is supported only in cmd", name, i, variadicArgsPlaceholder), "recipes", name, "pre", strconv.Itoa(i), "cmd")
			}
			if err := ValidateStageCommand(command); err != nil {
				return prefixConfigPath(fmt.Errorf("recipe %q pre[%d]: %w", name, i, err), err, "recipes", name, "pre", strconv.Itoa(i))
			}
		}
		for i, command := range rec.Post {
			if containsVariadicArgsPlaceholder(command.Cmd) {
				return configValuePathError(fmt.Errorf("recipe %q post[%d]: %s is supported only in cmd", name, i, variadicArgsPlaceholder), "recipes", name, "post", strconv.Itoa(i), "cmd")
			}
			if err := ValidateStageCommand(command); err != nil {
				return prefixConfigPath(fmt.Errorf("recipe %q post[%d]: %w", name, i, err), err, "recipes", name, "post", strconv.Itoa(i))
			}
		}
	}
	return nil
}

// ValidateEnumSetReferences validates named enum references after config includes merge.
func ValidateEnumSetReferences(cfg Config) error {
	for name, rec := range cfg.Recipes {
		for argName, arg := range rec.Arguments {
			if err := validateEnumSetReference(arg.Values, cfg.EnumSets); err != nil {
				return configValuePathError(fmt.Errorf("recipe %q argument %q values: %w", name, argName, err), "recipes", name, "arguments", argName, "values")
			}
		}
		if err := validateEnumSetReference(rec.ForEach, cfg.EnumSets); err != nil {
			return configValuePathError(fmt.Errorf("recipe %q for_each: %w", name, err), "recipes", name, "for_each")
		}
	}
	return nil
}

func ValidateResolvedRecipes(recipes map[string]Recipe) error {
	for name, rec := range recipes {
		if len(rec.Cmd) == 0 {
			return fmt.Errorf("recipe %q has no cmd", name)
		}
	}
	return nil
}

func validateRequirements(req Requirements) error {
	required := map[string]string{}
	if err := validateRequirementList("commands", req.Commands); err != nil {
		return err
	}
	for _, name := range req.Commands {
		required[name] = "commands"
	}
	if err := validateRequirementList("optional_commands", req.OptionalCommands); err != nil {
		return err
	}
	if err := validateInstallableRequirements("go_commands", req.GoCommands, required); err != nil {
		return err
	}
	if err := validateInstallableRequirements("node_commands", req.NodeCommands, required); err != nil {
		return err
	}
	for _, name := range req.OptionalCommands {
		if source, ok := required[name]; ok {
			return configValuePathError(fmt.Errorf("optional_commands overlaps required tool %q from %s", name, source), "optional_commands")
		}
	}
	return nil
}

func validateRequirementList(field string, names []string) error {
	seen := map[string]int{}
	for i, name := range names {
		if strings.TrimSpace(name) != name || name == "" {
			return configValuePathError(fmt.Errorf("%s[%d] must be a non-empty executable name without surrounding whitespace", field, i), field)
		}
		if strings.ContainsAny(name, `/\`) {
			return configValuePathError(fmt.Errorf("%s[%d] must be an executable name, not a path", field, i), field)
		}
		if first, ok := seen[name]; ok {
			return configValuePathError(fmt.Errorf("%s[%d] duplicates %s[%d] %q", field, i, field, first, name), field)
		}
		seen[name] = i
	}
	return nil
}

func validateInstallableRequirements(field string, commands map[string]string, required map[string]string) error {
	for name, pkg := range commands {
		if err := validateRequirementName(field, name); err != nil {
			return configKeyPathError(err, field, name)
		}
		if strings.TrimSpace(pkg) != pkg || pkg == "" {
			return configValuePathError(fmt.Errorf("%s.%s must be a non-empty package string without surrounding whitespace", field, name), field, name)
		}
		if source, ok := required[name]; ok {
			return configKeyPathError(fmt.Errorf("required tool %q declared in both %s and %s", name, source, field), field, name)
		}
		required[name] = field
	}
	return nil
}

func validateRequirementName(field, name string) error {
	if strings.TrimSpace(name) != name || name == "" {
		return fmt.Errorf("%s must be a non-empty executable name without surrounding whitespace", field)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%s must be an executable name, not a path", field)
	}
	if name == RunIDPlaceholder {
		return fmt.Errorf("%s key %q is reserved", field, name)
	}
	return nil
}

func extractRecipePreset(rec Recipe, cliArgs []string) (string, []string, error) {
	if len(rec.Presets) == 0 || len(cliArgs) == 0 {
		return "", cliArgs, nil
	}
	preset := ""
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
		if !ok || key != PresetArgumentName {
			continue
		}
		if err := ValidatePresetSelection(rec, preset, value); err != nil {
			return "", nil, err
		}
		if selectorIndex < 0 {
			selectorIndex = i
		}
		preset = value
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
		if ok && key == PresetArgumentName {
			continue
		}
		out = append(out, token)
	}
	return preset, out, nil
}

// ValidatePresetSelection validates a preset=<name> selector against a recipe.
func ValidatePresetSelection(rec Recipe, selectedPreset, value string) error {
	if selectedPreset != "" {
		return errors.New("preset specified multiple times")
	}
	if value == "" {
		return errors.New("preset must not be empty")
	}
	if _, ok := rec.Presets[value]; !ok {
		return fmt.Errorf("unknown preset %q", value)
	}
	return nil
}

func resolveArgumentsWithPreset(rec Recipe, cliArgs []string, valueOpts ValueBuiltinOptions) (string, map[string]string, []string, []string, error) {
	presetName, cliArgs, err := extractRecipePreset(rec, cliArgs)
	if err != nil {
		return "", nil, nil, nil, err
	}
	valueOpts.Recipe = rec
	usesVariadicArgs := RecipeUsesVariadicArgs(rec)
	if len(rec.Arguments) == 0 {
		if len(cliArgs) == 0 {
			return presetName, map[string]string{}, nil, nil, nil
		}
		if usesVariadicArgs {
			variadicArgs := cliArgs
			if cliArgs[0] == "--" {
				variadicArgs = cliArgs[1:]
			}
			return presetName, map[string]string{}, slices.Clone(variadicArgs), nil, nil
		}
		if key, _, ok := strings.Cut(cliArgs[0], "="); ok && !strings.HasPrefix(key, "-") {
			return "", nil, nil, nil, fmt.Errorf("unknown argument %q", key)
		}
		return "", nil, nil, nil, fmt.Errorf("unexpected positional argument %q", cliArgs[0])
	}
	values := map[string]string{}
	invalidDefaults := map[string]error{}
	for name, arg := range rec.Arguments {
		if arg.Default != nil {
			value, err := resolvedArgumentValueString(name, arg, arg.Default, valueOpts)
			if err != nil {
				invalidDefaults[name] = err
				continue
			}
			values[name] = value
		}
	}
	if presetName != "" {
		preset := rec.Presets[presetName]
		for name, raw := range preset.Arguments {
			arg := rec.Arguments[name]
			value, err := resolvedArgumentValueString(name, arg, raw, valueOpts)
			if err != nil {
				return "", nil, nil, nil, fmt.Errorf("preset %q: %w", presetName, err)
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
				return "", nil, nil, nil, fmt.Errorf("unknown argument %q", key)
			}
			expanded, err := resolvedArgumentValueString(key, arg, value, valueOpts)
			if err != nil {
				return "", nil, nil, nil, err
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
			return "", nil, nil, nil, fmt.Errorf("unexpected positional argument %q", token)
		}
		name := positionals[nextPositional]
		nextPositional++
		arg := rec.Arguments[name]
		expanded, err := resolvedArgumentValueString(name, arg, token, valueOpts)
		if err != nil {
			return "", nil, nil, nil, err
		}
		values[name] = expanded
	}
	for name, arg := range rec.Arguments {
		if arg.Required {
			if _, ok := values[name]; !ok {
				return "", nil, nil, nil, fmt.Errorf("missing required argument %q", name)
			}
		}
	}
	var warnings []string
	for _, name := range slices.Sorted(maps.Keys(invalidDefaults)) {
		err := invalidDefaults[name]
		if _, overridden := values[name]; !overridden {
			return "", nil, nil, nil, fmt.Errorf("%s default: %w", name, err)
		}
		warnings = append(warnings, fmt.Sprintf("%s default ignored: %v", name, err))
	}
	return presetName, values, variadicArgs, warnings, nil
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
			return configTablePathError(fmt.Errorf("argument name %q is reserved", name), "arguments", name)
		}
		if strings.TrimSpace(name) == "" {
			return configTablePathError(errors.New("empty argument name"), "arguments", name)
		}
		if strings.ContainsAny(name, "=()[], \t\r\n") {
			return configTablePathError(fmt.Errorf("invalid argument name %q", name), "arguments", name)
		}
		if err := validateArgumentType(arg.Type); err != nil {
			return configValuePathError(fmt.Errorf("%s: %w", name, err), "arguments", name, "type")
		}
		if err := validateArgumentPathKind(name, arg); err != nil {
			return configValuePathError(err, "arguments", name, "path_kind")
		}
		argRange, err := parseArgumentRange(name, arg)
		if err != nil {
			target := "min"
			if rangeErr, ok := errors.AsType[*argumentRangeError](err); ok {
				target = rangeErr.field
			}
			return configValuePathError(err, "arguments", name, target)
		}
		if arg.Position < 0 {
			return configValuePathError(fmt.Errorf("%s: position must be positive", name), "arguments", name, "position")
		}
		if arg.Position > 0 {
			if existing, ok := positions[arg.Position]; ok {
				return configValuePathError(fmt.Errorf("%s: position %d already used by %s", name, arg.Position, existing), "arguments", name, "position")
			}
			positions[arg.Position] = name
		}
		if arg.Values != nil {
			if err := validateValueCommand("values", arg.Values); err != nil {
				return configValuePathError(fmt.Errorf("%s values: %w", name, err), "arguments", name, "values")
			}
		}
		if arg.Default != nil {
			if _, err := argumentValueStringWithRange(name, arg, argRange, arg.Default); err != nil {
				return configValuePathError(fmt.Errorf("%s default: %w", name, err), "arguments", name, "default")
			}
		}
	}
	return nil
}

func validatePresets(args map[string]Argument, presets map[string]RecipePreset) error {
	if len(presets) == 0 {
		return nil
	}
	ranges := map[string]argumentRange{}
	for argName, arg := range args {
		argRange, err := parseArgumentRange(argName, arg)
		if err != nil {
			return err
		}
		ranges[argName] = argRange
	}
	if _, ok := args[PresetArgumentName]; ok {
		return configTablePathError(fmt.Errorf("argument name %q is reserved when presets are configured", PresetArgumentName), "arguments", PresetArgumentName)
	}
	for name, preset := range presets {
		if err := validateIdentifierKey("presets", name); err != nil {
			return err
		}
		if len(preset.Arguments) == 0 {
			continue
		}
		for argName, raw := range preset.Arguments {
			arg, ok := args[argName]
			if !ok {
				return configKeyPathError(fmt.Errorf("%s: unknown argument %q", name, argName), name, "arguments", argName)
			}
			if _, err := argumentValueStringWithRange(argName, arg, ranges[argName], raw); err != nil {
				return configValuePathError(fmt.Errorf("%s: %w", name, err), name, "arguments", argName)
			}
		}
	}
	return nil
}

func configValuePathError(err error, path ...string) error {
	return configPathError(err, ConfigErrorTargetValue, path...)
}

func configKeyPathError(err error, path ...string) error {
	return configPathError(err, ConfigErrorTargetKey, path...)
}

func configTablePathError(err error, path ...string) error {
	return configPathError(err, ConfigErrorTargetTable, path...)
}

func configPathError(err error, target ConfigErrorTarget, path ...string) error {
	if err == nil {
		return nil
	}
	return &ConfigPathError{path: slices.Clone(path), target: target, err: err}
}

func prefixConfigPath(err, source error, prefix ...string) error {
	if err == nil {
		return nil
	}
	if pathErr, ok := errors.AsType[*ConfigPathError](source); ok {
		path := make([]string, 0, len(prefix)+len(pathErr.path))
		path = append(path, prefix...)
		path = append(path, pathErr.path...)
		return &ConfigPathError{path: path, target: pathErr.Target(), err: err}
	}
	return configValuePathError(err, prefix...)
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

func validateCommandsMapAt(name string, pathPrefix []string, commands map[string]Command) error {
	for key, command := range commands {
		if err := validateIdentifierKey(name, key); err != nil {
			return configKeyPathError(err, appendConfigPath(pathPrefix, key)...)
		}
		if err := ValidateCommand(command); err != nil {
			return configValuePathError(fmt.Errorf("%s.%s: %w", name, key, err), appendConfigPath(pathPrefix, key)...)
		}
	}
	return nil
}

func validateVarsMapAt(name string, pathPrefix []string, vars map[string]string) error {
	for key := range vars {
		if err := validateIdentifierKey(name, key); err != nil {
			return configKeyPathError(err, appendConfigPath(pathPrefix, key)...)
		}
	}
	return nil
}

func appendConfigPath(path []string, item string) []string {
	out := make([]string, 0, len(path)+1)
	out = append(out, path...)
	return append(out, item)
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
			return configValuePathError(fmt.Errorf("recipe %q log_stages requires log", name), "recipes", name, "log_stages")
		}
		if rec.LogTee != nil {
			return configValuePathError(fmt.Errorf("recipe %q log_tee requires log", name), "recipes", name, "log_tee")
		}
		return nil
	}
	if rec.LogStages != nil && len(rec.LogStages) == 0 {
		return configValuePathError(fmt.Errorf("recipe %q log_stages must not be empty", name), "recipes", name, "log_stages")
	}
	for _, stage := range rec.LogStages {
		if !ValidLogStage(stage) {
			return configValuePathError(fmt.Errorf("recipe %q log_stages: unsupported stage %q", name, stage), "recipes", name, "log_stages")
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

// ValidateStageCommand validates a pre/post stage command.
func ValidateStageCommand(command StageCommand) error {
	if err := ValidateCommand(command.Cmd); err != nil {
		return err
	}
	if _, err := stageTimeout(command.Timeout); err != nil {
		return configValuePathError(err, "timeout")
	}
	return nil
}

// StageTimeout returns the resolved timeout for command, or zero when unset.
func StageTimeout(command StageCommand) time.Duration {
	timeout, _ := stageTimeout(command.Timeout)
	return timeout
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

// StageCommandHelpText returns a compact display form for command.
func StageCommandHelpText(command StageCommand) string {
	text := CommandHelpText(command.Cmd)
	if timeout := StageTimeout(command); timeout > 0 {
		text += " timeout=" + timeout.String()
	} else if timeout, err := stageTimeout(command.Timeout); err == nil && timeout > 0 {
		text += " timeout=" + timeout.String()
	}
	return text
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

func mergePresets(base, override map[string]RecipePreset) map[string]RecipePreset {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]RecipePreset{}
	}
	for name, preset := range override {
		out[name] = mergeRecipePreset(out[name], preset)
	}
	return out
}

func mergeRecipePreset(base, override RecipePreset) RecipePreset {
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
	if override.Min != nil {
		out.Min = override.Min
	}
	if override.Max != nil {
		out.Max = override.Max
	}
	if len(override.Values) > 0 {
		out.Values = slices.Clone(override.Values)
	}
	return out
}

func expandStageCommands(commands []StageCommand, values map[string]string, variadicArgs []string, shell, commandStage string) ([]StageCommand, error) {
	out := make([]StageCommand, len(commands))
	for i, command := range commands {
		expanded, err := expandStageCommand(command, values, variadicArgs, shell, commandStage)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out[i] = expanded
	}
	return out, nil
}

func expandStageCommand(stage StageCommand, values map[string]string, variadicArgs []string, shell, commandStage string) (StageCommand, error) {
	command, err := expandCommandWithOptions(stage.Cmd, values, variadicArgs, shell, placeholderExpansionOptions{commandStage: commandStage})
	if err != nil {
		return StageCommand{}, err
	}
	return StageCommand{Cmd: command, Timeout: stage.Timeout}, nil
}

func expandCommand(command Command, values map[string]string, variadicArgs []string, shell string) (Command, error) {
	return expandCommandWithOptions(command, values, variadicArgs, shell, placeholderExpansionOptions{})
}

type placeholderExpansionOptions struct {
	commandStage string
}

func expandCommandWithOptions(command Command, values map[string]string, variadicArgs []string, shell string, opts placeholderExpansionOptions) (Command, error) {
	if IsScriptCommand(command) {
		body, err := expandScriptWithOptions(ScriptBody(command), values, variadicArgs, scriptExpansionShell(command, shell), opts)
		if err != nil {
			return nil, err
		}
		if len(command) >= 4 {
			return append(Command{scriptCommand, command[1], body}, command[3:]...), nil
		}
		return ScriptCommand(body), nil
	}
	expanded, err := expandStringsWithOptions(command, values, variadicArgs, opts)
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

// CommandContainsStageStatusPlaceholder reports whether command contains a stage status placeholder.
func CommandContainsStageStatusPlaceholder(command Command) bool {
	if IsScriptCommand(command) {
		return strings.Contains(ScriptBody(command), "{"+StatusPlaceholder+":")
	}
	return slices.ContainsFunc(command, func(item string) bool {
		return strings.Contains(item, "{"+StatusPlaceholder+":")
	})
}

// ExpandStageStatusPlaceholders expands prior-stage status placeholders in a command.
func ExpandStageStatusPlaceholders(command Command, values map[string]string) (Command, error) {
	if !CommandContainsStageStatusPlaceholder(command) {
		return command, nil
	}
	if IsScriptCommand(command) {
		body, err := expandStageStatusString(ScriptBody(command), values)
		if err != nil {
			return nil, err
		}
		if len(command) >= 4 {
			return append(Command{scriptCommand, command[1], body}, command[3:]...), nil
		}
		return ScriptCommand(body), nil
	}
	out := make(Command, 0, len(command))
	for _, item := range command {
		expanded, err := expandStageStatusString(item, values)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded)
	}
	return out, nil
}

func expandStageStatusString(text string, values map[string]string) (string, error) {
	if !strings.Contains(text, "{"+StatusPlaceholder+":") {
		return text, nil
	}
	return expandPlaceholderNames(text, func(placeholder Placeholder) (string, error) {
		if placeholder.Name != StatusPlaceholder {
			return placeholder.Match, nil
		}
		if err := ValidateStageStatusPlaceholder(placeholder); err != nil {
			return "", err
		}
		return values[string(placeholder.Mode)], nil
	})
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
	return expandStringsWithOptions(items, values, variadicArgs, placeholderExpansionOptions{})
}

func expandStringsWithOptions(items []string, values map[string]string, variadicArgs []string, opts placeholderExpansionOptions) ([]string, error) {
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
		expanded, err := expandPlaceholdersWithOptions(item, values, opts)
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

// IsStageStatusPlaceholder reports whether placeholder references a prior stage status.
func IsStageStatusPlaceholder(placeholder Placeholder) bool {
	return placeholder.Name == StatusPlaceholder &&
		(placeholder.Mode == PlaceholderMode(LogStagePre) || placeholder.Mode == PlaceholderMode(LogStageCmd))
}

// ValidateStageStatusPlaceholder reports whether placeholder is a supported stage status placeholder.
func ValidateStageStatusPlaceholder(placeholder Placeholder) error {
	if placeholder.Name != StatusPlaceholder {
		return nil
	}
	switch placeholder.Mode {
	case PlaceholderMode(LogStagePre), PlaceholderMode(LogStageCmd):
		return nil
	case PlaceholderModeDefault:
		return fmt.Errorf("{%s} is not a stage status placeholder; use {%s:%s} or {%s:%s}", StatusPlaceholder, StatusPlaceholder, LogStagePre, StatusPlaceholder, LogStageCmd)
	default:
		return fmt.Errorf("%s supports only %s or %s", placeholder.Match, LogStagePre, LogStageCmd)
	}
}

// StatusPlaceholderStages reports which status placeholders are available in commandStage.
func StatusPlaceholderStages(commandStage string) []string {
	switch commandStage {
	case LogStageCmd:
		return cmdStatusPlaceholderStages
	case LogStagePost:
		return postStatusPlaceholderStages
	default:
		return nil
	}
}

// StageAllowsStatusPlaceholder reports whether placeholder is available in commandStage.
func StageAllowsStatusPlaceholder(commandStage string, placeholder Placeholder) bool {
	return slices.Contains(StatusPlaceholderStages(commandStage), string(placeholder.Mode))
}

// StatusPlaceholderContextError reports where a status placeholder may be used.
func StatusPlaceholderContextError(placeholder Placeholder) error {
	switch string(placeholder.Mode) {
	case LogStagePre:
		return fmt.Errorf("%s is supported only in cmd or post", placeholder.Match)
	case LogStageCmd:
		return fmt.Errorf("%s is supported only in post", placeholder.Match)
	default:
		return fmt.Errorf("%s is not supported here", placeholder.Match)
	}
}

func (opts placeholderExpansionOptions) allowsStageStatus(placeholder Placeholder) bool {
	return StageAllowsStatusPlaceholder(opts.commandStage, placeholder)
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
	return expandPlaceholdersWithOptions(text, values, placeholderExpansionOptions{})
}

func expandPlaceholdersWithOptions(text string, values map[string]string, opts placeholderExpansionOptions) (string, error) {
	var missing string
	out, err := expandPlaceholderNames(text, func(placeholder Placeholder) (string, error) {
		if placeholder.Name == StatusPlaceholder && placeholder.Mode != PlaceholderModeDefault {
			if err := ValidateStageStatusPlaceholder(placeholder); err != nil {
				return "", err
			}
			if !opts.allowsStageStatus(placeholder) {
				return "", StatusPlaceholderContextError(placeholder)
			}
			return placeholder.Match, nil
		}
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

func expandScriptWithOptions(body string, values map[string]string, variadicArgs []string, shell string, opts placeholderExpansionOptions) (string, error) {
	expanded, err := expandScriptPlaceholdersWithOptions(body, values, shell, opts)
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
	return expandScriptPlaceholdersWithOptions(text, values, shell, placeholderExpansionOptions{})
}

func expandScriptPlaceholdersWithOptions(text string, values map[string]string, shell string, opts placeholderExpansionOptions) (string, error) {
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
				if placeholder.Name == StatusPlaceholder && placeholder.Mode != PlaceholderModeDefault {
					if err := ValidateStageStatusPlaceholder(placeholder); err != nil {
						return "", err
					}
					if !opts.allowsStageStatus(placeholder) {
						return "", StatusPlaceholderContextError(placeholder)
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
	default:
		return nil, fmt.Errorf("command must be a shell string, got %s", tomlValueKind(value))
	}
}

func decodeStageCommands(value any) ([]StageCommand, error) {
	switch value := value.(type) {
	case string:
		stage, err := decodeStageCommand(value)
		if err != nil {
			return nil, err
		}
		return []StageCommand{stage}, nil
	case map[string]any:
		stage, err := decodeStageCommand(value)
		if err != nil {
			return nil, err
		}
		return []StageCommand{stage}, nil
	case []any:
		out := make([]StageCommand, 0, len(value))
		for i, item := range value {
			stage, err := decodeStageCommand(item)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out = append(out, stage)
		}
		return out, nil
	case []string:
		out := make([]StageCommand, 0, len(value))
		for _, item := range value {
			out = append(out, StageCommand{Cmd: ScriptCommand(item)})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("stage commands must be a shell string, table, or array, got %s", tomlValueKind(value))
	}
}

func decodeStageCommand(value any) (StageCommand, error) {
	switch value := value.(type) {
	case string:
		return StageCommand{Cmd: ScriptCommand(value)}, nil
	case map[string]any:
		rawCommand, ok := value["cmd"]
		if !ok {
			return StageCommand{}, errors.New("stage command requires cmd")
		}
		command, err := decodeCommand(rawCommand)
		if err != nil {
			return StageCommand{}, err
		}
		stage := StageCommand{Cmd: command}
		if rawTimeout, ok := value["timeout"]; ok {
			timeout, ok := rawTimeout.(string)
			if !ok {
				return StageCommand{}, fmt.Errorf("timeout must be a duration string, got %s", tomlValueKind(rawTimeout))
			}
			stage.Timeout = timeout
		}
		return stage, nil
	default:
		command, err := decodeCommand(value)
		if err != nil {
			return StageCommand{}, err
		}
		return StageCommand{Cmd: command}, nil
	}
}

func stageTimeout(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("timeout: %w", err)
	}
	if timeout <= 0 {
		return 0, errors.New("timeout must be greater than zero")
	}
	return timeout, nil
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
	case "string", "int", "float", "bool", "path", "rel_path", "duration", "duration:seconds":
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

func validateArgumentValueWithRange(name string, arg Argument, argRange argumentRange, value string) error {
	typ := ArgumentType(arg)
	switch typ {
	case "string", "path":
		return nil
	case "rel_path":
		if filepath.IsAbs(value) || strings.HasPrefix(value, "~") {
			return fmt.Errorf("%s: want relative path, got %q", name, value)
		}
	case "int":
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("%s: want int, got %q", name, value)
		}
		if err := validateArgumentRange(name, value, argRangeValue{kind: argumentRangeKindInt, intValue: parsed}, argRange); err != nil {
			return err
		}
	case "float":
		parsed, err := parseArgumentFloat(name, value, argRange.hasBounds())
		if err != nil {
			return err
		}
		if err := validateArgumentRange(name, value, argRangeValue{kind: argumentRangeKindFloat, floatValue: parsed}, argRange); err != nil {
			return err
		}
	case "bool":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s: want bool, got %q", name, value)
		}
	case "duration", "duration:seconds":
		parsed, err := parseArgumentDuration(name, arg, value)
		if err != nil {
			return err
		}
		if err := validateArgumentRange(name, value, argRangeValue{kind: argumentRangeKindDuration, durationValue: parsed}, argRange); err != nil {
			return err
		}
	}
	return nil
}

func argumentValueStringWithRange(name string, arg Argument, argRange argumentRange, raw any) (string, error) {
	value, err := ScalarValueString(raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if err := validateArgumentValueWithRange(name, arg, argRange, value); err != nil {
		return "", err
	}
	return value, nil
}

// ValidateArgumentValue validates a string argument value's scalar type and range.
func ValidateArgumentValue(name string, arg Argument, value string) error {
	argRange, err := parseArgumentRange(name, arg)
	if err != nil {
		return err
	}
	return validateArgumentValueWithRange(name, arg, argRange, value)
}

func resolvedArgumentValueString(name string, arg Argument, raw any, valueOpts ValueBuiltinOptions) (string, error) {
	argRange, err := parseArgumentRange(name, arg)
	if err != nil {
		return "", err
	}
	value, err := ScalarValueString(raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if ArgumentType(arg) == "duration:seconds" {
		duration, err := parseArgumentDuration(name, arg, value)
		if err != nil {
			return "", err
		}
		if err := validateArgumentRange(name, value, argRangeValue{kind: argumentRangeKindDuration, durationValue: duration}, argRange); err != nil {
			return "", err
		}
		if err := ValidateArgumentAcceptedValue(name, arg, value, valueOpts); err != nil {
			return "", err
		}
		return strconv.FormatInt(int64(duration/time.Second), 10), nil
	}
	if err := validateArgumentValueWithRange(name, arg, argRange, value); err != nil {
		return "", err
	}
	if err := ValidateArgumentAcceptedValue(name, arg, value, valueOpts); err != nil {
		return "", err
	}
	value, err = expandPathArgumentValue(arg, value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return value, nil
}

func parseArgumentDuration(name string, arg Argument, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: want duration, got %q", name, value)
	}
	if ArgumentType(arg) == "duration:seconds" && duration%time.Second != 0 {
		return 0, fmt.Errorf("%s: want whole-second duration, got %q", name, value)
	}
	return duration, nil
}

type argumentRangeKind uint8

const (
	argumentRangeKindInt argumentRangeKind = iota + 1
	argumentRangeKindFloat
	argumentRangeKindDuration
)

type argRangeValue struct {
	kind          argumentRangeKind
	text          string
	intValue      int64
	floatValue    float64
	durationValue time.Duration
}

type argumentRange struct {
	min    argRangeValue
	max    argRangeValue
	hasMin bool
	hasMax bool
}

func (argRange argumentRange) hasBounds() bool {
	return argRange.hasMin || argRange.hasMax
}

type argumentRangeError struct {
	field string
	err   error
}

func (err *argumentRangeError) Error() string {
	return err.err.Error()
}

func (err *argumentRangeError) Unwrap() error {
	return err.err
}

func parseArgumentRange(name string, arg Argument) (argumentRange, error) {
	minBound, hasMin, err := argumentRangeBound(name, arg, "min", arg.Min)
	if err != nil {
		return argumentRange{}, &argumentRangeError{field: "min", err: err}
	}
	maxBound, hasMax, err := argumentRangeBound(name, arg, "max", arg.Max)
	if err != nil {
		return argumentRange{}, &argumentRangeError{field: "max", err: err}
	}
	if hasMin && hasMax && rangeValueLess(maxBound, minBound) {
		return argumentRange{}, &argumentRangeError{field: "max", err: fmt.Errorf("%s: max must be greater than or equal to min", name)}
	}
	return argumentRange{
		min:    minBound,
		max:    maxBound,
		hasMin: hasMin,
		hasMax: hasMax,
	}, nil
}

func argumentRangeBound(name string, arg Argument, field string, raw any) (argRangeValue, bool, error) {
	if raw == nil {
		return argRangeValue{}, false, nil
	}
	value, err := ScalarValueString(raw)
	if err != nil {
		return argRangeValue{}, false, fmt.Errorf("%s %s: %w", name, field, err)
	}
	typ := ArgumentType(arg)
	switch typ {
	case "int":
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return argRangeValue{}, false, fmt.Errorf("%s %s: want int, got %q", name, field, value)
		}
		return argRangeValue{kind: argumentRangeKindInt, text: value, intValue: parsed}, true, nil
	case "float":
		parsed, err := parseArgumentFloat(name+" "+field, value, true)
		if err != nil {
			return argRangeValue{}, false, err
		}
		return argRangeValue{kind: argumentRangeKindFloat, text: value, floatValue: parsed}, true, nil
	case "duration", "duration:seconds":
		parsed, err := parseArgumentDuration(name+" "+field, arg, value)
		if err != nil {
			return argRangeValue{}, false, err
		}
		return argRangeValue{kind: argumentRangeKindDuration, text: value, durationValue: parsed}, true, nil
	default:
		return argRangeValue{}, false, fmt.Errorf("%s: %s requires type int, float, duration, or duration:seconds", name, field)
	}
}

func parseArgumentFloat(name, value string, rejectNaN bool) (float64, error) {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || rejectNaN && math.IsNaN(parsed) {
		return 0, fmt.Errorf("%s: want float, got %q", name, value)
	}
	return parsed, nil
}

func validateArgumentRange(name, value string, parsed argRangeValue, argRange argumentRange) error {
	if argRange.hasMin && rangeValueLess(parsed, argRange.min) {
		return fmt.Errorf("%s: want >= %s, got %q", name, argRange.min.text, value)
	}
	if argRange.hasMax && rangeValueLess(argRange.max, parsed) {
		return fmt.Errorf("%s: want <= %s, got %q", name, argRange.max.text, value)
	}
	return nil
}

func rangeValueLess(left, right argRangeValue) bool {
	if left.kind != right.kind {
		panic("mismatched argument range value kinds")
	}
	switch left.kind {
	case argumentRangeKindInt:
		return left.intValue < right.intValue
	case argumentRangeKindFloat:
		return left.floatValue < right.floatValue
	case argumentRangeKindDuration:
		return left.durationValue < right.durationValue
	default:
		panic("unknown argument range value kind")
	}
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
		return "", fmt.Errorf("unsupported value type %s", tomlValueKind(value))
	}
}

func tomlValueKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "bool"
	case int, int64:
		return "int"
	case float64:
		return "float"
	case []any, []string:
		return "array"
	case map[string]any:
		return "table"
	default:
		return "value"
	}
}

func cloneStageCommands(commands []StageCommand) []StageCommand {
	out := make([]StageCommand, len(commands))
	for i, command := range commands {
		out[i] = command
		out[i].Cmd = slices.Clone(command.Cmd)
	}
	return out
}
