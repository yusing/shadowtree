//go:build linux

package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/yusing/shadowtree/internal/recipe"
	"golang.org/x/sys/unix"
)

func createOverlayWorkspace(ctx context.Context, source, workDir, workspace string) (*sandboxWorkspace, error) {
	sandbox, err := prepareOverlayWorkspace(source, workDir, workspace, shouldSkip, nil)
	if err != nil {
		return nil, err
	}
	if err := sandbox.probeNamespaceOverlay(ctx); err != nil {
		_ = removeAll(sandbox.overlayDir)
		return nil, fmt.Errorf("namespace overlay: %w", err)
	}
	return sandbox, nil
}

func prepareOverlayWorkspace(source, workDir, workspace string, skip func(string, fs.DirEntry) bool, replaced []string) (*sandboxWorkspace, error) {
	overlayDir := filepath.Join(workDir, "overlay")
	cleanup := true
	defer func() {
		if cleanup {
			_ = removeAll(overlayDir)
		}
	}()
	upper := filepath.Join(overlayDir, "upper")
	work := filepath.Join(overlayDir, "work")
	for _, dir := range []string{upper, work, workspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	protectedWhiteouts, skipped, err := createSkipWhiteouts(source, upper, skip, replaced)
	if err != nil {
		return nil, err
	}
	sandbox := &sandboxWorkspace{
		root:               workspace,
		source:             source,
		target:             source,
		workDir:            workDir,
		overlayDir:         overlayDir,
		upper:              upper,
		work:               work,
		protectedWhiteouts: protectedWhiteouts,
		skipped:            skipped,
		skip:               skip,
		overlay:            true,
	}
	cleanup = false
	return sandbox, nil
}

func (sandbox *sandboxWorkspace) probeNamespaceOverlay(ctx context.Context) error {
	truePath, err := exec.LookPath("true")
	if err != nil {
		truePath = "/bin/true"
	}
	var stderr bytes.Buffer
	if err := sandbox.runNamespaceCommand(ctx, os.Environ(), sandbox.target, []string{truePath}, nil, io.Discard, &stderr); err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return fmt.Errorf("%w: %s", err, message)
		}
		return err
	}
	return nil
}

func (sandbox *sandboxWorkspace) runNamespaceCommand(ctx context.Context, env []string, dir string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	args := []string{
		OverlayHelperCommand,
		sandbox.source,
		sandbox.upper,
		sandbox.work,
		sandbox.target,
		dir,
		"--",
	}
	args = append(args, command...)
	return runNamespaceHelper(ctx, env, args, stdin, stdout, stderr)
}

type namespaceScriptPayload struct {
	Command   recipe.Command
	Env       []string
	Resolved  recipe.Resolved
	Recipes   map[string]recipe.Recipe
	EnumSets  map[string]recipe.Command
	ConfigEnv map[string]string
	SourceDir string
	Verbose   bool
	Stack     []string
}

type namespaceValueBuiltinPayload struct {
	Command  recipe.Command
	Recipe   recipe.Recipe
	Recipes  map[string]recipe.Recipe
	EnumSets map[string]recipe.Command
}

type namespaceExecutionTargetPayload struct {
	Source    recipe.TargetSource
	BuildArgs []string
}

