package runner

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/yusing/shadowtree/internal/configfile"
	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/systemsandbox"
)

type Options struct {
	Resolved        recipe.Resolved
	Recipes         map[string]recipe.Recipe
	EnumSets        map[string]recipe.Command
	ConfigEnv       map[string]string
	SourceDir       string
	PrintOnly       bool
	PrintExpanded   bool
	CheckOnly       bool
	CheckShell      bool
	Verbose         bool
	SyncOutAll      bool
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	systemLifecycle bool
	stageLog        *stageLogger
}

type ExitError struct {
	Code int
}

// CommandOutputOptions controls recipe references inside output-producing commands.
type CommandOutputOptions struct {
	Recipes    map[string]recipe.Recipe
	EnumSets   map[string]recipe.Command
	ConfigPath string
	SourceDir  string
}

var errReflinkUnsupported = errors.New("reflink unsupported")

const OverlayHelperCommand = "__shadowtree_overlay_helper"

const (
	phasePre     = recipe.LogStagePre
	phaseMain    = "main"
	phasePost    = recipe.LogStagePost
	phaseForEach = "for_each"
)

const stageBoundaryCommandMax = 120

type stageLogger struct {
	file   io.Writer
	stages map[string]bool
	tee    bool
}

func (err ExitError) Error() string {
	return fmt.Sprintf("command exited with status %d", err.Code)
}

func openRecipeLog(resolved recipe.Resolved, sourceDir string) (*os.File, string, error) {
	if resolved.LogPath == "" {
		return nil, "", nil
	}
	base, name, path, err := recipeLogPath(resolved, sourceDir)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, "", fmt.Errorf("open log root: %w", err)
	}
	defer root.Close()
	if err := ensureRecipeLogParent(root, name); err != nil {
		return nil, "", fmt.Errorf("create log directory: %w", err)
	}
	if err := removeRecipeLogLeaf(root, name); err != nil {
		return nil, "", err
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, "", fmt.Errorf("open log: %w", err)
	}
	return file, path, nil
}

func recipeLogPath(resolved recipe.Resolved, sourceDir string) (string, string, string, error) {
	path := filepath.Clean(filepath.FromSlash(resolved.LogPath))
	if path == "." || filepath.IsAbs(path) || !filepath.IsLocal(path) {
		return "", "", "", fmt.Errorf("log path must be relative to config directory: %s", resolved.LogPath)
	}
	base := sourceDir
	if resolved.ConfigPath != "" {
		base = filepath.Dir(resolved.ConfigPath)
	}
	name := filepath.ToSlash(path)
	return base, name, filepath.Join(base, path), nil
}

func ensureRecipeLogParent(root *os.Root, name string) error {
	dir := pathDirSlash(name)
	if dir == "." {
		return nil
	}
	current := ""
	for part := range strings.SplitSeq(dir, "/") {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		info, err := root.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := root.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		case err != nil:
			return err
		case info.Mode().Type() == os.ModeSymlink:
			if err := root.RemoveAll(current); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := root.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		case !info.IsDir():
			return fmt.Errorf("log parent is not a directory: %s", current)
		}
	}
	return nil
}

func removeRecipeLogLeaf(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat log: %w", err)
	}
	mode := info.Mode()
	if mode.IsDir() {
		return fmt.Errorf("log path is a directory: %s", name)
	}
	if mode.Type() != 0 && mode.Type() != os.ModeSymlink {
		return fmt.Errorf("log path is not a regular file: %s", name)
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove log: %w", err)
	}
	return nil
}

func runWithRecipeLog(options Options, sourceDir string, run func(Options) error) (string, error) {
	logFile, logPath, err := openRecipeLog(options.Resolved, sourceDir)
	if err != nil {
		return "", err
	}
	if logFile == nil {
		return "", run(options)
	}
	options.stageLog = newStageLogger(logFile, options.Resolved)
	runErr := run(options)
	closeErr := logFile.Close()
	if runErr != nil {
		return logPath, runErr
	}
	if closeErr != nil {
		return logPath, fmt.Errorf("close log: %w", closeErr)
	}
	return logPath, nil
}

func newStageLogger(w io.Writer, resolved recipe.Resolved) *stageLogger {
	stages := map[string]bool{}
	for _, stage := range resolved.LogStages {
		stages[stage] = true
	}
	return &stageLogger{file: w, stages: stages, tee: resolved.LogTee}
}

func (logger *stageLogger) logs(phase string) bool {
	return logger != nil && logger.stages[logStageForPhase(phase)]
}

func (logger *stageLogger) writers(phase string, stdout, stderr io.Writer) (io.Writer, io.Writer) {
	if !logger.logs(phase) {
		return stdout, stderr
	}
	if !logger.tee {
		return logger.file, logger.file
	}
	return io.MultiWriter(stdout, logger.file), io.MultiWriter(stderr, logger.file)
}

func (logger *stageLogger) writeBoundary(boundary string) error {
	if _, err := fmt.Fprintln(logger.file, boundary); err != nil {
		return fmt.Errorf("write log boundary: %w", err)
	}
	return nil
}

func logStageForPhase(phase string) string {
	if phase == phaseMain {
		return recipe.LogStageCmd
	}
	return phase
}

type installableRequirement struct {
	name     string
	guidance string
}

func checkRecipeRequirements(resolved recipe.Resolved, dir string, env []string, stderr io.Writer) error {
	req := resolved.Recipe.Requires
	if req.Empty() {
		return nil
	}
	var missingCommands []string
	for _, name := range req.Commands {
		if !executableAvailable(name, env, dir) {
			missingCommands = append(missingCommands, name)
		}
	}
	var missingGo []installableRequirement
	for _, name := range slices.Sorted(maps.Keys(req.GoCommands)) {
		if !executableAvailable(name, env, dir) {
			missingGo = append(missingGo, installableRequirement{name: name, guidance: "go install " + req.GoCommands[name]})
		}
	}
	var missingNode []installableRequirement
	nodePM := ""
	if len(req.NodeCommands) > 0 {
		nodePM = recipe.NodePackageManager(nodeRequirementDir(dir, resolved.Recipe))
	}
	for _, name := range slices.Sorted(maps.Keys(req.NodeCommands)) {
		if !executableAvailable(name, env, dir) {
			missingNode = append(missingNode, installableRequirement{name: name, guidance: recipe.NodeInstallCommandForPackageManager(nodePM, req.NodeCommands[name])})
		}
	}
	if len(missingCommands) > 0 || len(missingGo) > 0 || len(missingNode) > 0 {
		return missingRequirementsError(resolved.Name, missingCommands, missingGo, missingNode)
	}
	var missingOptional []string
	for _, name := range req.OptionalCommands {
		if !executableAvailable(name, env, dir) {
			missingOptional = append(missingOptional, name)
		}
	}
	if len(missingOptional) > 0 {
		fmt.Fprintf(stderr, "shadowtree: recipe %q optional tools not found: %s\n", resolved.Name, strings.Join(missingOptional, ", "))
	}
	return nil
}

func executableAvailable(name string, env []string, dir string) bool {
	_, err := lookPathEnv(name, env, dir)
	return err == nil
}

func nodeRequirementDir(dir string, rec recipe.Recipe) string {
	if len(rec.ForEach) > 0 || rec.Workdir == "" {
		return dir
	}
	workdir, err := recipeWorkdir(dir, rec.Workdir)
	if err != nil {
		return dir
	}
	return workdir
}

func missingRequirementsError(recipeName string, commands []string, goCommands, nodeCommands []installableRequirement) error {
	var messages []string
	if len(commands) > 0 {
		messages = append(messages, fmt.Sprintf("recipe %q missing required tools: %s", recipeName, strings.Join(commands, ", ")))
	}
	if len(goCommands) > 0 {
		messages = append(messages, fmt.Sprintf("recipe %q missing required Go tools: %s", recipeName, installableRequirementList(goCommands)))
	}
	if len(nodeCommands) > 0 {
		messages = append(messages, fmt.Sprintf("recipe %q missing required Node tools: %s", recipeName, installableRequirementList(nodeCommands)))
	}
	return errors.New(strings.Join(messages, "; "))
}

func installableRequirementList(missing []installableRequirement) string {
	parts := make([]string, 0, len(missing))
	for _, item := range missing {
		parts = append(parts, fmt.Sprintf("%s (%s)", item.name, item.guidance))
	}
	return strings.Join(parts, ", ")
}

