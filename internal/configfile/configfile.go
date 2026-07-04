package configfile

import (
	"errors"
	"fmt"
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

[recipes.test]
help = "Run Go tests."
cmd = ["go", "test"]
default_args = ["./..."]

[recipes.build]
help = "Build a Go package."
cmd = ["go", "build"]
default_args = ["{project}"]

[recipes.build.arguments.project]
help = "Go package to build."
type = "string"
position = 1
default = "./..."

[recipes.codegen-test]
help = "Generate code, then run Go tests."
pre = [["go", "generate", "./..."]]
cmd = ["go", "test"]
default_args = ["./..."]

[recipes.tidy]
help = "Tidy Go module files."
sandboxed = false
cmd = ["go", "mod", "tidy"]
`
	return os.WriteFile(path, []byte(sample), 0o644)
}
