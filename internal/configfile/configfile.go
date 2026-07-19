package configfile

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/yusing/shadowtree/internal/detect"
	"github.com/yusing/shadowtree/internal/recipe"
)

var Names = []string{".shadowtree.toml"}

type Loaded struct {
	Path                 string
	Config               recipe.Config
	Sources              SourceMap
	explicitRecipeFields map[string]explicitRecipeFields
}

// SourceMap records the config file that last supplied user-facing names.
type SourceMap struct {
	Recipes      map[string]string
	RecipeHelp   map[string]string
	Vars         map[string]string
	VarCommands  map[string]string
	Env          map[string]string
	RecipeVars   map[string]map[string]string
	RecipeEnv    map[string]map[string]string
	Arguments    map[string]map[string]string
	ArgumentHelp map[string]map[string]string
}

type explicitRecipeFields struct {
	RequiredArguments map[string]bool
	Requires          bool
}

func (sources SourceMap) RecipeHelpSource(name string) string {
	if source := sources.RecipeHelp[name]; source != "" {
		return source
	}
	return sources.Recipes[name]
}

func (sources SourceMap) RecipeVarSource(recipeName, name string) string {
	return nestedSource(sources.RecipeVars, recipeName, name)
}

func (sources SourceMap) RecipeEnvSource(recipeName, name string) string {
	return nestedSource(sources.RecipeEnv, recipeName, name)
}

func (sources SourceMap) ArgumentSource(recipeName, name string) string {
	return nestedSource(sources.Arguments, recipeName, name)
}

func (sources SourceMap) ArgumentHelpSource(recipeName, name string) string {
	if source := nestedSource(sources.ArgumentHelp, recipeName, name); source != "" {
		return source
	}
	return sources.ArgumentSource(recipeName, name)
}

func (sources SourceMap) GlobalPlaceholderSource(name string) string {
	if source := sources.VarCommands[name]; source != "" {
		return source
	}
	return sources.Vars[name]
}

func nestedSource(sources map[string]map[string]string, first, second string) string {
	if sources == nil {
		return ""
	}
	return sources[first][second]
}

// ResolveOptions controls how recipes are assembled from a loaded config.
type ResolveOptions struct {
	Profile         string
	EvalDynamicVars bool
	AllowMissingCmd bool
}

// CrossConfigTarget is a resolved @path:recipe target config.
type CrossConfigTarget struct {
	Loaded    Loaded
	Profile   string
	Recipes   map[string]recipe.Recipe
	Dir       string
	SourceDir string
}

func Load(path string) (Loaded, error) {
	loaded, err := load(path, nil, nil)
	if err != nil {
		return Loaded{}, err
	}
	if err := recipe.ValidateEnumSetReferences(loaded.Config); err != nil {
		return Loaded{}, err
	}
	return loaded, nil
}

// LoadConfigWithMeta expands includes for an already-decoded root config with TOML field metadata.
func LoadConfigWithMeta(path string, cfg recipe.Config, md toml.MetaData) (Loaded, error) {
	loaded, _, err := LoadConfigWithMetaOverride(path, path, cfg, md)
	return loaded, err
}

// LoadConfigWithMetaOverride expands path while substituting overridePath with an
// already-decoded config and TOML field metadata. The returned bool reports
// whether overridePath was reached through path's include graph.
func LoadConfigWithMetaOverride(path, overridePath string, cfg recipe.Config, md toml.MetaData) (Loaded, bool, error) {
	overrides := map[string]configOverride{
		CleanAbs(overridePath): {Config: cfg, Meta: md},
	}
	used := false
	loaded, err := load(path, overrides, &used)
	if err != nil {
		return Loaded{}, used, err
	}
	if err := recipe.ValidateEnumSetReferences(loaded.Config); err != nil {
		return Loaded{}, used, err
	}
	return loaded, used, err
}

type configOverride struct {
	Config recipe.Config
	Meta   toml.MetaData
}