func Run(ctx context.Context, options Options) (runErr error) {
	stdout := cmp.Or[io.Writer](options.Stdout, os.Stdout)
	stderr := cmp.Or[io.Writer](options.Stderr, os.Stderr)
	for _, warning := range options.Resolved.Warnings {
		fmt.Fprintf(stderr, "shadowtree: warning: recipe %q args: %s\n", options.Resolved.Name, warning)
	}
	source, err := filepath.Abs(options.SourceDir)
	if err != nil {
		return err
	}
	if options.Resolved.SandboxMode == recipe.SandboxModeSystem {
		canonicalSource, err := filepath.EvalSymlinks(source)
		if err != nil {
			return fmt.Errorf("canonical system source: %w", err)
		}
		options.Resolved.ConfigPath, err = rebaseSystemConfigPath(options.Resolved.ConfigPath, source, canonicalSource)
		if err != nil {
			return err
		}
		source = canonicalSource
	}
	options.SourceDir = source
	if options.PrintOnly {
		if options.PrintExpanded {
			printExpandedPlan(stdout, options.Resolved)
		} else {
			printPlan(stdout, options.Resolved)
		}
		return printSystemImagePlan(ctx, stdout, options, options.PrintExpanded)
	}
	if options.CheckOnly {
		if err := validatePlan(ctx, options); err != nil {
			return err
		}
		return nil
	}
	if options.Resolved.SandboxMode == recipe.SandboxModeSystem {
		return prepareSystemImages(ctx, options, stderr)
	}
	stdin := cmp.Or[io.Reader](options.Stdin, os.Stdin)
	env := mergedEnv(os.Environ(), options.Resolved.GlobalEnv, options.Resolved.Recipe.Env)
	if err := checkRecipeRequirements(options.Resolved, source, env, stderr); err != nil {
		return err
	}
	if options.Resolved.SandboxMode == recipe.SandboxModeHost {
		if options.Verbose {
			fmt.Fprintf(stderr, "shadowtree: running unsandboxed in %s\n", source)
		}
		_, err := runWithRecipeLog(options, source, func(logged Options) error {
			return runResolvedCommands(ctx, nil, source, env, logged, stdin, stdout, stderr, []string{recipeReferenceStackKey(logged.Resolved.ConfigPath, logged.Resolved.Name)})
		})
		return err
	}
	workDir, err := os.MkdirTemp("", "shadowtree-*")
	if err != nil {
		return err
	}
	workspace := filepath.Join(workDir, "workspace")
	sandbox, err := createSandboxWorkspace(ctx, source, workDir, workspace, stderr, options.Verbose)
	if err != nil {
		_ = removeAll(workDir)
		return err
	}
	defer func() {
		if err := sandbox.Close(); err != nil && runErr == nil {
			runErr = err
		}
	}()
	logPath, err := runWithRecipeLog(options, source, func(logged Options) error {
		return runResolvedCommands(ctx, sandbox, sandbox.root, env, logged, stdin, stdout, stderr, []string{recipeReferenceStackKey(logged.Resolved.ConfigPath, logged.Resolved.Name)})
	})
	if err != nil {
		return err
	}
	if err := mirrorRecipeLogToSandbox(logPath, source, sandbox); err != nil {
		return err
	}
	if options.SyncOutAll {
		if options.Verbose {
			fmt.Fprintln(stderr, "shadowtree: syncing entire workspace")
		}
		return sandbox.SyncAll(source)
	}
	syncRoot, cleanup, err := sandbox.SyncRoot(options.Resolved.SyncOut)
	if err != nil {
		return err
	}
	defer cleanup()
	for _, path := range options.Resolved.SyncOut {
		if err := SyncPath(syncRoot, source, path); err != nil {
			return fmt.Errorf("sync %s: %w", path, err)
		}
	}
	return nil
}

func prepareSystemImages(ctx context.Context, options Options, progress io.Writer) error {
	request, err := resolvedSystemImageRequest(ctx, options)
	if err != nil {
		return wrapSystemImageRequirements(options.Resolved.Name, err)
	}
	resolved := request.Root
	plan, err := systemsandbox.PlanComposition(request, options.SourceDir)
	if err != nil {
		return fmt.Errorf("recipe %q system image plan: %w", resolved.Name, err)
	}
	if err := validateSystemHelperPlatform(runtime.GOOS, runtime.GOARCH, plan.Platform); err != nil {
		return fmt.Errorf("recipe %q system lifecycle helper: %w", resolved.Name, err)
	}
	runtimeSelection, err := systemsandbox.Detect(ctx, progress)
	if err != nil {
		return fmt.Errorf("recipe %q system runtime detection: %w", resolved.Name, err)
	}
	runtimeName := runtimeSelection.Name
	plan, err = systemsandbox.ApplyConfinementPolicy(plan, runtimeSelection.Confinement)
	if err != nil {
		return fmt.Errorf("recipe %q system cache identity: %w", resolved.Name, err)
	}
	if err := systemsandbox.BuildImages(ctx, runtimeName, plan, progress); err != nil {
		return fmt.Errorf("recipe %q system image build: %w", resolved.Name, err)
	}
	releaseCaches, err := systemsandbox.AcquireCacheExecutionLocks(ctx, plan.Caches, progress)
	if err != nil {
		return fmt.Errorf("recipe %q system cache lock: %w", resolved.Name, err)
	}
	defer releaseCaches()
	if err := systemsandbox.PrepareCaches(ctx, runtimeName, plan.Caches, progress); err != nil {
		return fmt.Errorf("recipe %q system caches: %w", resolved.Name, err)
	}
	if err := systemsandbox.WaitForCacheAvailability(ctx, runtimeName, plan.Caches, progress); err != nil {
		return fmt.Errorf("recipe %q system cache availability: %w", resolved.Name, err)
	}
	stdin := cmp.Or[io.Reader](options.Stdin, os.Stdin)
	stdout := cmp.Or[io.Writer](options.Stdout, os.Stdout)
	return runSystemLifecycle(ctx, runtimeName, runtimeSelection.Confinement, plan, options, stdin, stdout, progress)
}

func rebaseSystemConfigPath(configPath, source, canonicalSource string) (string, error) {
	if configPath == "" {
		return "", nil
	}
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("canonical system config: %w", err)
	}
	rel, ok := relInside(source, configPath)
	if !ok {
		return "", fmt.Errorf("system config path is outside source: %s", configPath)
	}
	return filepath.Join(canonicalSource, rel), nil
}

func validateSystemHelperPlatform(goos, goarch, platform string) error {
	if goos != "linux" {
		return fmt.Errorf("host executable targets %s/%s, but system images require Linux", goos, goarch)
	}
	want := "linux/" + goarch
	if platform != want {
		return fmt.Errorf("host executable targets %s, but system image targets %s", want, platform)
	}
	return nil
}

func printSystemImagePlan(ctx context.Context, w io.Writer, options Options, expanded bool) error {
	if options.Resolved.SandboxMode != recipe.SandboxModeSystem {
		return nil
	}
	request, err := resolvedSystemImageRequest(ctx, options)
	if err != nil {
		return wrapSystemImageRequirements(options.Resolved.Name, err)
	}
	resolved := request.Root
	plan, err := systemsandbox.PlanComposition(request, options.SourceDir)
	if err != nil {
		return fmt.Errorf("recipe %q system image plan: %w", resolved.Name, err)
	}
	fmt.Fprintf(w, "base_image: %s\n", plan.BaseImage)
	fmt.Fprintf(w, "final_image: %s\n", plan.FinalTag)
	for _, stage := range plan.Stages {
		fmt.Fprintf(w, "image_stage.%s.key: %s\n", stage.Name, stage.Key)
		fmt.Fprintf(w, "image_stage.%s.tag: %s\n", stage.Name, stage.Tag)
		if expanded {
			fmt.Fprintf(w, "image_stage.%s.containerfile: |\n", stage.Name)
			for line := range strings.Lines(stage.Containerfile) {
				fmt.Fprintf(w, "  %s", line)
			}
			for _, path := range slices.Sorted(maps.Keys(stage.ContextHashes)) {
				fmt.Fprintf(w, "image_stage.%s.context.%s.sha256: %s\n", stage.Name, path, stage.ContextHashes[path])
			}
			for _, name := range slices.Sorted(maps.Keys(stage.Metadata)) {
				fmt.Fprintf(w, "image_stage.%s.metadata.%s: %s\n", stage.Name, name, stage.Metadata[name])
			}
		}
	}
	for index, seed := range plan.DependencySeeds {
		fmt.Fprintf(w, "dependency_seed[%d].provider: %s\n", index, seed.Provider)
		fmt.Fprintf(w, "dependency_seed[%d].source: %s\n", index, seed.SourcePath)
		fmt.Fprintf(w, "dependency_seed[%d].target: %s\n", index, seed.TargetPath)
	}
	for index, cache := range plan.Caches {
		prefix := fmt.Sprintf("cache[%d]", index)
		fmt.Fprintf(w, "%s.provider: %s\n", prefix, cache.Provider)
		fmt.Fprintf(w, "%s.key: %s\n", prefix, cache.Key)
		fmt.Fprintf(w, "%s.volume: %s\n", prefix, cache.Name)
		fmt.Fprintf(w, "%s.workspace: %s\n", prefix, cache.WorkspaceRoot)
		fmt.Fprintf(w, "%s.mount: %s\n", prefix, cache.MountPath)
		if cache.OutputPath != "" {
			fmt.Fprintf(w, "%s.output: %s\n", prefix, cache.OutputPath)
		}
		fmt.Fprintf(w, "%s.concurrency: %s\n", prefix, cache.Concurrency)
		paths, err := cacheExportPaths([]systemsandbox.CachePlan{cache}, options.SourceDir, options.Resolved.SyncOut, options.SyncOutAll)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "%s.sync_intersections: %s\n", prefix, strings.Join(paths, ","))
		if expanded {
			fmt.Fprintf(w, "%s.format: %s\n", prefix, cache.Format)
			fmt.Fprintf(w, "%s.platform: %s\n", prefix, cache.Platform)
			fmt.Fprintf(w, "%s.toolchain: %s\n", prefix, cache.Toolchain)
			fmt.Fprintf(w, "%s.abi: %s\n", prefix, cache.ABIKey)
			fmt.Fprintf(w, "%s.uid_gid: %d:%d\n", prefix, cache.UID, cache.GID)
		}
	}
	return nil
}

