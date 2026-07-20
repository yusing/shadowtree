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
	"strings"

	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/systemsandbox"
)

const (
	SystemHelperCommand  = "__shadowtree_system_helper"
	systemHelperProtocol = 1
	systemDefaultLocale  = "C.UTF-8"
)

type systemLifecyclePlan struct {
	Protocol        int
	Resolved        recipe.Resolved
	Recipes         map[string]recipe.Recipe
	EnumSets        map[string]recipe.Command
	ConfigEnv       map[string]string
	SourceDir       string
	Environment     map[string]string
	DependencySeeds []systemsandbox.DependencySeed
	Caches          []systemsandbox.CachePlan
	SyncOut         []string
	SyncOutAll      bool
	ExportDir       string
}

type systemInvocation struct {
	dir, export, helper, plan string
	workspace                 *systemWorkspace
}

type systemWorkspace struct {
	source         string
	dir            string
	copyRoot       string
	overlay        *sandboxWorkspace
	active         *sandboxWorkspace
	mount          systemsandbox.WorkspaceMount
	skipped        []string
	protected      map[string]struct{}
	replaced       []string
	fallbackReason error
}

func runSystemLifecycle(ctx context.Context, runtimeName systemsandbox.RuntimeName, confinement systemsandbox.ConfinementPolicy, strategy systemsandbox.WorkspaceStrategy, image systemsandbox.ImagePlan, options Options, stdin io.Reader, stdout, stderr, verbose io.Writer, setup *systemProgress) (runErr error) {
	fmt.Fprintf(verbose, "shadowtree: setting up %s system workspace with runtime %s\n", strategy, runtimeName)
	if err := setup.Start(systemWorkspaceProgressLabel(strategy, false)); err != nil {
		return errors.Join(fmt.Errorf("render system progress: %w", err), setup.Fail())
	}
	invocation, err := createSystemInvocation(image, options, strategy)
	if err != nil {
		return errors.Join(err, setup.Fail())
	}
	defer func() {
		fmt.Fprintln(verbose, "shadowtree: cleaning system invocation")
		runErr = errors.Join(runErr, invocation.Close(ctx, runtimeName, image.FinalTag, verbose))
	}()
	workspace := invocation.workspace
	if workspace.fallbackReason != nil {
		if err := setup.Start(systemWorkspaceProgressLabel(systemsandbox.WorkspaceCopied, true)); err != nil {
			return errors.Join(fmt.Errorf("render system progress: %w", err), setup.Fail())
		}
		fmt.Fprintf(verbose, "shadowtree: overlay workspace unavailable (%q); using copied fallback\n", workspace.fallbackReason)
	}
	for _, path := range workspace.skipped {
		fmt.Fprintf(verbose, "shadowtree: system workspace skipped unreadable path %q\n", path)
	}
	fmt.Fprintln(verbose, "shadowtree: executing system lifecycle")
	ready := false
	run := func() error {
		return systemsandbox.RunLifecycle(ctx, runtimeName, systemsandbox.LifecycleOptions{
			Image: image.FinalTag, Platform: image.Platform, Workspace: workspace.mount,
			WorkspacePath: options.SourceDir, HelperHost: invocation.helper, PlanHost: invocation.plan,
			Caches: image.Caches, ExportHost: invocation.export,
			Confinement: confinement,
			Stdin:       stdin, Stdout: stdout, Stderr: stderr, Progress: verbose,
			Ready: func() error {
				if err := setup.Succeed(); err != nil {
					return fmt.Errorf("render system progress: %w", err)
				}
				ready = true
				return nil
			},
		})
	}
	err = run()
	if setupErr, ok := errors.AsType[systemsandbox.WorkspaceSetupError](err); ok && workspace.mount.Strategy == systemsandbox.WorkspaceOverlay {
		if progressErr := setup.Start(systemWorkspaceProgressLabel(systemsandbox.WorkspaceCopied, true)); progressErr != nil {
			return errors.Join(err, fmt.Errorf("render system progress: %w", progressErr), setup.Fail())
		}
		fmt.Fprintf(verbose, "shadowtree: runtime overlay workspace unavailable (%q); using copied fallback\n", setupErr)
		if fallbackErr := workspace.useCopiedFallback(options.Resolved.SyncOut, options.SyncOutAll, setupErr); fallbackErr != nil {
			return errors.Join(err, fallbackErr, setup.Fail())
		}
		err = run()
	}
	if logErr := workspace.syncLog(options.Resolved); logErr != nil && err == nil {
		err = logErr
	}
	if err != nil {
		if !ready {
			err = errors.Join(err, setup.Fail())
		}
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return systemContainerExitError(err)
	}
	fmt.Fprintln(verbose, "shadowtree: exporting system workspace")
	return workspace.syncSuccess(image.Caches, options, invocation.export)
}