func (sandbox *sandboxWorkspace) runNamespaceScriptCommand(ctx context.Context, env []string, dir string, command recipe.Command, stdin io.Reader, stdout, stderr io.Writer, options Options, stack []string) error {
	payload := namespaceScriptPayload{
		Command:   command,
		Env:       env,
		Resolved:  options.Resolved,
		Recipes:   options.Recipes,
		EnumSets:  options.EnumSets,
		ConfigEnv: options.ConfigEnv,
		SourceDir: options.SourceDir,
		Verbose:   options.Verbose,
		Stack:     stack,
	}
	file, err := os.CreateTemp(sandbox.workDir, "script-*.json")
	if err != nil {
		return err
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	if err := json.NewEncoder(file).Encode(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	args := []string{
		OverlayHelperCommand,
		sandbox.source,
		sandbox.upper,
		sandbox.work,
		sandbox.target,
		dir,
		"--script",
		path,
	}
	return runNamespaceHelper(ctx, env, args, stdin, stdout, stderr)
}

func (sandbox *sandboxWorkspace) runNamespaceValueBuiltinCommand(ctx context.Context, env []string, dir string, command recipe.Command, stderr io.Writer, options Options) ([]recipe.ValueCandidate, error) {
	payload := namespaceValueBuiltinPayload{
		Command:  command,
		Recipe:   options.Resolved.Recipe,
		Recipes:  options.Recipes,
		EnumSets: options.EnumSets,
	}
	file, err := os.CreateTemp(sandbox.workDir, "values-*.json")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	if err := json.NewEncoder(file).Encode(payload); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	args := []string{
		OverlayHelperCommand,
		sandbox.source,
		sandbox.upper,
		sandbox.work,
		sandbox.target,
		sandbox.namespaceDir(dir),
		"--values",
		path,
	}
	var stdout bytes.Buffer
	if err := runNamespaceHelper(ctx, env, args, nil, &stdout, stderr); err != nil {
		return nil, err
	}
	var values []recipe.ValueCandidate
	if err := json.Unmarshal(stdout.Bytes(), &values); err != nil {
		return nil, err
	}
	return values, nil
}

func (sandbox *sandboxWorkspace) runNamespaceExecutionTargets(ctx context.Context, env []string, dir string, source recipe.TargetSource, buildArgs []string, stderr io.Writer) ([]recipe.ExecutionTarget, error) {
	file, err := os.CreateTemp(sandbox.workDir, "targets-*.json")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	if err := json.NewEncoder(file).Encode(namespaceExecutionTargetPayload{Source: source, BuildArgs: buildArgs}); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	args := []string{
		OverlayHelperCommand,
		sandbox.source,
		sandbox.upper,
		sandbox.work,
		sandbox.target,
		dir,
		"--targets",
		path,
	}
	var stdout bytes.Buffer
	if err := runNamespaceHelper(ctx, env, args, nil, &stdout, stderr); err != nil {
		return nil, err
	}
	var targets []recipe.ExecutionTarget
	if err := json.Unmarshal(stdout.Bytes(), &targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func runNamespaceHelper(ctx context.Context, env, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	helper, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, helper, args...)
	cmd.Dir = "/"
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getuid(),
			Size:        1,
		}},
		GidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getgid(),
			Size:        1,
		}},
		GidMappingsEnableSetgroups: false,
	}
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

func OverlayHelperMain(ctx context.Context, argv []string) int {
	if len(argv) < 7 || (argv[5] != "--" && argv[5] != "--script" && argv[5] != "--values" && argv[5] != "--targets") {
		fmt.Fprintln(os.Stderr, "shadowtree overlay helper: missing command")
		return 125
	}
	lower, upper, work, target, dir := argv[0], argv[1], argv[2], argv[3], argv[4]
	command := argv[6:]
	if lower == "" || upper == "" || work == "" || target == "" || dir == "" || len(command) == 0 {
		fmt.Fprintln(os.Stderr, "shadowtree overlay helper: incomplete arguments")
		return 125
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: mount private: %v\n", err)
		return 125
	}
	options := "lowerdir=" + lower + ",upperdir=" + upper + ",workdir=" + work + ",userxattr"
	if err := unix.Mount("overlay", target, "overlay", 0, options); err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: mount overlayfs: %v\n", err)
		return 125
	}
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: chdir: %v\n", err)
		return 125
	}
	if argv[5] == "--script" {
		return overlayHelperScriptMain(ctx, dir, argv[6])
	}
	if argv[5] == "--values" {
		return overlayHelperValuesMain(ctx, dir, argv[6])
	}
	if argv[5] == "--targets" {
		return overlayHelperTargetsMain(ctx, dir, argv[6])
	}
	executable := command[0]
	if !strings.Contains(executable, "/") {
		path, err := exec.LookPath(executable)
		if err != nil {
			fmt.Fprintf(os.Stderr, "shadowtree overlay helper: %s: %v\n", executable, err)
			return 127
		}
		executable = path
	}
	if err := unix.Exec(executable, command, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: exec %s: %v\n", executable, err)
		return 127
	}
	return 127
}

func overlayHelperTargetsMain(ctx context.Context, dir, path string) int {
	file, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: open targets payload: %v\n", err)
		return 125
	}
	defer file.Close()
	var payload namespaceExecutionTargetPayload
	if err := json.NewDecoder(file).Decode(&payload); err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: decode targets payload: %v\n", err)
		return 125
	}
	targets, err := recipe.ResolveExecutionTargets(ctx, payload.Source, dir, os.Environ(), payload.BuildArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: targets: %v\n", err)
		return 125
	}
	if err := json.NewEncoder(os.Stdout).Encode(targets); err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: encode targets: %v\n", err)
		return 125
	}
	return 0
}

func overlayHelperValuesMain(ctx context.Context, dir, path string) int {
	file, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: open values payload: %v\n", err)
		return 125
	}
	defer file.Close()
	var payload namespaceValueBuiltinPayload
	if err := json.NewDecoder(file).Decode(&payload); err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: decode values payload: %v\n", err)
		return 125
	}
	values, _, err := recipe.BuiltinValues(payload.Command, recipe.ValueBuiltinOptions{
		Context:  ctx,
		Dir:      dir,
		Recipe:   payload.Recipe,
		Recipes:  payload.Recipes,
		EnumSets: payload.EnumSets,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: values: %v\n", err)
		return 125
	}
	if err := json.NewEncoder(os.Stdout).Encode(values); err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: encode values: %v\n", err)
		return 125
	}
	return 0
}

