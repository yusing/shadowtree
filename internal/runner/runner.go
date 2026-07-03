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

func (err ExitError) Error() string {
	return fmt.Sprintf("command exited with status %d", err.Code)
}

func Run(ctx context.Context, options Options) error {
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
		return runResolvedCommands(ctx, source, env, options, stdin, stdout, stderr)
	}
	workDir, err := os.MkdirTemp("", "shadowtree-*")
	if err != nil {
		return err
	}
	workspace := filepath.Join(workDir, "workspace")
	if options.Keep {
		fmt.Fprintf(stderr, "shadowtree: workspace: %s\n", workspace)
	} else {
		defer os.RemoveAll(workDir)
	}
	if options.Verbose {
		fmt.Fprintf(stderr, "shadowtree: copying %s -> %s\n", source, workspace)
	}
	if err := CopyTree(source, workspace); err != nil {
		return fmt.Errorf("copy workspace: %w", err)
	}
	if err := runResolvedCommands(ctx, workspace, env, options, stdin, stdout, stderr); err != nil {
		return err
	}
	if options.SyncOutAll {
		if options.Verbose {
			fmt.Fprintln(stderr, "shadowtree: syncing entire workspace")
		}
		return replaceDirContents(workspace, source)
	}
	for _, path := range options.Resolved.SyncOut {
		if err := SyncPath(workspace, source, path); err != nil {
			return fmt.Errorf("sync %s: %w", path, err)
		}
	}
	return nil
}

func runResolvedCommands(ctx context.Context, dir string, env []string, options Options, stdin io.Reader, stdout, stderr io.Writer) error {
	var firstErr error
	for i, command := range options.Resolved.Recipe.Pre {
		if err := runCommand(ctx, dir, env, command, stdin, stdout, stderr, options.Verbose, "pre", i); err != nil {
			firstErr = err
			break
		}
	}
	if firstErr == nil {
		firstErr = runCommand(ctx, dir, env, options.Resolved.Main, stdin, stdout, stderr, options.Verbose, "main", 0)
	}
	for i, command := range options.Resolved.Recipe.Post {
		if err := runCommand(ctx, dir, env, command, stdin, stdout, stderr, options.Verbose, "post", i); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func runCommand(ctx context.Context, dir string, env []string, command recipe.Command, stdin io.Reader, stdout, stderr io.Writer, verbose bool, phase string, index int) error {
	if verbose {
		label := phase
		if phase != "main" {
			label = fmt.Sprintf("%s[%d]", phase, index)
		}
		fmt.Fprintf(stderr, "shadowtree: %s: %s\n", label, strings.Join(command, " "))
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
	dst := filepath.Join(source, cleaned)
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	mode := info.Mode()
	switch {
	case mode.IsDir():
		return CopyTree(src, dst)
	case mode.Type() == 0:
		return copyRegularFile(src, dst, mode.Perm())
	case mode.Type() == os.ModeSymlink:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.Symlink(target, dst)
	default:
		return fmt.Errorf("unsupported sync_out file type: %s", requested)
	}
}

func replaceDirContents(src, dst string) error {
	entries, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if shouldSkip(entry.Name(), entry) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return CopyTree(src, dst)
}

func copyRegularFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
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