func load(path string, overrides map[string]configOverride, overrideUsed *bool, stack ...string) (Loaded, error) {
	absPath := CleanAbs(path)
	if slices.Contains(stack, absPath) {
		return Loaded{}, fmt.Errorf("include cycle: %s", includeCycle(stack, absPath))
	}
	stack = append(stack, absPath)
	var cfg recipe.Config
	var md toml.MetaData
	var err error
	if override, ok := overrides[absPath]; ok {
		cfg = override.Config
		md = override.Meta
		if overrideUsed != nil {
			*overrideUsed = true
		}
	} else {
		cfg, md, err = read(path)
		if err != nil {
			return Loaded{}, err
		}
	}
	if err := validateIncludes(cfg.Include); err != nil {
		return Loaded{}, err
	}
	if err := validateUndecodedFields(md); err != nil {
		return Loaded{}, err
	}
	if err := recipe.ValidateConfig(cfg); err != nil {
		return Loaded{}, err
	}

	loaded := Loaded{Path: path}
	for _, include := range cfg.Include {
		includePath := include
		if !filepath.IsAbs(includePath) {
			includePath = filepath.Join(filepath.Dir(path), includePath)
		}
		included, err := load(includePath, overrides, overrideUsed, stack...)
		if err != nil {
			return Loaded{}, fmt.Errorf("include %q from %s: %w", include, path, err)
		}
		loaded.Config = mergeConfigs(loaded.Config, included.Config, included.explicitRecipeFields)
		loaded.Sources = mergeSourceMaps(loaded.Sources, included.Sources)
		loaded.explicitRecipeFields = mergeExplicitRecipeFields(loaded.explicitRecipeFields, included.explicitRecipeFields)
	}
	cfg.Include = nil
	explicitFields := explicitRecipeFieldsFromMeta(cfg, md)
	loaded.Config = mergeConfigs(loaded.Config, cfg, explicitFields)
	loaded.Sources = mergeSourceMaps(loaded.Sources, sourceMapForConfig(cfg, md, absPath))
	loaded.explicitRecipeFields = mergeExplicitRecipeFields(loaded.explicitRecipeFields, explicitFields)
	if err := recipe.ValidateConfig(loaded.Config); err != nil {
		return Loaded{}, err
	}
	return loaded, nil
}

func read(path string) (recipe.Config, toml.MetaData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return recipe.Config{}, toml.MetaData{}, err
	}
	cfg := recipe.Config{}
	var md toml.MetaData
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".toml":
		var err error
		md, err = toml.Decode(string(data), &cfg)
		if err != nil {
			return recipe.Config{}, toml.MetaData{}, err
		}
	default:
		return recipe.Config{}, toml.MetaData{}, fmt.Errorf("unsupported config extension: %s", ext)
	}
	return cfg, md, nil
}

func validateIncludes(includes []string) error {
	for i, include := range includes {
		if strings.TrimSpace(include) != include || include == "" {
			return fmt.Errorf("include[%d] must be a non-empty path without surrounding whitespace", i)
		}
	}
	return nil
}

func validateUndecodedFields(md toml.MetaData) error {
	for _, key := range md.Undecoded() {
		if len(key) == 0 || len(key) == 1 && key[0] == "$schema" || knownUndecodedStageCommandField(key) {
			continue
		}
		return fmt.Errorf("unknown field %s", key.String())
	}
	return nil
}

func knownUndecodedStageCommandField(key toml.Key) bool {
	return len(key) == 4 &&
		key[0] == "recipes" &&
		(key[2] == "pre" || key[2] == "post") &&
		recipe.IsStageCommandField(key[3])
}

func CleanAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

func SamePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return CleanAbs(a) == CleanAbs(b)
}

func includeCycle(stack []string, path string) string {
	cycle := append(slices.Clone(stack), path)
	for i, item := range cycle {
		cycle[i] = filepath.ToSlash(item)
	}
	return strings.Join(cycle, " -> ")
}

