package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/systemsandbox"
)

const (
	SystemHelperCommand  = "__shadowtree_system_helper"
	systemHelperProtocol = 1
)

type systemLifecyclePlan struct {
	Protocol       int
	Resolved       recipe.Resolved
	Recipes        map[string]recipe.Recipe
	EnumSets       map[string]recipe.Command
	ConfigEnv      map[string]string
	SourceDir      string
	Environment    map[string]string
	DependencySeed *systemsandbox.DependencySeed
	Caches         []systemsandbox.CachePlan
	SyncOut        []string
	SyncOutAll     bool
	ExportDir      string
}

type systemInvocation struct {
	dir, workspace, export, helper, plan string
}

func runSystemLifecycle(ctx context.Context, runtimeName systemsandbox.RuntimeName, confinement systemsandbox.ConfinementPolicy, image systemsandbox.ImagePlan, options Options, stdin io.Reader, stdout, stderr io.Writer) error {
	invocation, err := createSystemInvocation(image, options)
	if err != nil {
		return err
	}
	defer func() {
		fmt.Fprintln(stderr, "shadowtree: cleaning system invocation")
		_ = os.RemoveAll(invocation.dir)
	}()
	workspace := invocation.workspace
	fmt.Fprintln(stderr, "shadowtree: executing system lifecycle")
	err = systemsandbox.RunLifecycle(ctx, runtimeName, systemsandbox.LifecycleOptions{
		Image: image.FinalTag, Platform: image.Platform, WorkspaceHost: workspace,
		WorkspacePath: options.SourceDir, HelperHost: invocation.helper, PlanHost: invocation.plan,
		Caches: image.Caches, ExportHost: invocation.export,
		Confinement: confinement,
		Stdin:       stdin, Stdout: stdout, Stderr: stderr, Progress: stderr,
	})
	if logErr := syncSystemLog(options.Resolved, options.SourceDir, workspace); logErr != nil && err == nil {
		err = logErr
	}
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return systemContainerExitError(err)
	}
	if err := applySystemCacheExports(image.Caches, options, invocation.export, workspace); err != nil {
		return err
	}
	fmt.Fprintln(stderr, "shadowtree: exporting system workspace")
	if options.SyncOutAll {
		return (&sandboxWorkspace{root: workspace}).SyncAll(options.SourceDir)
	}
	for _, path := range options.Resolved.SyncOut {
		if err := SyncPath(workspace, options.SourceDir, path); err != nil {
			return fmt.Errorf("sync %s: %w", path, err)
		}
	}
	return nil
}

func createSystemInvocation(image systemsandbox.ImagePlan, options Options) (systemInvocation, error) {
	dir, err := os.MkdirTemp("", "shadowtree-system-*")
	if err != nil {
		return systemInvocation{}, err
	}
	fail := func(err error) (systemInvocation, error) {
		_ = os.RemoveAll(dir)
		return systemInvocation{}, err
	}
	workspace := filepath.Join(dir, "workspace")
	if err := CopyTree(options.SourceDir, workspace); err != nil {
		return fail(fmt.Errorf("copy system workspace: %w", err))
	}
	export := filepath.Join(dir, "export")
	if err := os.Mkdir(export, 0o700); err != nil {
		return fail(fmt.Errorf("create cache export: %w", err))
	}
	executable, err := os.Executable()
	if err != nil {
		return fail(fmt.Errorf("resolve lifecycle helper: %w", err))
	}
	helper := filepath.Join(dir, "helper")
	if err := copyRegularFile(executable, helper, 0o500); err != nil {
		return fail(fmt.Errorf("copy lifecycle helper: %w", err))
	}
	environment := envListMap(os.Environ())
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		delete(environment, name)
	}
	maps.Copy(environment, map[string]string{"HOME": "/tmp/shadowtree-home", "TMPDIR": "/tmp"})
	maps.Copy(environment, options.Resolved.GlobalEnv)
	maps.Copy(environment, options.Resolved.Recipe.Env)
	for _, cache := range image.Caches {
		maps.Copy(environment, cache.Environment)
	}
	plan := systemLifecyclePlan{
		Protocol: systemHelperProtocol, Resolved: options.Resolved, Recipes: options.Recipes,
		EnumSets: options.EnumSets, ConfigEnv: options.ConfigEnv, SourceDir: options.SourceDir,
		Environment: environment, DependencySeed: image.DependencySeed,
		Caches: image.Caches, SyncOut: options.Resolved.SyncOut, SyncOutAll: options.SyncOutAll,
		ExportDir: "/opt/shadowtree/export",
	}
	planData, err := json.Marshal(plan)
	if err != nil {
		return fail(fmt.Errorf("encode lifecycle plan: %w", err))
	}
	planPath := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(planPath, planData, 0o600); err != nil {
		return fail(fmt.Errorf("write lifecycle plan: %w", err))
	}
	return systemInvocation{dir: dir, workspace: workspace, export: export, helper: helper, plan: planPath}, nil
}

func systemContainerExitError(err error) error {
	type exitCoder interface{ ExitCode() int }
	var exit exitCoder
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return ExitError{Code: exit.ExitCode()}
	}
	return fmt.Errorf("system container lifecycle: %w", err)
}

