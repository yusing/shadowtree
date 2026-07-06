package configfile

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/yusing/shadowtree/internal/detect"
	"github.com/yusing/shadowtree/internal/recipe"
)

var Names = []string{".shadowtree.toml"}

type Loaded struct {
	Path   string
	Config recipe.Config
}

// ResolveOptions controls how recipes are assembled from a loaded config.
type ResolveOptions struct {
	Profile         string
	EvalDynamicVars bool
}

func Load(path string) (Loaded, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Loaded{}, err
	}
	cfg := recipe.Config{}
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".toml":
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return Loaded{}, err
		}
	default:
		return Loaded{}, fmt.Errorf("unsupported config extension: %s", ext)
	}
	if err := recipe.ValidateConfig(cfg); err != nil {
		return Loaded{}, err
	}
	return Loaded{Path: path, Config: cfg}, nil
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
	if opts.EvalDynamicVars {
		dynamicVars, err := recipe.EvalVarCommands(ctx, dir, loaded.Config.Env, loaded.Config.VarCommands, loaded.Config.Shell, loaded.Config.ShellPrelude)
		if err != nil {
			return nil, "", fmt.Errorf("var_commands: %w", err)
		}
		maps.Copy(vars, dynamicVars)
	}
	recipes, err := recipe.MergeRecipes(recipe.Builtins(profile, recipe.BuiltinOptions{Dir: dir}), loaded.Config.Recipes)
	if err != nil {
		return nil, "", err
	}
	return recipe.ApplyGlobals(recipes, vars, loaded.Config.Shell, loaded.Config.ShellPrelude), profile, nil
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
			return Loaded{}, false, nil
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