func mergeConfigs(base, override recipe.Config, explicitFields map[string]explicitRecipeFields) recipe.Config {
	out := base
	if override.Profile != "" {
		out.Profile = override.Profile
	}
	out.Env = mergeStringMaps(out.Env, override.Env)
	out.Vars = mergeStringMaps(out.Vars, override.Vars)
	out.VarCommands = mergeCommandMaps(out.VarCommands, override.VarCommands)
	out.EnumSets = mergeCommandMaps(out.EnumSets, override.EnumSets)
	if override.Shell != "" {
		out.Shell = override.Shell
	}
	out.ShellPrelude = recipe.JoinShell(out.ShellPrelude, override.ShellPrelude)
	out.Recipes = mergeRecipeMaps(out.Recipes, override.Recipes, explicitFields)
	return out
}

func mergeRecipeMaps(base, override map[string]recipe.Recipe, explicitFields map[string]explicitRecipeFields) map[string]recipe.Recipe {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := maps.Clone(base)
	if out == nil {
		out = map[string]recipe.Recipe{}
	}
	for name, rec := range override {
		out[name] = mergeIncludedRecipe(out[name], rec, explicitFields[name])
	}
	return out
}

func mergeIncludedRecipe(base, override recipe.Recipe, fields explicitRecipeFields) recipe.Recipe {
	out := recipe.MergeRecipe(base, override)
	if fields.Requires {
		out.Requires = override.Requires
	}
	if override.Vars != nil {
		out.Vars = mergeStringMaps(base.Vars, override.Vars)
	}
	if override.Env != nil {
		out.Env = mergeStringMaps(base.Env, override.Env)
	}
	for argName := range fields.RequiredArguments {
		arg := out.Arguments[argName]
		arg.Required = override.Arguments[argName].Required
		out.Arguments[argName] = arg
	}
	return out
}

func mergeStringMaps(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, override)
	return out
}

func mergeCommandMaps(base, override map[string]recipe.Command) map[string]recipe.Command {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := maps.Clone(base)
	if out == nil {
		out = map[string]recipe.Command{}
	}
	for name, command := range override {
		out[name] = slices.Clone(command)
	}
	return out
}

func explicitRecipeFieldsFromMeta(cfg recipe.Config, md toml.MetaData) map[string]explicitRecipeFields {
	fields := map[string]explicitRecipeFields{}
	for recipeName, rec := range cfg.Recipes {
		current := fields[recipeName]
		if md.IsDefined("recipes", recipeName, "requires") {
			current.Requires = true
		}
		for argName := range rec.Arguments {
			if !md.IsDefined("recipes", recipeName, "arguments", argName, "required") {
				continue
			}
			if current.RequiredArguments == nil {
				current.RequiredArguments = map[string]bool{}
			}
			current.RequiredArguments[argName] = true
		}
		if current.Requires || len(current.RequiredArguments) > 0 {
			fields[recipeName] = current
		}
	}
	return fields
}

func mergeExplicitRecipeFields(base, override map[string]explicitRecipeFields) map[string]explicitRecipeFields {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := cloneExplicitRecipeFields(base)
	if out == nil {
		out = map[string]explicitRecipeFields{}
	}
	for recipeName, fields := range override {
		current := out[recipeName]
		current.Requires = current.Requires || fields.Requires
		if len(fields.RequiredArguments) > 0 {
			if current.RequiredArguments == nil {
				current.RequiredArguments = map[string]bool{}
			}
			for argName := range fields.RequiredArguments {
				current.RequiredArguments[argName] = true
			}
		}
		out[recipeName] = current
	}
	return out
}

func cloneExplicitRecipeFields(values map[string]explicitRecipeFields) map[string]explicitRecipeFields {
	if values == nil {
		return nil
	}
	out := make(map[string]explicitRecipeFields, len(values))
	for recipeName, fields := range values {
		out[recipeName] = explicitRecipeFields{
			RequiredArguments: maps.Clone(fields.RequiredArguments),
			Requires:          fields.Requires,
		}
	}
	return out
}