func overlayHelperScriptMain(ctx context.Context, dir, path string) int {
	file, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: open script payload: %v\n", err)
		return 125
	}
	defer file.Close()
	var payload namespaceScriptPayload
	if err := json.NewDecoder(file).Decode(&payload); err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: decode script payload: %v\n", err)
		return 125
	}
	options := Options{
		Resolved:  payload.Resolved,
		Recipes:   payload.Recipes,
		EnumSets:  payload.EnumSets,
		ConfigEnv: payload.ConfigEnv,
		SourceDir: payload.SourceDir,
		Verbose:   payload.Verbose,
	}
	err = runScriptCommand(ctx, nil, dir, payload.Env, payload.Command, os.Stdin, os.Stdout, os.Stderr, options, payload.Stack)
	if err == nil {
		return 0
	}
	var exitErr ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	fmt.Fprintf(os.Stderr, "shadowtree overlay helper: script: %v\n", err)
	return 125
}

func createSkipWhiteouts(source, upper string, skip func(string, fs.DirEntry) bool, replaced []string) (map[string]struct{}, []string, error) {
	protected, skipped, err := inspectWorkspaceExclusions(source, func(rel string, entry fs.DirEntry) bool {
		if slices.Contains(replaced, filepath.ToSlash(rel)) {
			return true
		}
		return skip(rel, entry)
	})
	if err != nil {
		return nil, skipped, err
	}
	protected, skipped, err = filterReplacedWorkspaceExclusions(protected, skipped, replaced)
	if err != nil {
		return nil, skipped, err
	}
	for _, name := range slices.Sorted(maps.Keys(protected)) {
		if err := createOverlayWhiteout(source, upper, filepath.FromSlash(name)); err != nil {
			return nil, skipped, fmt.Errorf("whiteout %s: %w", name, err)
		}
	}
	for _, name := range replaced {
		if _, err := os.Lstat(filepath.Join(source, filepath.FromSlash(name))); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, skipped, fmt.Errorf("inspect replacement %s: %w", name, err)
		}
		if err := createOverlayWhiteout(source, upper, filepath.FromSlash(name)); err != nil {
			return nil, skipped, fmt.Errorf("whiteout replacement %s: %w", name, err)
		}
	}
	return protected, skipped, nil
}

func createOverlayWhiteout(source, upper, rel string) error {
	if err := mkdirOverlayParents(source, upper, rel); err != nil {
		return err
	}
	path := filepath.Join(upper, rel)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return err
	}
	return unix.Setxattr(path, "user.overlay.whiteout", []byte("y"), 0)
}

func mkdirOverlayParents(source, upper, rel string) error {
	parent := filepath.Dir(rel)
	if parent == "." {
		return nil
	}
	current := ""
	for part := range strings.SplitSeq(filepath.ToSlash(parent), "/") {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		mode := os.FileMode(0o755)
		if info, err := os.Lstat(filepath.Join(source, filepath.FromSlash(current))); err == nil && info.IsDir() {
			mode = info.Mode().Perm()
		}
		dst := filepath.Join(upper, filepath.FromSlash(current))
		if err := os.Mkdir(dst, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := os.Chmod(dst, mode); err != nil {
			return err
		}
	}
	return nil
}

func reflinkRegularFile(dst, src string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		return fmt.Errorf("reflink %s: %w", dst, err)
	}
	return out.Chmod(perm)
}

func isReflinkUnsupported(err error) bool {
	return errors.Is(err, errReflinkUnsupported) ||
		errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOTTY) ||
		errors.Is(err, unix.EINVAL)
}

func isOverlayWhiteout(path string, info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && info.Mode().Type()&fs.ModeDevice != 0 && unix.Major(uint64(stat.Rdev)) == 0 && unix.Minor(uint64(stat.Rdev)) == 0 {
		return true
	}
	if !info.Mode().IsRegular() || info.Size() != 0 {
		return false
	}
	return hasOverlayXattr(path, "whiteout", 'y')
}

func isOverlayOpaqueDir(path string) bool {
	return hasOverlayXattr(path, "opaque", 'y')
}

func hasOverlayXattr(path, name string, value byte) bool {
	attr := make([]byte, 1)
	n, err := unix.Getxattr(path, "trusted.overlay."+name, attr)
	if err == nil && n == 1 && attr[0] == value {
		return true
	}
	n, err = unix.Getxattr(path, "user.overlay."+name, attr)
	return err == nil && n == 1 && attr[0] == value
}
