package recipe

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Scope identifies the user-selected execution scope.
type Scope string

const ScopeAll Scope = "all"

// TargetSource identifies a built-in provider of aggregate execution targets.
type TargetSource string

const (
	GoPackageTargets     TargetSource = "go-packages"
	GoMainPackageTargets TargetSource = "go-main-packages"
	GoModuleTargets      TargetSource = "go-modules"
)

// ExecutionTarget is one aggregate main-command invocation.
type ExecutionTarget struct {
	Label   string
	Value   string
	Workdir string
}

type allPlan struct {
	Domain      string
	Source      TargetSource
	TargetArg   string
	Recipe      Recipe
	Unsupported string
}

func withAllPlan(rec Recipe, domain string, source TargetSource, all Recipe) Recipe {
	targetArg := ""
	for name, arg := range rec.Arguments {
		if arg.Position == 0 {
			continue
		}
		if _, retained := all.Arguments[name]; !retained {
			targetArg = name
			break
		}
	}
	rec.all = &allPlan{Domain: domain, Source: source, TargetArg: targetArg, Recipe: all}
	return rec
}

func withUnsupportedAll(rec Recipe, reason string) Recipe {
	rec.all = &allPlan{Unsupported: reason}
	return rec
}

func allTargetRecipe(rec Recipe, argument string) Recipe {
	all := rec
	all.Arguments = maps.Clone(rec.Arguments)
	delete(all.Arguments, argument)
	all.ForEach = nil
	all.Workdir = ""
	all.Cmd = slices.Clone(rec.Cmd)
	placeholder := "{" + argument + "}"
	for i, value := range all.Cmd {
		if value == placeholder {
			all.Cmd[i] = "{" + ForEachItemPlaceholder + "}"
		}
	}
	all.all = nil
	return all
}

// AllSupport reports how --all applies to rec.
func AllSupport(rec Recipe) (domain, unsupported string, supported bool) {
	if rec.all == nil {
		return "", "the recipe does not declare an aggregate plan", false
	}
	if rec.all.Unsupported != "" {
		return "", rec.all.Unsupported, false
	}
	return rec.all.Domain, "", true
}

// SelectAll selects rec's complete aggregate alternative.
func SelectAll(name string, rec Recipe) (Recipe, string, TargetSource, error) {
	domain, unsupported, supported := AllSupport(rec)
	if !supported {
		return Recipe{}, "", "", fmt.Errorf("recipe %q does not support --all: %s", name, unsupported)
	}
	selected := rec.all.Recipe
	selected.Vars = rec.Vars
	selected.varsExpanded = rec.varsExpanded
	selected.Shell = rec.Shell
	selected.ShellPrelude = rec.ShellPrelude
	selected.Env = rec.Env
	selected.all = rec.all
	return selected, domain, rec.all.Source, nil
}

func validateAllCLIArgs(rec Recipe, cliArgs []string) error {
	for _, token := range cliArgs {
		if token == "--" {
			return nil
		}
		if strings.HasPrefix(token, "-") {
			continue
		}
		if name, _, ok := strings.Cut(token, "="); ok {
			if _, exists := rec.Arguments[name]; exists {
				continue
			}
			if rec.all != nil && name == rec.all.TargetArg {
				return fmt.Errorf("--all cannot be combined with target %q", token)
			}
			return fmt.Errorf("unknown argument %q", name)
		}
		return fmt.Errorf("--all cannot be combined with target %q; use -- before passthrough arguments with separate values", token)
	}
	return nil
}