func syncSystemLog(resolved recipe.Resolved, source, workspace string) error {
	if resolved.LogPath == "" {
		return nil
	}
	_, _, hostPath, err := recipeLogPath(resolved, source)
	if err != nil {
		return err
	}
	rel, ok := relInside(source, hostPath)
	if !ok {
		return nil
	}
	if _, err := os.Stat(filepath.Join(workspace, rel)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return SyncPath(workspace, source, filepath.ToSlash(rel))
}

// SystemHelperMain executes one validated resolved lifecycle inside the system
// container. It returns a process exit code for the hidden CLI entry point.
func SystemHelperMain(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "system helper requires one plan path")
		return 2
	}
	info, err := os.Stat(args[0])
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintln(os.Stderr, "system lifecycle plan must be a private regular file")
		return 1
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "read system lifecycle plan:", err)
		return 1
	}
	var plan systemLifecyclePlan
	if err := json.Unmarshal(data, &plan); err != nil {
		fmt.Fprintln(os.Stderr, "decode system lifecycle plan:", err)
		return 1
	}
	if plan.Protocol != systemHelperProtocol || plan.SourceDir == "" {
		fmt.Fprintln(os.Stderr, "unsupported or incomplete system lifecycle plan")
		return 1
	}
	if err := os.MkdirAll("/tmp/shadowtree-home", 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "create private home:", err)
		return 1
	}
	if plan.DependencySeed != nil {
		if !filepath.IsLocal(filepath.FromSlash(plan.DependencySeed.TargetPath)) {
			fmt.Fprintln(os.Stderr, "dependency seed target escapes the private workspace")
			return 1
		}
		target := filepath.Join(plan.SourceDir, filepath.FromSlash(plan.DependencySeed.TargetPath))
		if err := CopyTree(plan.DependencySeed.SourcePath, target); err != nil {
			fmt.Fprintln(os.Stderr, "copy dependency seed:", err)
			return 1
		}
	}
	environment := mergedEnv(os.Environ(), plan.Environment)
	options := Options{
		Resolved: plan.Resolved, Recipes: plan.Recipes, EnumSets: plan.EnumSets,
		ConfigEnv: plan.ConfigEnv, SourceDir: plan.SourceDir, Stdin: os.Stdin,
		Stdout: os.Stdout, Stderr: os.Stderr, systemLifecycle: true,
	}
	if err := checkRecipeRequirements(plan.Resolved, plan.SourceDir, environment, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, err = runWithRecipeLog(options, plan.SourceDir, func(logged Options) error {
		return runResolvedCommands(ctx, nil, plan.SourceDir, environment, logged, os.Stdin, os.Stdout, os.Stderr, []string{recipeReferenceStackKey(logged.Resolved.ConfigPath, logged.Resolved.Name)})
	})
	if err == nil {
		if err := exportSystemCaches(plan); err != nil {
			fmt.Fprintln(os.Stderr, "export cache-backed sync paths:", err)
			return 1
		}
		return 0
	}
	if exit, ok := errors.AsType[ExitError](err); ok {
		return exit.Code
	}
	if cause := context.Cause(ctx); cause != nil {
		return 130
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}

func cacheExportPaths(caches []systemsandbox.CachePlan, source string, syncOut []string, all bool) ([]string, error) {
	selected := map[string]bool{}
	for _, cache := range caches {
		if cache.OutputPath == "" {
			continue
		}
		cacheRel, ok := relInside(source, cache.OutputPath)
		if !ok {
			continue
		}
		cacheRel = filepath.Clean(cacheRel)
		if all {
			selected[cacheRel] = true
			continue
		}
		for _, requested := range syncOut {
			path, err := cleanSyncOutPath(requested)
			if err != nil {
				return nil, err
			}
			switch {
			case sameOrDescendant(path, cacheRel):
				selected[path] = true
			case sameOrDescendant(cacheRel, path):
				selected[cacheRel] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(selected)), nil
}

func exportSystemCaches(plan systemLifecyclePlan) error {
	paths, err := cacheExportPaths(plan.Caches, plan.SourceDir, plan.SyncOut, plan.SyncOutAll)
	if err != nil {
		return err
	}
	for _, path := range paths {
		var owner *systemsandbox.CachePlan
		sourceName := ""
		for _, cache := range plan.Caches {
			if cache.OutputPath == "" {
				continue
			}
			outputRel, ok := relInside(plan.SourceDir, cache.OutputPath)
			if !ok || !sameOrDescendant(path, outputRel) {
				continue
			}
			subpath, err := filepath.Rel(outputRel, path)
			if err != nil {
				return err
			}
			owner = &cache
			sourceName = subpath
			break
		}
		if owner == nil {
			return fmt.Errorf("cache export path %s has no owning cache", path)
		}
		if err := syncPathAs(owner.MountPath, plan.ExportDir, sourceName, path); err != nil {
			return err
		}
	}
	return nil
}

func applySystemCacheExports(caches []systemsandbox.CachePlan, options Options, export, workspace string) error {
	paths, err := cacheExportPaths(caches, options.SourceDir, options.Resolved.SyncOut, options.SyncOutAll)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := SyncPath(export, workspace, path); err != nil {
			return fmt.Errorf("apply cache snapshot %s: %w", path, err)
		}
	}
	return nil
}