func sourceMapForConfig(cfg recipe.Config, md toml.MetaData, path string) SourceMap {
	sources := SourceMap{
		Recipes:      map[string]string{},
		RecipeHelp:   map[string]string{},
		Vars:         map[string]string{},
		VarCommands:  map[string]string{},
		Env:          map[string]string{},
		RecipeVars:   map[string]map[string]string{},
		RecipeEnv:    map[string]map[string]string{},
		Arguments:    map[string]map[string]string{},
		ArgumentHelp: map[string]map[string]string{},
	}
	for name := range cfg.Vars {
		sources.Vars[name] = path
	}
	for name := range cfg.VarCommands {
		sources.VarCommands[name] = path
	}
	for name := range cfg.Env {
		sources.Env[name] = path
	}
	for recipeName, rec := range cfg.Recipes {
		sources.Recipes[recipeName] = path
		if md.IsDefined("recipes", recipeName, "help") {
			sources.RecipeHelp[recipeName] = path
		}
		if len(rec.Vars) > 0 {
			sources.RecipeVars[recipeName] = map[string]string{}
			for name := range rec.Vars {
				sources.RecipeVars[recipeName][name] = path
			}
		}
		if len(rec.Env) > 0 {
			sources.RecipeEnv[recipeName] = map[string]string{}
			for name := range rec.Env {
				sources.RecipeEnv[recipeName][name] = path
			}
		}
		if len(rec.Arguments) > 0 {
			sources.Arguments[recipeName] = map[string]string{}
			for name := range rec.Arguments {
				sources.Arguments[recipeName][name] = path
				if md.IsDefined("recipes", recipeName, "arguments", name, "help") {
					if sources.ArgumentHelp[recipeName] == nil {
						sources.ArgumentHelp[recipeName] = map[string]string{}
					}
					sources.ArgumentHelp[recipeName][name] = path
				}
			}
		}
	}
	return sources
}

func mergeSourceMaps(base, override SourceMap) SourceMap {
	return SourceMap{
		Recipes:      mergeStringMaps(base.Recipes, override.Recipes),
		RecipeHelp:   mergeStringMaps(base.RecipeHelp, override.RecipeHelp),
		Vars:         mergeStringMaps(base.Vars, override.Vars),
		VarCommands:  mergeStringMaps(base.VarCommands, override.VarCommands),
		Env:          mergeStringMaps(base.Env, override.Env),
		RecipeVars:   mergeNestedSourceMaps(base.RecipeVars, override.RecipeVars),
		RecipeEnv:    mergeNestedSourceMaps(base.RecipeEnv, override.RecipeEnv),
		Arguments:    mergeNestedSourceMaps(base.Arguments, override.Arguments),
		ArgumentHelp: mergeNestedSourceMaps(base.ArgumentHelp, override.ArgumentHelp),
	}
}

func mergeNestedSourceMaps(base, override map[string]map[string]string) map[string]map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := cloneNestedSourceMap(base)
	if out == nil {
		out = map[string]map[string]string{}
	}
	for name, values := range override {
		out[name] = mergeStringMaps(out[name], values)
	}
	return out
}

func cloneNestedSourceMap(values map[string]map[string]string) map[string]map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]map[string]string, len(values))
	for name, nested := range values {
		out[name] = maps.Clone(nested)
	}
	return out
}