func validateExecutionMode(ctx context.Context, resolved recipe.Resolved, progress io.Writer) error {
	if resolved.SandboxMode != recipe.SandboxModeSystem {
		return nil
	}
	return fmt.Errorf("recipe %q cannot enter system mode from an existing host or workspace lifecycle; invoke it as a top-level recipe", resolved.Name)
}

type sandboxWorkspace struct {
	root               string
	source             string
	target             string
	workDir            string
	overlayDir         string
	upper              string
	work               string
	protectedWhiteouts map[string]struct{}
	overlay            bool
}

var newOverlayWorkspace = createOverlayWorkspace

func createSandboxWorkspace(ctx context.Context, source, workDir, workspace string, stderr io.Writer, verbose bool) (*sandboxWorkspace, error) {
	sandbox, err := newOverlayWorkspace(ctx, source, workDir, workspace)
	if err == nil {
		if verbose {
			fmt.Fprintf(stderr, "shadowtree: overlayfs %s -> %s\n", source, workspace)
		}
		return sandbox, nil
	}
	fmt.Fprintf(stderr, "shadowtree: overlayfs unavailable (%v); falling back to copied workspace\n", err)
	if verbose {
		fmt.Fprintf(stderr, "shadowtree: copying %s -> %s\n", source, workspace)
	}
	if err := CopyTree(source, workspace); err != nil {
		return nil, fmt.Errorf("copy workspace: %w", err)
	}
	return &sandboxWorkspace{root: workspace, source: source, workDir: workDir}, nil
}

func (sandbox *sandboxWorkspace) SyncRoot(paths []string) (string, func(), error) {
	cleanup := func() {}
	if !sandbox.overlay || len(paths) == 0 {
		return sandbox.root, cleanup, nil
	}
	root := filepath.Join(sandbox.workDir, "sync")
	if err := sandbox.materializePaths(root, paths); err != nil {
		return "", cleanup, fmt.Errorf("materialize workspace: %w", err)
	}
	return root, func() { _ = os.RemoveAll(root) }, nil
}

func (sandbox *sandboxWorkspace) SyncAll(source string) error {
	if !sandbox.overlay {
		return replaceDirContents(sandbox.root, source)
	}
	return applyOverlayUpper(sandbox.upper, source, sandbox.protectedWhiteouts)
}

func (sandbox *sandboxWorkspace) Close() error {
	return removeAll(sandbox.workDir)
}

func (sandbox *sandboxWorkspace) namespaceDir(dir string) string {
	if !sandbox.overlay {
		return dir
	}
	rel, ok := sandbox.relInsideRoot(dir)
	if !ok {
		return dir
	}
	return filepath.Join(sandbox.target, rel)
}

func (sandbox *sandboxWorkspace) relInsideRoot(dir string) (string, bool) {
	return relInside(sandbox.root, dir)
}

