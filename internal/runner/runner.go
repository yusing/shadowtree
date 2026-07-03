package runner

import (
	"context"
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

	"github.com/yusing/shadowtree/internal/recipe"
)

type Options struct {
	Resolved   recipe.Resolved
	SourceDir  string
	Keep       bool
	PrintOnly  bool
	Verbose    bool
	SyncOutAll bool
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
}

type ExitError struct {
	Code int
}

var errReflinkUnsupported = errors.New("reflink unsupported")

const OverlayHelperCommand = "__shadowtree_overlay_helper"

func (err ExitError) Error() string {
	return fmt.Sprintf("command exited with status %d", err.Code)
}

func Run(ctx context.Context, options Options) (runErr error) {
	stdout := writerOr(options.Stdout, os.Stdout)
	stderr := writerOr(options.Stderr, os.Stderr)
	stdin := readerOr(options.Stdin, os.Stdin)
	if options.PrintOnly {
		printPlan(stdout, options.Resolved)
		return nil
	}
	source, err := filepath.Abs(options.SourceDir)
	if err != nil {
		return err
	}
	env := mergedEnv(os.Environ(), options.Resolved.GlobalEnv, options.Resolved.Recipe.Env)
	if !options.Resolved.Sandboxed {
		if options.Verbose {
			fmt.Fprintf(stderr, "shadowtree: running unsandboxed in %s\n", source)
		}
		return runResolvedCommands(ctx, nil, source, env, options, stdin, stdout, stderr)
	}
	workDir, err := os.MkdirTemp("", "shadowtree-*")
	if err != nil {
		return err
	}
	workspace := filepath.Join(workDir, "workspace")
	if options.Keep {
		fmt.Fprintf(stderr, "shadowtree: workspace: %s\n", workspace)
	}
	sandbox, err := createSandboxWorkspace(source, workDir, workspace, stderr, options.Verbose)
	if err != nil {
		_ = removeAll(workDir)
		return err
	}
	defer func() {
		if err := sandbox.Close(options.Keep); err != nil && runErr == nil {
			runErr = err
		}
	}()
	if err := runResolvedCommands(ctx, sandbox, sandbox.root, env, options, stdin, stdout, stderr); err != nil {
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

type sandboxWorkspace struct {
	root               string
	source             string
	workDir            string
	overlayDir         string
	upper              string
	work               string
	protectedWhiteouts map[string]struct{}
	overlay            bool
}

var newOverlayWorkspace = createOverlayWorkspace

func createSandboxWorkspace(source, workDir, workspace string, stderr io.Writer, verbose bool) (*sandboxWorkspace, error) {
	sandbox, err := newOverlayWorkspace(source, workDir, workspace)
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
	if err := sandbox.materialize(root); err != nil {
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

func (sandbox *sandboxWorkspace) Close(keep bool) error {
	if !sandbox.overlay {
		if keep {
			return nil
		}
		return removeAll(sandbox.workDir)
	}
	var firstErr error
	persisted := filepath.Join(sandbox.workDir, "workspace.persisted")
	if keep && firstErr == nil {
		if err := sandbox.materialize(persisted); err != nil {
			firstErr = fmt.Errorf("keep workspace: %w", err)
		} else if err := removeAll(sandbox.root); err != nil {
			firstErr = err
		} else if err := os.Rename(persisted, sandbox.root); err != nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		if keep {
			firstErr = removeAll(sandbox.overlayDir)
		} else {
			firstErr = removeAll(sandbox.workDir)
		}
	}
	if firstErr != nil {
		_ = removeAll(persisted)
	}
	return firstErr
}

func (sandbox *sandboxWorkspace) materialize(dst string) error {
	if err := clearHostDir(dst); err != nil {
		return err
	}
	if err := CopyTree(sandbox.source, dst); err != nil {
		return err
	}
	return applyOverlayUpper(sandbox.upper, dst, nil)
}

func runResolvedCommands(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, options Options, stdin io.Reader, stdout, stderr io.Writer) error {
	var firstErr error
	for i, command := range options.Resolved.Recipe.Pre {
		if err := runCommand(ctx, sandbox, dir, env, command, stdin, stdout, stderr, options.Verbose, "pre", i); err != nil {
			firstErr = err
			break
		}
	}
	if firstErr == nil {
		firstErr = runCommand(ctx, sandbox, dir, env, options.Resolved.Main, stdin, stdout, stderr, options.Verbose, "main", 0)
	}
	for i, command := range options.Resolved.Recipe.Post {
		if err := runCommand(ctx, sandbox, dir, env, command, stdin, stdout, stderr, options.Verbose, "post", i); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func runCommand(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, command recipe.Command, stdin io.Reader, stdout, stderr io.Writer, verbose bool, phase string, index int) error {
	if verbose {
		label := phase
		if phase != "main" {
			label = fmt.Sprintf("%s[%d]", phase, index)
		}
		fmt.Fprintf(stderr, "shadowtree: %s: %s\n", label, strings.Join(command, " "))
	}
	if sandbox != nil && sandbox.overlay {
		return sandbox.runNamespaceCommand(ctx, env, command, stdin, stdout, stderr)
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ExitError{Code: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}

func printPlan(w io.Writer, resolved recipe.Resolved) {
	fmt.Fprintf(w, "recipe: %s\n", resolved.Name)
	if resolved.Profile != "" {
		fmt.Fprintf(w, "profile: %s\n", resolved.Profile)
	}
	if resolved.ConfigPath != "" {
		fmt.Fprintf(w, "config: %s\n", resolved.ConfigPath)
	}
	if !resolved.Sandboxed {
		fmt.Fprintln(w, "sandboxed: false")
	}
	for i, command := range resolved.Recipe.Pre {
		fmt.Fprintf(w, "pre[%d]: %s\n", i, strings.Join(command, " "))
	}
	fmt.Fprintf(w, "main: %s\n", strings.Join(resolved.Main, " "))
	for i, command := range resolved.Recipe.Post {
		fmt.Fprintf(w, "post[%d]: %s\n", i, strings.Join(command, " "))
	}
	for _, path := range resolved.SyncOut {
		fmt.Fprintf(w, "sync_out: %s\n", path)
	}
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

func SyncPath(workspace, source, requested string) error {
	cleaned := filepath.Clean(requested)
	if cleaned == "." || filepath.IsAbs(cleaned) || !filepath.IsLocal(cleaned) {
		return fmt.Errorf("sync_out path must stay under workspace: %s", requested)
	}
	src := filepath.Join(workspace, cleaned)
	info, statErr := os.Lstat(src)
	dstRoot, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer dstRoot.Close()
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return removeRootPathIfPresent(dstRoot, cleaned)
		}
		return statErr
	}
	mode := info.Mode()
	switch {
	case mode.IsDir():
		if err := removeRootPath(dstRoot, cleaned); err != nil {
			return err
		}
		return copyTreeToRoot(src, dstRoot, cleaned)
	case mode.Type() == 0:
		if err := removeRootPath(dstRoot, cleaned); err != nil {
			return err
		}
		return copyRegularFileToRoot(dstRoot, cleaned, src, mode.Perm())
	case mode.Type() == os.ModeSymlink:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := ensureRootParent(dstRoot, cleaned); err != nil {
			return err
		}
		if err := removeRootPath(dstRoot, cleaned); err != nil {
			return err
		}
		return dstRoot.Symlink(target, cleaned)
	default:
		return fmt.Errorf("unsupported sync_out file type: %s", requested)
	}
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
		if isOverlayWhiteout(src, info) {
			if err := removeRootPath(dst, name); err != nil {
				return err
			}
			return nil
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
			if err := removeRootPath(dst, name); err != nil {
				return err
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
	})
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
	for _, part := range strings.Split(dir, "/") {
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
		for _, part := range strings.Split(dir, "/") {
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
		if name == excludedName || strings.HasPrefix(name, excludedName+"/") {
			return true
		}
	}
	return false
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

func mergedEnv(base []string, global, local map[string]string) []string {
	env := map[string]string{}
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	maps.Copy(env, global)
	maps.Copy(env, local)
	out := make([]string, 0, len(env))
	for _, key := range slices.Sorted(maps.Keys(env)) {
		out = append(out, key+"="+env[key])
	}
	return out
}

func writerOr(w io.Writer, fallback io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return fallback
}

func readerOr(r io.Reader, fallback io.Reader) io.Reader {
	if r != nil {
		return r
	}
	return fallback
}