// ResolveRecipes merges built-ins, config recipes, globals, and optional dynamic vars.
func ResolveRecipes(ctx context.Context, loaded Loaded, dir string, opts ResolveOptions) (map[string]recipe.Recipe, string, error) {
	profile := opts.Profile
	if profile == "" {
		profile = loaded.Config.Profile
	}
	if profile == "" && loaded.Path == "" {
		profile = detect.Profile(dir)
	}
	if profile != "" && !recipe.SupportsProfile(profile) {
		return nil, "", fmt.Errorf("unsupported profile: %s", profile)
	}
	vars := maps.Clone(loaded.Config.Vars)
	if vars == nil {
		vars = map[string]string{}
	}
	dynamicVars := map[string]string{}
	if opts.EvalDynamicVars {
		var err error
		dynamicVars, err = recipe.EvalVarCommands(ctx, dir, loaded.Config.Env, loaded.Config.VarCommands, loaded.Config.Shell, loaded.Config.ShellPrelude)
		if err != nil {
			return nil, "", fmt.Errorf("var_commands: %w", err)
		}
	} else {
		for name := range loaded.Config.VarCommands {
			dynamicVars[name] = "{" + name + "}"
		}
	}
	recipes, err := recipe.MergeRecipes(recipe.Builtins(profile, recipe.BuiltinOptions{Context: ctx, Dir: dir}), loaded.Config.Recipes)
	if err != nil {
		return nil, "", err
	}
	recipes, err = recipe.ApplyGlobalsExpanded(recipes, vars, dynamicVars, loaded.Config.Shell, loaded.Config.ShellPrelude)
	if err != nil {
		return nil, "", err
	}
	if !opts.AllowMissingCmd {
		if err := recipe.ValidateResolvedRecipes(recipes); err != nil {
			return nil, "", err
		}
	}
	return recipes, profile, nil
}

func ResolveCrossConfigReference(ctx context.Context, refPath, configPath, sourceDir string, opts ResolveOptions) (CrossConfigTarget, error) {
	targetDir, resolvedSource, err := crossConfigTargetDir(refPath, configPath, sourceDir)
	if err != nil {
		return CrossConfigTarget{}, err
	}
	loaded, err := Load(filepath.Join(targetDir, Names[0]))
	if err != nil {
		return CrossConfigTarget{}, err
	}
	recipes, profile, err := ResolveRecipes(ctx, loaded, targetDir, opts)
	if err != nil {
		return CrossConfigTarget{}, err
	}
	return CrossConfigTarget{
		Loaded:    loaded,
		Profile:   profile,
		Recipes:   recipes,
		Dir:       targetDir,
		SourceDir: resolvedSource,
	}, nil
}

func crossConfigTargetDir(path, configPath, sourceDir string) (string, string, error) {
	if sourceDir == "" {
		sourceDir = "."
	}
	source, err := filepath.Abs(sourceDir)
	if err != nil {
		return "", "", err
	}
	base := source
	if configPath != "" {
		base = filepath.Dir(configPath)
		if !filepath.IsAbs(base) {
			base = filepath.Join(source, base)
		}
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", "", fmt.Errorf("@%s: %w", path, err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("@%s: not a directory", path)
	}
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", "", err
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", fmt.Errorf("@%s: %w", path, err)
	}
	rel, err := filepath.Rel(resolvedSource, resolvedTarget)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("@%s: path is outside source", path)
	}
	return resolvedTarget, resolvedSource, nil
}

func Find(cwd string) (Loaded, bool, error) {
	root := detect.RepoRoot(cwd)
	dir := cwd
	for {
		for _, name := range Names {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				loaded, err := Load(path)
				return loaded, true, err
			} else if !errors.Is(err, os.ErrNotExist) {
				return Loaded{}, false, err
			}
		}
		if dir == root {
			superproject, ok := detect.SuperprojectRoot(root)
			if !ok {
				return Loaded{}, false, nil
			}
			dir = filepath.Dir(root)
			root = superproject
			continue
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Loaded{}, false, nil
		}
		dir = parent
	}
}

func Init(path string) error {
	if path == "" {
		path = ".shadowtree.toml"
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	const sample = `profile = "go"

[recipes.codegen-test]
help = "Generate code, then run Go tests."
for_each = "@go-modules"
workdir = "{item}"
cmd = "set -e; go generate ./...; go test ./... {@}"
`
	return os.WriteFile(path, []byte(sample), 0o644)
}