func relInside(root, dir string) (string, bool) {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

func (sandbox *sandboxWorkspace) materializePaths(dst string, paths []string) error {
	cleaned, err := cleanSyncOutPaths(paths)
	if err != nil {
		return err
	}
	if err := clearHostDir(dst); err != nil {
		return err
	}
	srcRoot, err := os.OpenRoot(sandbox.source)
	if err != nil {
		return err
	}
	defer srcRoot.Close()
	dstRoot, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer dstRoot.Close()
	excludedLower, err := overlayLowerExclusions(sandbox.upper, cleaned)
	if err != nil {
		return err
	}
	for _, name := range cleaned {
		if err := copySourcePathToRoot(srcRoot, name, dstRoot, excludedLower); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}
	return applyOverlayUpperPaths(sandbox.upper, dst, cleaned)
}

func runResolvedCommands(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, options Options, stdin io.Reader, stdout, stderr io.Writer, stack []string) error {
	var firstErr error
	preStatus := ""
	cmdStatus := ""
	for i, command := range options.Resolved.Recipe.Pre {
		if err := runStageCommand(ctx, sandbox, dir, env, command, stdin, stdout, stderr, options, phasePre, i, stack); err != nil {
			preStatus = statusValue(err)
			firstErr = err
			break
		}
	}
	if firstErr == nil {
		if len(options.Resolved.Recipe.Pre) > 0 {
			preStatus = "0"
		}
		mainOptions := options
		if recipe.CommandContainsStageStatusPlaceholder(options.Resolved.Main) {
			expandedMain, err := recipe.ExpandStageStatusPlaceholders(options.Resolved.Main, stageStatusValues(preStatus, cmdStatus))
			if err != nil {
				firstErr = fmt.Errorf("cmd: %w", err)
			} else {
				mainOptions.Resolved.Main = expandedMain
			}
		}
		if firstErr == nil {
			firstErr = runMainCommands(ctx, sandbox, dir, env, mainOptions, stdin, stdout, stderr, stack)
		}
		cmdStatus = statusValue(firstErr)
	}
	postCtx := ctx
	postStdin := stdin
	if ctx.Err() != nil {
		postCtx = context.WithoutCancel(ctx)
		postStdin = strings.NewReader("")
	}
	for i, command := range options.Resolved.Recipe.Post {
		if recipe.CommandContainsStageStatusPlaceholder(command.Cmd) {
			expanded, err := recipe.ExpandStageStatusPlaceholders(command.Cmd, stageStatusValues(preStatus, cmdStatus))
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("post[%d]: %w", i, err)
				}
				continue
			}
			command.Cmd = expanded
		}
		if err := runStageCommand(postCtx, sandbox, dir, env, command, postStdin, stdout, stderr, options, phasePost, i, stack); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func stageStatusValues(preStatus, cmdStatus string) map[string]string {
	return map[string]string{
		recipe.LogStagePre: preStatus,
		recipe.LogStageCmd: cmdStatus,
	}
}

func statusValue(err error) string {
	if err == nil {
		return "0"
	}
	var exitErr ExitError
	if errors.As(err, &exitErr) {
		return strconv.Itoa(exitErr.Code)
	}
	return "1"
}

func runStageCommand(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, command recipe.StageCommand, stdin io.Reader, stdout, stderr io.Writer, options Options, phase string, index int, stack []string) error {
	timeout := recipe.StageTimeout(command)
	if timeout <= 0 {
		return runCommand(ctx, sandbox, dir, env, command.Cmd, stdin, stdout, stderr, options, phase, index, stack)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := runCommand(runCtx, sandbox, dir, env, command.Cmd, stdin, stdout, stderr, options, phase, index, stack)
	if err != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s[%d] timed out after %s", logStageForPhase(phase), index, timeout)
	}
	return err
}

func runMainCommands(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, options Options, stdin io.Reader, stdout, stderr io.Writer, stack []string) error {
	if options.Resolved.TargetSource != "" {
		return runAggregateCommands(ctx, sandbox, dir, env, options, stdin, stdout, stderr, stack)
	}
	if len(options.Resolved.Recipe.ForEach) == 0 {
		workdir, err := recipeWorkdir(dir, options.Resolved.Recipe.Workdir)
		if err != nil {
			return fmt.Errorf("workdir: %w", err)
		}
		return runCommand(ctx, sandbox, workdir, env, options.Resolved.Main, stdin, stdout, stderr, options, phaseMain, 0, stack)
	}
	items, err := forEachItems(ctx, sandbox, dir, env, options, stderr, stack)
	if err != nil {
		return err
	}
	for index, item := range items {
		command, err := recipe.ExpandForEachCommand(options.Resolved.Main, item, index)
		if err != nil {
			return fmt.Errorf("for_each[%d] cmd: %w", index, err)
		}
		workdir := dir
		if options.Resolved.Recipe.Workdir != "" {
			expanded, err := recipe.ExpandForEachString(options.Resolved.Recipe.Workdir, item, index)
			if err != nil {
				return fmt.Errorf("for_each[%d] workdir: %w", index, err)
			}
			workdir, err = recipeWorkdir(dir, expanded)
			if err != nil {
				return fmt.Errorf("for_each[%d] workdir: %w", index, err)
			}
		}
		if err := runCommand(ctx, sandbox, workdir, env, command, stdin, stdout, stderr, options, phaseMain, index, stack); err != nil {
			return err
		}
	}
	return nil
}

func runAggregateCommands(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, options Options, stdin io.Reader, stdout, stderr io.Writer, stack []string) error {
	targets, err := aggregateTargets(ctx, sandbox, dir, env, options, stderr)
	if err != nil {
		return fmt.Errorf("discover %s: %w", options.Resolved.TargetDomain, err)
	}
	if len(targets) == 0 {
		return fmt.Errorf("recipe %q with --all found no %s", options.Resolved.Name, options.Resolved.TargetDomain)
	}
	if options.Verbose {
		fmt.Fprintf(stderr, "shadowtree: discovered %d execution batch(es) for %s\n", len(targets), options.Resolved.TargetDomain)
	}
	for index, target := range targets {
		item := recipe.ValueCandidate{Value: target.Value, Help: target.Label}
		command, err := recipe.ExpandForEachCommand(options.Resolved.Main, item, index)
		if err != nil {
			return fmt.Errorf("target[%d] cmd: %w", index, err)
		}
		workdir, err := recipeWorkdir(dir, target.Workdir)
		if err != nil {
			return fmt.Errorf("target[%d] workdir: %w", index, err)
		}
		if err := runCommand(ctx, sandbox, workdir, env, command, stdin, stdout, stderr, options, phaseMain, index, stack); err != nil {
			return fmt.Errorf("target %s: %w", target.Label, err)
		}
	}
	return nil
}

func aggregateTargets(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, options Options, stderr io.Writer) ([]recipe.ExecutionTarget, error) {
	if sandbox != nil && sandbox.overlay {
		return sandbox.runNamespaceExecutionTargets(ctx, env, sandbox.namespaceDir(dir), options.Resolved.TargetSource, options.Resolved.VariadicArgs, stderr)
	}
	return recipe.ResolveExecutionTargets(ctx, options.Resolved.TargetSource, dir, env, options.Resolved.VariadicArgs)
}

func forEachItems(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, options Options, stderr io.Writer, stack []string) ([]recipe.ValueCandidate, error) {
	command := options.Resolved.Recipe.ForEach
	usesFilesystem, ok, err := recipe.ValueBuiltinUsesFilesystem(command)
	if ok {
		if err != nil {
			return nil, err
		}
		builtinDir := dir
		cleanup := func() {}
		if usesFilesystem && (sandbox == nil || !sandbox.overlay) {
			builtinDir, cleanup, err = forEachBuiltinDir(sandbox, dir)
			if err != nil {
				return nil, err
			}
		}
		defer cleanup()
		values, err := builtinValues(ctx, sandbox, builtinDir, env, command, options, stderr)
		if err != nil {
			return nil, err
		}
		return values, nil
	}
	command = recipe.CommandWithRecipeReference(command, options.Resolved.Recipe.Shell, options.Resolved.Recipe.ShellPrelude)
	var stdout bytes.Buffer
	if err := runCommand(ctx, sandbox, dir, env, command, nil, &stdout, stderr, options, phaseForEach, 0, stack); err != nil {
		return nil, err
	}
	return recipe.ParseValueCandidates(stdout.String()), nil
}

func builtinValues(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, command recipe.Command, options Options, stderr io.Writer) ([]recipe.ValueCandidate, error) {
	if sandbox != nil && sandbox.overlay {
		return sandbox.runNamespaceValueBuiltinCommand(ctx, env, dir, command, stderr, options)
	}
	values, _, err := recipe.BuiltinValues(command, recipe.ValueBuiltinOptions{
		Context:  ctx,
		Dir:      dir,
		Recipe:   options.Resolved.Recipe,
		Recipes:  options.Recipes,
		EnumSets: options.EnumSets,
	})
	return values, err
}

func forEachBuiltinDir(sandbox *sandboxWorkspace, dir string) (string, func(), error) {
	cleanup := func() {}
	if sandbox == nil || !sandbox.overlay {
		return dir, cleanup, nil
	}
	return "", cleanup, errors.New("overlay filesystem builtins must run in namespace")
}

func recipeWorkdir(root, value string) (string, error) {
	value = filepath.Clean(filepath.FromSlash(value))
	if value == "." {
		return root, nil
	}
	if filepath.IsAbs(value) || !filepath.IsLocal(value) {
		return "", fmt.Errorf("must be relative to recipe workspace: %s", value)
	}
	return filepath.Join(root, value), nil
}

func runCommand(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, command recipe.Command, stdin io.Reader, stdout, stderr io.Writer, options Options, phase string, index int, stack []string) error {
	logBoundary := options.stageLog.logs(phase)
	if options.Verbose || logBoundary {
		boundary := stageBoundary(phase, index, resolvedHasMultipleCommands(options.Resolved), command)
		if options.Verbose {
			fmt.Fprintln(stderr, boundary)
		}
		if logBoundary {
			if err := options.stageLog.writeBoundary(boundary); err != nil {
				return err
			}
		}
	}
	stdout, stderr = options.stageLog.writers(phase, stdout, stderr)
	if _, ok := recipe.ParseRecipeReference(command); ok && options.Recipes != nil {
		return runRecipeReference(ctx, sandbox, dir, env, command, stdin, stdout, stderr, options, stack)
	}
	if recipe.IsScriptCommand(command) {
		if sandbox != nil && sandbox.overlay {
			return sandbox.runNamespaceScriptCommand(ctx, env, sandbox.namespaceDir(dir), command, stdin, stdout, stderr, options, stack)
		}
		return runScriptCommand(ctx, sandbox, dir, env, command, stdin, stdout, stderr, options, stack)
	}
	if sandbox != nil && sandbox.overlay {
		return sandbox.runNamespaceCommand(ctx, env, sandbox.namespaceDir(dir), command, stdin, stdout, stderr)
	}
	return runExternalCommand(ctx, dir, env, command, stdin, stdout, stderr)
}

func resolvedHasMultipleCommands(resolved recipe.Resolved) bool {
	return len(resolved.Recipe.ForEach) > 0 || resolved.TargetSource != ""
}

func stageBoundary(phase string, index int, hasForEach bool, command recipe.Command) string {
	return fmt.Sprintf("== %s: %s ==", stageBoundaryLabel(phase, index, hasForEach), stageBoundaryCommand(command))
}

func stageBoundaryLabel(phase string, index int, hasForEach bool) string {
	stage := logStageForPhase(phase)
	if phase != phaseMain || hasForEach {
		return fmt.Sprintf("%s[%d]", stage, index)
	}
	return stage
}

func stageBoundaryCommand(command recipe.Command) string {
	return truncateStageBoundaryCommand(recipe.CommandHelpText(command))
}

func truncateStageBoundaryCommand(text string) string {
	if len(text) <= stageBoundaryCommandMax {
		return text
	}

	cutoffRunes := stageBoundaryCommandMax - len("...")
	cutoffByte := len(text)
	count := 0
	for i := range text {
		if count == cutoffRunes {
			cutoffByte = i
		}
		count++
		if count > stageBoundaryCommandMax {
			return text[:cutoffByte] + "..."
		}
	}
	return text
}

func runExternalCommand(ctx context.Context, dir string, env []string, command recipe.Command, stdin io.Reader, stdout, stderr io.Writer) error {
	executable, err := lookPathEnv(command[0], env, dir)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, executable, command[1:]...)
	cmd.Args[0] = command[0]
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	configureCommandCancellation(cmd)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ExitError{Code: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}

func lookPathEnv(file string, env []string, dir string) (string, error) {
	if commandHasPathSeparator(file) {
		return file, nil
	}
	path, ok := envValue(env, "PATH")
	if !ok {
		if env == nil {
			return exec.LookPath(file)
		}
		return "", fmt.Errorf("%s: %w", file, exec.ErrNotFound)
	}
	names := executableNames(file, env)
	for _, pathDir := range filepath.SplitList(path) {
		for _, name := range names {
			execPath := filepath.Join(pathDir, name)
			statPath := execPath
			if pathDir == "" {
				execPath = "." + string(filepath.Separator) + name
				statPath = filepath.Join(dir, name)
			} else if !filepath.IsAbs(pathDir) {
				statPath = filepath.Join(dir, pathDir, name)
			}
			info, err := os.Stat(statPath)
			if err != nil || info.IsDir() || !isExecutable(info) {
				continue
			}
			return execPath, nil
		}
	}
	return "", fmt.Errorf("%s: %w", file, exec.ErrNotFound)
}

func commandHasPathSeparator(file string) bool {
	if runtime.GOOS == "windows" {
		return strings.ContainsAny(file, `/\`)
	}
	return strings.Contains(file, "/")
}

func executableNames(file string, env []string) []string {
	if runtime.GOOS != "windows" || filepath.Ext(file) != "" {
		return []string{file}
	}
	pathext, ok := envValue(env, "PATHEXT")
	if !ok || pathext == "" {
		pathext = ".COM;.EXE;.BAT;.CMD"
	}
	names := []string{file}
	for ext := range strings.SplitSeq(pathext, ";") {
		if ext == "" {
			continue
		}
		if ext[0] != '.' {
			ext = "." + ext
		}
		names = append(names, file+ext)
	}
	return names
}

func isExecutable(info os.FileInfo) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

func runRecipeReference(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, command recipe.Command, stdin io.Reader, stdout, stderr io.Writer, options Options, stack []string) error {
	ref, _ := recipe.ParseRecipeReference(command)
	if ref.Path != "" {
		return runCrossConfigRecipeReference(ctx, sandbox, env, ref, stdin, stdout, stderr, options, stack)
	}
	key := recipeReferenceStackKey(options.Resolved.ConfigPath, ref.Name)
	if slices.Contains(stack, key) {
		cycle := append(slices.Clone(stack), key)
		return fmt.Errorf("recipe reference cycle: %s", strings.Join(cycle, " -> "))
	}
	rec, ok := options.Recipes[ref.Name]
	if !ok {
		return fmt.Errorf("unknown recipe reference: @%s", ref.Name)
	}
	resolved, err := recipe.ResolveWithOptions(ref.Name, rec, ref.Args, nil, options.ConfigEnv, options.Resolved.ConfigPath, options.Resolved.Profile, recipe.ResolveOptions{RunID: options.Resolved.RunID, Recipes: options.Recipes, EnumSets: options.EnumSets})
	if err != nil {
		return err
	}
	if !options.systemLifecycle {
		if err := validateExecutionMode(ctx, resolved, stderr); err != nil {
			return err
		}
	}
	nested := options
	nested.Resolved = resolved
	nested.SyncOutAll = false
	nested.PrintOnly = false
	nested.stageLog = nil
	nestedEnv := mergedEnv(env, resolved.GlobalEnv, resolved.Recipe.Env)
	if err := checkRecipeRequirements(resolved, dir, nestedEnv, stderr); err != nil {
		return err
	}
	return runResolvedCommands(ctx, sandbox, dir, nestedEnv, nested, stdin, stdout, stderr, append(slices.Clone(stack), key))
}

func runCrossConfigRecipeReference(ctx context.Context, sandbox *sandboxWorkspace, env []string, ref recipe.RecipeReferenceTarget, stdin io.Reader, stdout, stderr io.Writer, options Options, stack []string) error {
	target, err := configfile.ResolveCrossConfigReference(ctx, ref.Path, options.Resolved.ConfigPath, options.SourceDir, configfile.ResolveOptions{EvalDynamicVars: true})
	if err != nil {
		return fmt.Errorf("@%s: %w", ref.Target(), err)
	}
	key := recipeReferenceStackKey(target.Loaded.Path, ref.Name)
	if slices.Contains(stack, key) {
		cycle := append(slices.Clone(stack), key)
		return fmt.Errorf("recipe reference cycle: %s", strings.Join(cycle, " -> "))
	}
	rec, ok := target.Recipes[ref.Name]
	if !ok {
		return fmt.Errorf("unknown recipe reference: @%s", ref.Target())
	}
	resolved, err := recipe.ResolveWithOptions(ref.Name, rec, ref.Args, nil, target.Loaded.Config.Env, target.Loaded.Path, target.Profile, recipe.ResolveOptions{RunID: options.Resolved.RunID, Recipes: target.Recipes, EnumSets: target.Loaded.Config.EnumSets})
	if err != nil {
		return err
	}
	if !options.systemLifecycle {
		if err := validateExecutionMode(ctx, resolved, stderr); err != nil {
			return err
		}
	}
	nested := options
	nested.Resolved = resolved
	nested.Recipes = target.Recipes
	nested.EnumSets = target.Loaded.Config.EnumSets
	nested.ConfigEnv = target.Loaded.Config.Env
	nested.SyncOutAll = false
	nested.PrintOnly = false
	nested.stageLog = nil
	nestedEnv := mergedEnv(env, resolved.GlobalEnv, resolved.Recipe.Env)
	targetDir := targetExecutionDir(sandbox, target.SourceDir, target.Dir)
	if err := checkRecipeRequirements(resolved, targetDir, nestedEnv, stderr); err != nil {
		return err
	}
	return runResolvedCommands(ctx, sandbox, targetDir, nestedEnv, nested, stdin, stdout, stderr, append(slices.Clone(stack), key))
}

func CommandOutput(ctx context.Context, dir string, env map[string]string, command recipe.Command, opts CommandOutputOptions) (string, error) {
	if recipe.IsScriptCommand(command) && opts.Recipes != nil {
		var stdout bytes.Buffer
		err := runScriptCommand(ctx, nil, dir, mergedEnv(os.Environ(), env), command, nil, &stdout, io.Discard, Options{
			Resolved:  recipe.Resolved{ConfigPath: opts.ConfigPath},
			Recipes:   opts.Recipes,
			ConfigEnv: env,
			SourceDir: cmp.Or(opts.SourceDir, dir),
		}, nil)
		if err != nil {
			return "", err
		}
		return stdout.String(), nil
	}
	ref, ok := recipe.ParseRecipeReference(command)
	if !ok || opts.Recipes == nil {
		return recipe.CommandOutput(ctx, dir, env, command)
	}
	if ref.Path != "" {
		return crossConfigCommandOutput(ctx, dir, env, ref, opts)
	}
	rec, ok := opts.Recipes[ref.Name]
	if !ok {
		return "", fmt.Errorf("unknown recipe reference: @%s", ref.Name)
	}
	resolved, err := recipe.ResolveWithOptions(ref.Name, rec, ref.Args, nil, env, opts.ConfigPath, "", recipe.ResolveOptions{Recipes: opts.Recipes, EnumSets: opts.EnumSets})
	if err != nil {
		return "", err
	}
	if err := validateExecutionMode(ctx, resolved, io.Discard); err != nil {
		return "", err
	}
	var stdout bytes.Buffer
	envList := mergedEnv(os.Environ(), resolved.GlobalEnv, resolved.Recipe.Env)
	if err := checkRecipeRequirements(resolved, dir, envList, io.Discard); err != nil {
		return "", err
	}
	err = runResolvedCommands(ctx, nil, dir, envList, Options{
		Resolved:  resolved,
		Recipes:   opts.Recipes,
		EnumSets:  opts.EnumSets,
		ConfigEnv: env,
		SourceDir: cmp.Or(opts.SourceDir, dir),
	}, nil, &stdout, io.Discard, []string{recipeReferenceStackKey(opts.ConfigPath, ref.Name)})
	if err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func crossConfigCommandOutput(ctx context.Context, dir string, env map[string]string, ref recipe.RecipeReferenceTarget, opts CommandOutputOptions) (string, error) {
	sourceDir := cmp.Or(opts.SourceDir, dir)
	target, err := configfile.ResolveCrossConfigReference(ctx, ref.Path, opts.ConfigPath, sourceDir, configfile.ResolveOptions{EvalDynamicVars: true})
	if err != nil {
		return "", fmt.Errorf("@%s: %w", ref.Target(), err)
	}
	rec, ok := target.Recipes[ref.Name]
	if !ok {
		return "", fmt.Errorf("unknown recipe reference: @%s", ref.Target())
	}
	resolved, err := recipe.ResolveWithOptions(ref.Name, rec, ref.Args, nil, target.Loaded.Config.Env, target.Loaded.Path, target.Profile, recipe.ResolveOptions{Recipes: target.Recipes, EnumSets: target.Loaded.Config.EnumSets})
	if err != nil {
		return "", err
	}
	if err := validateExecutionMode(ctx, resolved, io.Discard); err != nil {
		return "", err
	}
	var stdout bytes.Buffer
	envList := mergedEnv(os.Environ(), env, resolved.GlobalEnv, resolved.Recipe.Env)
	if err := checkRecipeRequirements(resolved, target.Dir, envList, io.Discard); err != nil {
		return "", err
	}
	err = runResolvedCommands(ctx, nil, target.Dir, envList, Options{
		Resolved:  resolved,
		Recipes:   target.Recipes,
		EnumSets:  target.Loaded.Config.EnumSets,
		ConfigEnv: target.Loaded.Config.Env,
		SourceDir: sourceDir,
	}, nil, &stdout, io.Discard, []string{recipeReferenceStackKey(target.Loaded.Path, ref.Name)})
	if err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func targetExecutionDir(sandbox *sandboxWorkspace, sourceDir, targetDir string) string {
	if sandbox == nil {
		return targetDir
	}
	rel, err := filepath.Rel(sourceDir, targetDir)
	if err != nil {
		return targetDir
	}
	if sandbox.overlay {
		return filepath.Join(sandbox.target, rel)
	}
	return filepath.Join(sandbox.root, rel)
}

func mirrorRecipeLogToSandbox(logPath, source string, sandbox *sandboxWorkspace) error {
	if logPath == "" || sandbox == nil {
		return nil
	}
	rel, ok := relInside(source, logPath)
	if !ok {
		return nil
	}
	name := filepath.ToSlash(rel)
	info, err := os.Stat(logPath)
	if err != nil {
		return fmt.Errorf("stat log: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("log is not a regular file: %s", logPath)
	}
	if sandbox.overlay {
		root, err := os.OpenRoot(sandbox.upper)
		if err != nil {
			return err
		}
		defer root.Close()
		return copyRegularFileToRoot(root, name, logPath, info.Mode().Perm())
	}
	return copyRegularFile(logPath, filepath.Join(sandbox.root, filepath.FromSlash(name)), info.Mode().Perm())
}

func recipeReferenceStackKey(configPath, name string) string {
	if configPath == "" {
		return name
	}
	return configPath + ":" + name
}

func printPlan(w io.Writer, resolved recipe.Resolved) {
	fmt.Fprintf(w, "recipe: %s\n", resolved.Name)
	if resolved.Scope != "" {
		fmt.Fprintf(w, "scope: %s\n", resolved.Scope)
		fmt.Fprintf(w, "target_domain: %s\n", resolved.TargetDomain)
		fmt.Fprintf(w, "target_source: %s\n", resolved.TargetSource)
	}
	if resolved.Profile != "" {
		fmt.Fprintf(w, "profile: %s\n", resolved.Profile)
	}
	if resolved.ConfigPath != "" {
		fmt.Fprintf(w, "config: %s\n", resolved.ConfigPath)
	}
	if resolved.LogPath != "" {
		fmt.Fprintf(w, "log: %s\n", resolved.LogPath)
		fmt.Fprintf(w, "log_stages: %s\n", strings.Join(resolved.LogStages, ","))
		fmt.Fprintf(w, "log_tee: %t\n", resolved.LogTee)
	}
	if resolved.SandboxMode != recipe.SandboxModeWorkspace {
		fmt.Fprintf(w, "sandboxed: %s\n", resolved.SandboxMode)
		if resolved.SandboxMode == recipe.SandboxModeSystem {
			fmt.Fprintln(w, "runtime: <not probed>")
		}
	}
	printPlanRequirements(w, resolved.Recipe.Requires)
	for i, command := range resolved.Recipe.Pre {
		fmt.Fprintf(w, "pre[%d]: %s\n", i, recipe.StageCommandHelpText(command))
	}
	if len(resolved.Recipe.ForEach) > 0 {
		fmt.Fprintf(w, "for_each: %s\n", recipe.CommandHelpText(resolved.Recipe.ForEach))
	}
	if resolved.Recipe.Workdir != "" {
		fmt.Fprintf(w, "workdir: %s\n", resolved.Recipe.Workdir)
	}
	fmt.Fprintf(w, "main: %s\n", recipe.CommandHelpText(resolved.Main))
	for i, command := range resolved.Recipe.Post {
		fmt.Fprintf(w, "post[%d]: %s\n", i, recipe.StageCommandHelpText(command))
	}
	for _, path := range resolved.SyncOut {
		fmt.Fprintf(w, "sync_out: %s\n", path)
	}
}

func printExpandedPlan(w io.Writer, resolved recipe.Resolved) {
	fmt.Fprintf(w, "recipe: %s\n", resolved.Name)
	printPlanValue(w, "scope", string(resolved.Scope))
	printPlanValue(w, "target_domain", resolved.TargetDomain)
	printPlanValue(w, "target_source", string(resolved.TargetSource))
	printPlanValue(w, "config", resolved.ConfigPath)
	printPlanValue(w, "profile", resolved.Profile)
	fmt.Fprintf(w, "sandboxed: %s\n", resolved.SandboxMode)
	if resolved.SandboxMode == recipe.SandboxModeSystem {
		fmt.Fprintln(w, "runtime: <not probed>")
	}
	printPlanValue(w, "workdir", cmp.Or(resolved.Recipe.Workdir, "."))
	if len(resolved.SyncOut) == 0 {
		fmt.Fprintln(w, "sync_out: <none>")
	} else {
		for _, path := range resolved.SyncOut {
			fmt.Fprintf(w, "sync_out: %s\n", path)
		}
	}
	if resolved.LogPath == "" {
		fmt.Fprintln(w, "log: <none>")
	} else {
		fmt.Fprintf(w, "log: %s\n", resolved.LogPath)
		fmt.Fprintf(w, "log_stages: %s\n", strings.Join(resolved.LogStages, ","))
		fmt.Fprintf(w, "log_tee: %t\n", resolved.LogTee)
	}
	printPlanRequirements(w, resolved.Recipe.Requires)
	if resolved.Preset != "" {
		fmt.Fprintf(w, "preset: %s\n", resolved.Preset)
	}
	printStringMapSection(w, "arguments", resolved.Arguments)
	printArgumentValueCommands(w, resolved.Recipe.Arguments)
	if len(resolved.VariadicArgs) > 0 {
		fmt.Fprintf(w, "variadic_args: %s\n", strings.Join(resolved.VariadicArgs, " "))
	}
	printStringMapSection(w, "vars", resolved.Recipe.Vars)
	printStringMapSection(w, "env", resolved.Recipe.Env)
	if forEach := expandedForEachCommand(resolved); len(forEach) > 0 {
		printExpandedCommand(w, "for_each", forEach)
	}
	for i, command := range resolved.Recipe.Pre {
		printExpandedStageCommand(w, fmt.Sprintf("pre[%d]", i), command)
	}
	printExpandedCommand(w, "main", resolved.Main)
	for i, command := range resolved.Recipe.Post {
		printExpandedStageCommand(w, fmt.Sprintf("post[%d]", i), command)
	}
}

func printPlanValue(w io.Writer, key, value string) {
	if value == "" {
		value = "<none>"
	}
	fmt.Fprintf(w, "%s: %s\n", key, value)
}

func printStringMapSection(w io.Writer, name string, values map[string]string) {
	fmt.Fprintf(w, "%s:\n", name)
	if len(values) == 0 {
		fmt.Fprintln(w, "  <none>")
		return
	}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		fmt.Fprintf(w, "  %s: %s\n", key, values[key])
	}
}

func printArgumentValueCommands(w io.Writer, args map[string]recipe.Argument) {
	var names []string
	for name, arg := range args {
		if len(arg.Values) > 0 {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	slices.Sort(names)
	fmt.Fprintln(w, "argument_values:")
	for _, name := range names {
		fmt.Fprintf(w, "  %s: %s\n", name, recipe.CommandHelpText(args[name].Values))
	}
}

func printExpandedStageCommand(w io.Writer, label string, command recipe.StageCommand) {
	printExpandedCommand(w, label, command.Cmd)
	if timeout := recipe.StageTimeout(command); timeout > 0 {
		fmt.Fprintf(w, "%s.timeout: %s\n", label, timeout)
	}
}

func printExpandedCommand(w io.Writer, label string, command recipe.Command) {
	if recipe.IsScriptCommand(command) {
		if shell := recipe.ScriptShell(command); shell != "" {
			fmt.Fprintf(w, "%s.shell: %s\n", label, shell)
		}
		fmt.Fprintf(w, "%s.script: |\n", label)
		printIndentedBlock(w, recipe.ScriptBody(command), "  ")
		return
	}
	fmt.Fprintf(w, "%s: %s\n", label, recipe.CommandHelpText(command))
}

func printIndentedBlock(w io.Writer, text, indent string) {
	text = strings.TrimRight(text, "\r\n")
	if text == "" {
		fmt.Fprintf(w, "%s<empty>\n", indent)
		return
	}
	for line := range strings.Lines(text) {
		fmt.Fprintf(w, "%s%s", indent, line)
		if !strings.HasSuffix(line, "\n") {
			fmt.Fprintln(w)
		}
	}
}

func printPlanRequirements(w io.Writer, req recipe.Requirements) {
	if req.Empty() {
		return
	}
	if len(req.Commands) > 0 {
		fmt.Fprintf(w, "requires.commands: %s\n", strings.Join(req.Commands, ", "))
	}
	if len(req.OptionalCommands) > 0 {
		fmt.Fprintf(w, "requires.optional_commands: %s\n", strings.Join(req.OptionalCommands, ", "))
	}
	if len(req.GoCommands) > 0 {
		fmt.Fprintf(w, "requires.go_commands: %s\n", requirementMapText(req.GoCommands))
	}
	if len(req.NodeCommands) > 0 {
		fmt.Fprintf(w, "requires.node_commands: %s\n", requirementMapText(req.NodeCommands))
	}
}

func requirementMapText(values map[string]string) string {
	parts := make([]string, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, ", ")
}

func CopyTree(srcRoot, dstRoot string) error {
	return filepath.WalkDir(srcRoot, func(src string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcRoot, src)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dstRoot, 0o755)
		}
		if shouldSkip(rel, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dst := filepath.Join(dstRoot, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			return os.MkdirAll(dst, mode.Perm())
		case mode.Type() == 0:
			return copyRegularFile(src, dst, mode.Perm())
		case mode.Type() == os.ModeSymlink:
			target, err := os.Readlink(src)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		default:
			return nil
		}
	})
}

func cleanSyncOutPath(requested string) (string, error) {
	cleaned := filepath.Clean(requested)
	if cleaned == "." || filepath.IsAbs(cleaned) || !filepath.IsLocal(cleaned) {
		return "", fmt.Errorf("sync_out path must stay under workspace: %s", requested)
	}
	return filepath.ToSlash(cleaned), nil
}

func cleanSyncOutPaths(paths []string) ([]string, error) {
	cleaned := make([]string, 0, len(paths))
	for _, path := range paths {
		name, err := cleanSyncOutPath(path)
		if err != nil {
			return nil, err
		}
		cleaned = append(cleaned, name)
	}
	return cleaned, nil
}

type overlayLowerExclude struct {
	replaced       map[string]struct{}
	hiddenChildren map[string]struct{}
}

func newOverlayLowerExclude() *overlayLowerExclude {
	return &overlayLowerExclude{
		replaced:       map[string]struct{}{},
		hiddenChildren: map[string]struct{}{},
	}
}

func (excluded *overlayLowerExclude) replace(name string) {
	excluded.replaced[name] = struct{}{}
}

func (excluded *overlayLowerExclude) hideChildren(name string) {
	excluded.hiddenChildren[name] = struct{}{}
}

func (excluded *overlayLowerExclude) contains(name string) bool {
	if excluded == nil {
		return false
	}
	for hidden := range excluded.replaced {
		if sameOrDescendant(name, hidden) {
			return true
		}
	}
	for hidden := range excluded.hiddenChildren {
		if name != hidden && sameOrDescendant(name, hidden) {
			return true
		}
	}
	return false
}

func overlayLowerExclusions(upperRoot string, paths []string) (*overlayLowerExclude, error) {
	excluded := newOverlayLowerExclude()
	if upperRoot == "" {
		return excluded, nil
	}
	err := walkSelectedUpperEntries(upperRoot, paths, func(name, path string, info fs.FileInfo) error {
		if isOverlayWhiteout(path, info) {
			excluded.replace(name)
			return nil
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			if isOverlayOpaqueDir(path) {
				excluded.hideChildren(name)
			}
		case mode.Type() == 0 || mode.Type() == os.ModeSymlink:
			excluded.replace(name)
		default:
			excluded.replace(name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return excluded, nil
}

func copySourcePathToRoot(srcRoot *os.Root, name string, dstRoot *os.Root, excluded *overlayLowerExclude) error {
	if excluded.contains(name) {
		return nil
	}
	info, err := srcRoot.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if shouldSkip(name, fileInfoDirEntry{info: info}) {
		return nil
	}
	_, err = copyRootPathToRoot(srcRoot, name, info, dstRoot, name, excluded)
	return err
}

func SyncPath(workspace, source, requested string) error {
	cleaned, err := cleanSyncOutPath(requested)
	if err != nil {
		return err
	}
	return syncPathAs(workspace, source, cleaned, cleaned)
}

func syncPathAs(workspace, source, sourceName, destinationName string) error {
	destinationName, err := cleanSyncOutPath(destinationName)
	if err != nil {
		return err
	}
	if sourceName == "." {
		dstRoot, err := os.OpenRoot(source)
		if err != nil {
			return err
		}
		defer dstRoot.Close()
		if err := removeRootPath(dstRoot, destinationName); err != nil {
			return err
		}
		return copyTreeToRoot(workspace, dstRoot, destinationName)
	}
	sourceName, err = cleanSyncOutPath(sourceName)
	if err != nil {
		return err
	}
	srcRoot, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer srcRoot.Close()
	info, statErr := srcRoot.Lstat(sourceName)
	dstRoot, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer dstRoot.Close()
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return removeRootPathIfPresent(dstRoot, destinationName)
		}
		return statErr
	}
	if info.IsDir() && shouldSkip(sourceName, fileInfoDirEntry{info: info}) {
		return removeRootPath(dstRoot, destinationName)
	}
	if info.IsDir() {
		if err := removeRootPath(dstRoot, destinationName); err != nil {
			return err
		}
	}
	supported, err := copyRootPathToRoot(srcRoot, sourceName, info, dstRoot, destinationName, nil)
	if err != nil {
		return err
	}
	if !supported {
		return fmt.Errorf("unsupported sync path file type: %s", sourceName)
	}
	return nil
}

func replaceDirContents(src, dst string) error {
	dstRoot, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer dstRoot.Close()
	entries, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if shouldSkip(entry.Name(), entry) {
			continue
		}
		if err := dstRoot.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	return copyTreeToRoot(src, dstRoot, ".")
}

func applyOverlayUpper(upperRoot, dstRoot string, excluded map[string]struct{}) error {
	return applyOverlayUpperFiltered(upperRoot, dstRoot, excluded, func(string) bool { return true })
}

func applyOverlayUpperPaths(upperRoot, dstRoot string, paths []string) error {
	if upperRoot == "" {
		return nil
	}
	dst, err := os.OpenRoot(dstRoot)
	if err != nil {
		return err
	}
	defer dst.Close()
	return walkSelectedUpperEntries(upperRoot, paths, func(name, path string, info fs.FileInfo) error {
		return applyOverlayUpperEntry(dst, name, path, info)
	})
}

func applyOverlayUpperFiltered(upperRoot, dstRoot string, excluded map[string]struct{}, include func(string) bool) error {
	if upperRoot == "" {
		return nil
	}
	dst, err := os.OpenRoot(dstRoot)
	if err != nil {
		return err
	}
	defer dst.Close()
	return filepath.WalkDir(upperRoot, func(src string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(upperRoot, src)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("overlay path escapes root: %s", rel)
		}
		if !include(name) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if isExcludedPath(name, excluded) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(src)
		if err != nil {
			return err
		}
		return applyOverlayUpperEntry(dst, name, src, info)
	})
}

func walkSelectedUpperEntries(upperRoot string, selected []string, visit func(name, path string, info fs.FileInfo) error) error {
	seen := map[string]struct{}{}
	for _, selectedName := range selected {
		prefixes := pathPrefixes(selectedName)
		for index, name := range prefixes {
			path := filepath.Join(upperRoot, filepath.FromSlash(name))
			info, err := os.Lstat(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
					break
				}
				return err
			}
			if _, ok := seen[name]; !ok {
				if err := visit(name, path, info); err != nil {
					return err
				}
				seen[name] = struct{}{}
			}
			if isOverlayWhiteout(path, info) || !info.IsDir() {
				break
			}
			if index == len(prefixes)-1 {
				if err := walkUpperSubtree(path, name, seen, visit); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func walkUpperSubtree(rootPath, rootName string, seen map[string]struct{}, visit func(name, path string, info fs.FileInfo) error) error {
	return filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := pathJoinSlash(rootName, rel)
		if _, ok := seen[name]; ok {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if err := visit(name, path, info); err != nil {
			return err
		}
		seen[name] = struct{}{}
		return nil
	})
}

func pathPrefixes(name string) []string {
	parts := strings.Split(filepath.ToSlash(name), "/")
	prefixes := make([]string, 0, len(parts))
	current := ""
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		prefixes = append(prefixes, current)
	}
	return prefixes
}

func applyOverlayUpperEntry(dst *os.Root, name, src string, info fs.FileInfo) error {
	if isOverlayWhiteout(src, info) {
		return removeRootPath(dst, name)
	}
	mode := info.Mode()
	switch {
	case mode.IsDir():
		if err := mkdirAllRootReplacingLeaf(dst, name, mode.Perm()); err != nil {
			return err
		}
		if err := dst.Chmod(name, mode.Perm()); err != nil {
			return err
		}
		if isOverlayOpaqueDir(src) {
			if err := clearRootDir(dst, name); err != nil {
				return err
			}
		}
		return nil
	case mode.Type() == os.ModeSymlink:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := removeRootPath(dst, name); err != nil {
			return err
		}
		return dst.Symlink(target, name)
	case mode.Type() == 0:
		if err := removeRootPath(dst, name); err != nil {
			return err
		}
		return copyRegularFileToRoot(dst, name, src, mode.Perm())
	default:
		return removeRootPath(dst, name)
	}
}

func copyRootPathToRoot(srcRoot *os.Root, srcName string, info fs.FileInfo, dstRoot *os.Root, dstName string, excluded *overlayLowerExclude) (bool, error) {
	mode := info.Mode()
	switch {
	case mode.IsDir():
		return true, copyRootTreeToRoot(srcRoot, srcName, info, dstRoot, dstName, excluded)
	case mode.Type() == 0:
		if err := removeRootPath(dstRoot, dstName); err != nil {
			return true, err
		}
		return true, copyRootRegularFileToRoot(srcRoot, srcName, dstRoot, dstName, mode.Perm())
	case mode.Type() == os.ModeSymlink:
		target, err := srcRoot.Readlink(srcName)
		if err != nil {
			return true, err
		}
		if err := ensureRootParent(dstRoot, dstName); err != nil {
			return true, err
		}
		if err := removeRootPath(dstRoot, dstName); err != nil {
			return true, err
		}
		return true, dstRoot.Symlink(target, dstName)
	default:
		return false, nil
	}
}

func copyRootTreeToRoot(srcRoot *os.Root, srcName string, info fs.FileInfo, dstRoot *os.Root, dstName string, excluded *overlayLowerExclude) error {
	if err := mkdirAllRootReplacingLeaf(dstRoot, dstName, info.Mode().Perm()); err != nil {
		return err
	}
	if err := dstRoot.Chmod(dstName, info.Mode().Perm()); err != nil {
		return err
	}
	dir, err := srcRoot.Open(srcName)
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		childSrc := pathJoinSlash(srcName, entry.Name())
		if excluded.contains(childSrc) || shouldSkip(childSrc, entry) {
			continue
		}
		childInfo, err := entry.Info()
		if err != nil {
			return err
		}
		childDst := pathJoinSlash(dstName, entry.Name())
		if _, err := copyRootPathToRoot(srcRoot, childSrc, childInfo, dstRoot, childDst, excluded); err != nil {
			return err
		}
	}
	return nil
}

func copyRootRegularFileToRoot(srcRoot *os.Root, srcName string, dstRoot *os.Root, dstName string, perm os.FileMode) error {
	if err := ensureRootParent(dstRoot, dstName); err != nil {
		return err
	}
	in, err := srcRoot.Open(srcName)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := dstRoot.OpenFile(dstName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	chmodErr := out.Chmod(perm)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if chmodErr != nil {
		return chmodErr
	}
	return closeErr
}

type fileInfoDirEntry struct {
	info fs.FileInfo
}

func (entry fileInfoDirEntry) Name() string {
	return entry.info.Name()
}

func (entry fileInfoDirEntry) IsDir() bool {
	return entry.info.IsDir()
}

func (entry fileInfoDirEntry) Type() fs.FileMode {
	return entry.info.Mode().Type()
}

func (entry fileInfoDirEntry) Info() (fs.FileInfo, error) {
	return entry.info, nil
}

func copyTreeToRoot(srcRoot string, dstRoot *os.Root, dstName string) error {
	return filepath.WalkDir(srcRoot, func(src string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcRoot, src)
		if err != nil {
			return err
		}
		if shouldSkip(rel, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := filepath.ToSlash(filepath.Join(dstName, rel))
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if rel == "." {
			if dstName == "." {
				return nil
			}
			name := filepath.ToSlash(dstName)
			if err := mkdirAllRootReplacingLeaf(dstRoot, name, mode.Perm()); err != nil {
				return err
			}
			return dstRoot.Chmod(name, mode.Perm())
		}
		switch {
		case mode.IsDir():
			if err := mkdirAllRootReplacingLeaf(dstRoot, name, mode.Perm()); err != nil {
				return err
			}
			return dstRoot.Chmod(name, mode.Perm())
		case mode.Type() == 0:
			if err := removeRootPath(dstRoot, name); err != nil {
				return err
			}
			return copyRegularFileToRoot(dstRoot, name, src, mode.Perm())
		case mode.Type() == os.ModeSymlink:
			target, err := os.Readlink(src)
			if err != nil {
				return err
			}
			if err := removeRootPath(dstRoot, name); err != nil {
				return err
			}
			return dstRoot.Symlink(target, name)
		default:
			return nil
		}
	})
}

func copyRegularFileToRoot(root *os.Root, name, src string, perm os.FileMode) error {
	if err := ensureRootParent(root, name); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	chmodErr := out.Chmod(perm)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if chmodErr != nil {
		return chmodErr
	}
	return closeErr
}

func mkdirAllRootReplacingLeaf(root *os.Root, name string, perm os.FileMode) error {
	if err := ensureRootParent(root, name); err != nil {
		return err
	}
	if info, err := root.Lstat(name); err == nil && info.Mode().Type() == os.ModeSymlink {
		if err := root.RemoveAll(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := root.MkdirAll(name, perm); err != nil {
		if removeErr := root.RemoveAll(name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
		return root.MkdirAll(name, perm)
	}
	return nil
}

func ensureRootParent(root *os.Root, name string) error {
	dir := pathDirSlash(name)
	if dir == "." {
		return nil
	}
	current := ""
	for part := range strings.SplitSeq(dir, "/") {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		info, err := root.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := root.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		case err != nil:
			return err
		case !info.IsDir() || info.Mode().Type() == os.ModeSymlink:
			if err := root.RemoveAll(current); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := root.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		}
	}
	return nil
}

func removeRootPath(root *os.Root, name string) error {
	if err := ensureRootParent(root, name); err != nil {
		return err
	}
	if err := root.RemoveAll(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeRootPathIfPresent(root *os.Root, name string) error {
	dir := pathDirSlash(name)
	if dir != "." {
		current := ""
		for part := range strings.SplitSeq(dir, "/") {
			if part == "" || part == "." {
				continue
			}
			if current == "" {
				current = part
			} else {
				current += "/" + part
			}
			info, err := root.Lstat(current)
			switch {
			case errors.Is(err, os.ErrNotExist):
				return nil
			case err != nil:
				return err
			case info.Mode().Type() == os.ModeSymlink:
				return removeRootPath(root, name)
			case !info.IsDir():
				return nil
			}
		}
	}
	if err := root.RemoveAll(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func clearHostDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.MkdirAll(dir, 0o755)
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func removeAll(path string) error {
	var err error
	for range 3 {
		err = os.RemoveAll(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		_ = chmodTreeDirs(path)
	}
	return err
}

func chmodTreeDirs(path string) error {
	return filepath.WalkDir(path, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrPermission) {
				_ = os.Chmod(name, 0o700)
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return os.Chmod(name, 0o700)
		}
		return nil
	})
}

func clearRootDir(root *os.Root, dirName string) error {
	dir, err := root.Open(dirName)
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		name := filepath.ToSlash(filepath.Join(dirName, entry.Name()))
		if err := root.RemoveAll(name); err != nil {
			return err
		}
	}
	return nil
}

func pathJoinSlash(elem ...string) string {
	return filepath.ToSlash(filepath.Join(elem...))
}

func pathDirSlash(name string) string {
	dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name)))
	if dir == "." {
		return "."
	}
	return dir
}

func isExcludedPath(name string, excluded map[string]struct{}) bool {
	if len(excluded) == 0 {
		return false
	}
	name = filepath.ToSlash(name)
	for excludedName := range excluded {
		if sameOrDescendant(name, excludedName) {
			return true
		}
	}
	return false
}

func sameOrDescendant(name, parent string) bool {
	return name == parent || strings.HasPrefix(name, parent+"/")
}

func copyRegularFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	err := reflinkRegularFile(dst, src, perm)
	if err == nil {
		return nil
	}
	if !isReflinkUnsupported(err) {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func shouldSkip(rel string, entry fs.DirEntry) bool {
	name := entry.Name()
	if name == ".git" || name == ".shadowtree" {
		return true
	}
	if strings.HasPrefix(name, ".shadowtree.") {
		return true
	}
	return rel == ".shadowtree"
}

func mergedEnv(base []string, overlays ...map[string]string) []string {
	env := envListMap(base)
	for _, overlay := range overlays {
		maps.Copy(env, overlay)
	}
	return envMapList(env)
}

func envMapList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, key := range slices.Sorted(maps.Keys(env)) {
		out = append(out, key+"="+env[key])
	}
	return out
}

func envListMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, item := range env {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			values[name] = value
		}
	}
	return values
}

func envValue(env []string, name string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		key, value, ok := strings.Cut(env[i], "=")
		if !ok {
			continue
		}
		if key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}