// ResolveExecutionTargets discovers aggregate targets in the active workspace.
func ResolveExecutionTargets(ctx context.Context, source TargetSource, dir string, env, buildArgs []string) ([]ExecutionTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch source {
	case GoPackageTargets:
		modules, err := discoverGoModules(ctx, dir, "")
		if err != nil {
			return nil, err
		}
		targets := make([]ExecutionTarget, 0, len(modules))
		for _, module := range modules {
			targets = append(targets, ExecutionTarget{Label: module.Help, Value: "./...", Workdir: module.Value})
		}
		return targets, nil
	case GoModuleTargets:
		modules, err := discoverGoModules(ctx, dir, "")
		if err != nil {
			return nil, err
		}
		targets := make([]ExecutionTarget, 0, len(modules))
		for _, module := range modules {
			targets = append(targets, ExecutionTarget{Label: module.Help, Value: module.Value, Workdir: module.Value})
		}
		return targets, nil
	case GoMainPackageTargets:
		return goMainPackageExecutionTargets(ctx, dir, env, buildArgs)
	default:
		return nil, fmt.Errorf("unknown aggregate target source %q", source)
	}
}

func goMainPackageExecutionTargets(ctx context.Context, baseDir string, env, buildArgs []string) ([]ExecutionTarget, error) {
	modules, err := discoverGoModules(ctx, baseDir, "")
	if err != nil {
		return nil, err
	}
	packagesByValue := make(map[string]ValueCandidate)
	for _, module := range modules {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		moduleDir := filepath.Join(baseDir, filepath.FromSlash(module.Value))
		args := []string{"list", "-e", "-buildvcs=false", "-f", "{{if eq .Name \"main\"}}{{.Dir}}\t{{.ImportPath}}{{end}}"}
		args = append(args, goListBuildContextArgs(buildArgs)...)
		args = append(args, "./...")
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = moduleDir
		cmd.Env = env
		output, err := cmd.Output()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("discover main packages in %s: %w", module.Value, err)
		}
		packages, err := parseGoListPackageCandidates(baseDir, output)
		if err != nil {
			return nil, err
		}
		for _, pkg := range packages {
			packagesByValue[pkg.Value] = pkg
		}
	}
	packages := slices.SortedFunc(maps.Values(packagesByValue), func(a, b ValueCandidate) int {
		return strings.Compare(a.Value, b.Value)
	})
	targets := make([]ExecutionTarget, 0, len(packages))
	for _, pkg := range packages {
		pkgDir := baseDir
		if pkg.Value != "." {
			pkgDir = filepath.Join(baseDir, filepath.FromSlash(strings.TrimPrefix(pkg.Value, "./")))
		}
		moduleDir, err := owningGoModule(baseDir, pkgDir)
		if err != nil {
			return nil, fmt.Errorf("main package %s: %w", pkg.Value, err)
		}
		workdir, err := relativeSlashPath(baseDir, moduleDir)
		if err != nil {
			return nil, err
		}
		value, err := goPackageValue(moduleDir, pkgDir)
		if err != nil {
			return nil, err
		}
		targets = append(targets, ExecutionTarget{Label: pkg.Value, Value: value, Workdir: workdir})
	}
	return targets, nil
}

func goListBuildContextArgs(args []string) []string {
	var selected []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-race", arg == "-msan", arg == "-asan":
			selected = append(selected, arg)
		case arg == "-tags", arg == "-mod", arg == "-modfile", arg == "-overlay", arg == "-pgo":
			selected = append(selected, arg)
			if i+1 < len(args) {
				i++
				selected = append(selected, args[i])
			}
		case strings.HasPrefix(arg, "-tags="),
			strings.HasPrefix(arg, "-mod="),
			strings.HasPrefix(arg, "-modfile="),
			strings.HasPrefix(arg, "-overlay="),
			strings.HasPrefix(arg, "-pgo="):
			selected = append(selected, arg)
		}
	}
	return selected
}

func owningGoModule(baseDir, path string) (string, error) {
	baseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for dir := path; ; dir = filepath.Dir(dir) {
		if info, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil && !info.IsDir() {
			return dir, nil
		}
		if dir == baseDir {
			return "", errors.New("no owning go.mod")
		}
		parent := filepath.Dir(dir)
		if parent == dir || !pathWithin(baseDir, parent) {
			return "", errors.New("no owning go.mod")
		}
	}
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