func systemWorkspaceProgressLabel(strategy systemsandbox.WorkspaceStrategy, fallback bool) string {
	if fallback {
		return "Copy system workspace fallback"
	}
	if strategy == systemsandbox.WorkspaceOverlay {
		return "Setup overlay workspace"
	}
	return "Copy system workspace"
}

func createSystemInvocation(image systemsandbox.ImagePlan, options Options, strategy systemsandbox.WorkspaceStrategy) (systemInvocation, error) {
	dir, err := os.MkdirTemp("", "shadowtree-system-*")
	if err != nil {
		return systemInvocation{}, err
	}
	fail := func(err error) (systemInvocation, error) {
		return systemInvocation{}, errors.Join(err, removeAll(dir))
	}
	workspace, err := prepareSystemWorkspace(options.SourceDir, dir, strategy, options.Resolved.SyncOut, options.SyncOutAll, image.DependencySeeds)
	if err != nil {
		return fail(fmt.Errorf("prepare system workspace: %w", err))
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
	environment := systemLifecycleEnvironment(os.Environ(), options.Resolved, image.Caches)
	plan := systemLifecyclePlan{
		Protocol: systemHelperProtocol, Resolved: options.Resolved, Recipes: options.Recipes,
		EnumSets: options.EnumSets, ConfigEnv: options.ConfigEnv, SourceDir: options.SourceDir,
		Environment: environment, DependencySeeds: image.DependencySeeds,
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

func prepareSystemWorkspace(source, dir string, strategy systemsandbox.WorkspaceStrategy, syncOut []string, syncOutAll bool, seeds []systemsandbox.DependencySeed) (*systemWorkspace, error) {
	replaced, err := validateDependencySeedTargets(seeds, source)
	if err != nil {
		return nil, fmt.Errorf("validate dependency seed targets: %w", err)
	}
	workspace := &systemWorkspace{
		source:   source,
		dir:      dir,
		copyRoot: filepath.Join(dir, "workspace"),
		replaced: replaced,
	}
	if strategy == systemsandbox.WorkspaceOverlay {
		overlay, err := prepareOverlayWorkspace(source, dir, workspace.copyRoot, systemWorkspaceSkip, replaced)
		if err == nil {
			if err := validateSkippedSystemPaths(overlay.skipped, syncOut, syncOutAll); err != nil {
				return nil, err
			}
			if err := validateProtectedSystemPaths(overlay.protectedWhiteouts, syncOut, false); err != nil {
				return nil, err
			}
			workspace.overlay = overlay
			workspace.active = overlay
			workspace.skipped = slices.Clone(overlay.skipped)
			workspace.protected = maps.Clone(overlay.protectedWhiteouts)
			workspace.mount = systemsandbox.WorkspaceMount{
				Strategy: systemsandbox.WorkspaceOverlay,
				Source:   source,
				Upper:    overlay.upper,
				Work:     overlay.work,
			}
			return workspace, nil
		}
		if err := workspace.useCopiedFallback(syncOut, syncOutAll, err); err != nil {
			return nil, err
		}
		return workspace, nil
	}
	if strategy != systemsandbox.WorkspaceCopied {
		return nil, fmt.Errorf("unsupported system workspace strategy %q", strategy)
	}
	if err := workspace.useCopiedFallback(syncOut, syncOutAll, nil); err != nil {
		return nil, err
	}
	workspace.fallbackReason = nil
	return workspace, nil
}

func (workspace *systemWorkspace) useCopiedFallback(syncOut []string, syncOutAll bool, reason error) error {
	if err := removeAll(workspace.copyRoot); err != nil {
		return fmt.Errorf("clear copied system workspace: %w", err)
	}
	protected, _, err := inspectWorkspaceExclusions(workspace.source, systemWorkspaceSkip)
	if err != nil {
		return fmt.Errorf("inspect copied system workspace: %w", err)
	}
	skipped, err := copySystemWorkspaceTree(workspace.source, workspace.copyRoot, workspace.replaced)
	if err != nil {
		return fmt.Errorf("copy system workspace: %w", err)
	}
	protected, skipped, err = filterReplacedWorkspaceExclusions(protected, skipped, workspace.replaced)
	if err != nil {
		return err
	}
	if err := validateSkippedSystemPaths(skipped, syncOut, syncOutAll); err != nil {
		return err
	}
	if err := validateProtectedSystemPaths(protected, syncOut, syncOutAll); err != nil {
		return err
	}
	workspace.active = &sandboxWorkspace{
		root: workspace.copyRoot, source: workspace.source, workDir: workspace.dir,
		skipped: skipped, skip: systemWorkspaceSkip,
	}
	workspace.mount = systemsandbox.WorkspaceMount{Strategy: systemsandbox.WorkspaceCopied, Source: workspace.copyRoot}
	workspace.skipped = slices.Clone(skipped)
	workspace.protected = protected
	workspace.fallbackReason = reason
	return nil
}

func validateProtectedSystemPaths(protected map[string]struct{}, syncOut []string, unsafeSyncAll bool) error {
	cleaned, err := cleanSyncOutPaths(syncOut)
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(protected)) {
		for _, selected := range cleaned {
			if sameOrDescendant(name, selected) || sameOrDescendant(selected, name) {
				return fmt.Errorf("system workspace protected path %q overlaps sync_out path %q", name, selected)
			}
		}
		if unsafeSyncAll && name != ".git" {
			return fmt.Errorf("system workspace cannot use sync-out-all while protecting excluded path %q", name)
		}
	}
	return nil
}

func (workspace *systemWorkspace) syncLog(resolved recipe.Resolved) (syncErr error) {
	if resolved.LogPath == "" {
		return nil
	}
	_, _, hostPath, err := recipeLogPath(resolved, workspace.source)
	if err != nil {
		return err
	}
	rel, ok := relInside(workspace.source, hostPath)
	if !ok {
		return nil
	}
	root, cleanup, err := workspace.active.SyncRoot([]string{filepath.ToSlash(rel)})
	if err != nil {
		return err
	}
	defer func() { syncErr = errors.Join(syncErr, cleanup()) }()
	if _, err := os.Stat(filepath.Join(root, rel)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return syncPathWithPolicy(root, workspace.source, filepath.ToSlash(rel), systemWorkspaceSkip)
}

func (workspace *systemWorkspace) syncSuccess(caches []systemsandbox.CachePlan, options Options, export string) (syncErr error) {
	if options.SyncOutAll {
		if workspace.active.overlay {
			root, cleanup, err := workspace.materializeAll()
			if err != nil {
				return err
			}
			defer func() { syncErr = errors.Join(syncErr, cleanup()) }()
			if err := applySystemCacheExports(caches, options, export, root); err != nil {
				return err
			}
			return replaceDirContentsWithPolicy(root, workspace.source, systemWorkspaceSkip)
		}
		root := workspace.active.root
		if err := applySystemCacheExports(caches, options, export, root); err != nil {
			return err
		}
		return replaceDirContentsWithPolicy(root, workspace.source, systemWorkspaceSkip)
	}
	root, cleanup, err := workspace.active.SyncRoot(options.Resolved.SyncOut)
	if err != nil {
		return err
	}
	defer func() { syncErr = errors.Join(syncErr, cleanup()) }()
	if err := applySystemCacheExports(caches, options, export, root); err != nil {
		return err
	}
	for _, path := range options.Resolved.SyncOut {
		if err := syncPathWithPolicy(root, options.SourceDir, path, systemWorkspaceSkip); err != nil {
			return fmt.Errorf("sync %s: %w", path, err)
		}
	}
	return nil
}

func (workspace *systemWorkspace) materializeAll() (string, func() error, error) {
	root := filepath.Join(workspace.dir, "sync-all")
	cleanup := func() error { return removeAll(root) }
	if err := removeAll(root); err != nil {
		return "", cleanup, fmt.Errorf("clear complete system materialization: %w", err)
	}
	if _, err := copySystemWorkspaceTree(workspace.source, root, workspace.replaced); err != nil {
		return "", cleanup, errors.Join(fmt.Errorf("copy complete system lower workspace: %w", err), cleanup())
	}
	if err := applyOverlayUpper(workspace.active.upper, root, workspace.protected); err != nil {
		return "", cleanup, errors.Join(fmt.Errorf("apply complete system overlay: %w", err), cleanup())
	}
	return root, cleanup, nil
}

func (workspace *systemWorkspace) close(ctx context.Context, runtimeName systemsandbox.RuntimeName, image string, verbose io.Writer) error {
	var errs []error
	if workspace.overlay != nil {
		if err := removeAll(workspace.overlay.overlayDir); err != nil {
			if runtimeName == systemsandbox.Docker || runtimeName == systemsandbox.Podman {
				runtimeErr := systemsandbox.CleanupOverlayWorkspace(ctx, runtimeName, image, workspace.overlay.overlayDir, verbose)
				retryErr := removeAll(workspace.overlay.overlayDir)
				err = overlayCleanupResult(err, runtimeErr, retryErr)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("remove system overlay workspace: %w", err))
			}
		}
	}
	if err := removeAll(workspace.copyRoot); err != nil {
		errs = append(errs, fmt.Errorf("remove copied system workspace: %w", err))
	}
	return errors.Join(errs...)
}

func overlayCleanupResult(initialErr, runtimeErr, retryErr error) error {
	if retryErr == nil {
		return nil
	}
	return errors.Join(initialErr, runtimeErr, retryErr)
}

func (invocation systemInvocation) Close(ctx context.Context, runtimeName systemsandbox.RuntimeName, image string, verbose io.Writer) error {
	var errs []error
	if invocation.workspace != nil {
		errs = append(errs, invocation.workspace.close(ctx, runtimeName, image, verbose))
	}
	for _, target := range []struct {
		label string
		path  string
	}{
		{label: "cache export", path: invocation.export},
		{label: "helper", path: invocation.helper},
		{label: "plan", path: invocation.plan},
	} {
		if target.path == "" {
			continue
		}
		if err := removeAll(target.path); err != nil {
			errs = append(errs, fmt.Errorf("remove system %s: %w", target.label, err))
		}
	}
	if err := removeAll(invocation.dir); err != nil {
		errs = append(errs, fmt.Errorf("remove system invocation: %w", err))
	}
	return errors.Join(errs...)
}

func systemLifecycleEnvironment(host []string, resolved recipe.Resolved, caches []systemsandbox.CachePlan) map[string]string {
	environment := envListMap(host)
	for name := range environment {
		if name == "PATH" || name == "HOME" || name == "TMPDIR" || systemLocaleEnvironmentName(name) {
			delete(environment, name)
		}
	}
	maps.Copy(environment, map[string]string{
		"HOME":   "/tmp/shadowtree-home",
		"TMPDIR": "/tmp",
		"LANG":   systemDefaultLocale,
	})
	maps.Copy(environment, resolved.GlobalEnv)
	maps.Copy(environment, resolved.Recipe.Env)
	for _, cache := range caches {
		maps.Copy(environment, cache.Environment)
	}
	return environment
}

func systemRuntimeEnvironment(runtime []string, planned map[string]string) []string {
	environment := envListMap(runtime)
	for name := range environment {
		if systemLocaleEnvironmentName(name) {
			delete(environment, name)
		}
	}
	maps.Copy(environment, planned)
	return envMapList(environment)
}

func systemLocaleEnvironmentName(name string) bool {
	return name == "LANG" || name == "LANGUAGE" || strings.HasPrefix(name, "LC_")
}

func validateSkippedSystemPaths(skipped, syncOut []string, syncOutAll bool) error {
	if len(skipped) == 0 {
		return nil
	}
	if syncOutAll {
		return errors.New("system workspace cannot use sync-out-all after skipping unreadable paths")
	}
	cleaned, err := cleanSyncOutPaths(syncOut)
	if err != nil {
		return err
	}
	for _, skippedPath := range skipped {
		for _, selected := range cleaned {
			if sameOrDescendant(skippedPath, selected) || sameOrDescendant(selected, skippedPath) {
				return fmt.Errorf("system workspace unreadable path %q overlaps sync_out path %q", skippedPath, selected)
			}
		}
	}
	return nil
}

func systemContainerExitError(err error) error {
	type exitCoder interface{ ExitCode() int }
	var exit exitCoder
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return errors.Join(ExitError{Code: exit.ExitCode()}, fmt.Errorf("system container lifecycle: %w", err))
	}
	return fmt.Errorf("system container lifecycle: %w", err)
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
	if err := validateDependencySeeds(plan.DependencySeeds, plan.SourceDir); err != nil {
		fmt.Fprintln(os.Stderr, "validate dependency seeds:", err)
		return 1
	}
	workspaceRoot, err := os.OpenRoot(plan.SourceDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open dependency seed workspace:", err)
		return 1
	}
	defer workspaceRoot.Close()
	for _, seed := range plan.DependencySeeds {
		if err := workspaceRoot.RemoveAll(filepath.ToSlash(seed.TargetPath)); err != nil {
			fmt.Fprintf(os.Stderr, "clear dependency seed target %s: %v\n", seed.TargetPath, err)
			return 1
		}
	}
	for _, seed := range plan.DependencySeeds {
		if err := copyTreeToRoot(seed.SourcePath, workspaceRoot, filepath.ToSlash(seed.TargetPath)); err != nil {
			fmt.Fprintf(os.Stderr, "copy dependency seed %s to %s: %v\n", seed.Provider, seed.TargetPath, err)
			return 1
		}
	}
	environment := systemRuntimeEnvironment(os.Environ(), plan.Environment)
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

func validateDependencySeeds(seeds []systemsandbox.DependencySeed, workspace string) error {
	if _, err := validateDependencySeedTargets(seeds, workspace); err != nil {
		return err
	}
	for _, seed := range seeds {
		info, err := os.Stat(seed.SourcePath)
		if err != nil {
			return fmt.Errorf("%s source %s: %w", seed.Provider, seed.SourcePath, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s source %s is not a directory", seed.Provider, seed.SourcePath)
		}
	}
	return nil
}

func validateDependencySeedTargets(seeds []systemsandbox.DependencySeed, workspace string) ([]string, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	targets := make([]string, 0, len(seeds))
	for index, seed := range seeds {
		target := filepath.Clean(filepath.FromSlash(seed.TargetPath))
		if seed.Provider == "" || !filepath.IsAbs(seed.SourcePath) || target == "." || !filepath.IsLocal(target) {
			return nil, fmt.Errorf("seed %d has incomplete or unsafe ownership", index)
		}
		var ancestors []string
		for current := target; current != "."; current = filepath.Dir(current) {
			ancestors = append(ancestors, filepath.ToSlash(current))
		}
		slices.Reverse(ancestors)
		for _, current := range ancestors {
			info, err := root.Lstat(current)
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("%s target %s: %w", seed.Provider, current, err)
			}
			if info.Mode().Type() == os.ModeSymlink {
				return nil, fmt.Errorf("%s target %s has a symlink component", seed.Provider, current)
			}
			if current != filepath.ToSlash(target) && !info.IsDir() {
				return nil, fmt.Errorf("%s target parent %s is not a directory", seed.Provider, current)
			}
		}
		targetName := filepath.ToSlash(target)
		for prior := range index {
			left, right := targets[prior], targetName
			if left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/") {
				return nil, fmt.Errorf("seed targets overlap: %s and %s", left, right)
			}
		}
		targets = append(targets, targetName)
	}
	slices.Sort(targets)
	return targets, nil
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
