//go:build linux

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func createOverlayWorkspace(source, workDir, workspace string) (*sandboxWorkspace, error) {
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
	protectedWhiteouts, err := createSkipWhiteouts(source, upper)
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
		overlay:            true,
	}
	if err := sandbox.probeNamespaceOverlay(); err != nil {
		return nil, fmt.Errorf("namespace overlay: %w", err)
	}
	cleanup = false
	return sandbox, nil
}

func (sandbox *sandboxWorkspace) probeNamespaceOverlay() error {
	truePath, err := exec.LookPath("true")
	if err != nil {
		truePath = "/bin/true"
	}
	var stderr bytes.Buffer
	if err := sandbox.runNamespaceCommand(context.Background(), os.Environ(), []string{truePath}, nil, io.Discard, &stderr); err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return fmt.Errorf("%w: %s", err, message)
		}
		return err
	}
	return nil
}

func (sandbox *sandboxWorkspace) runNamespaceCommand(ctx context.Context, env []string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	helper, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{
		OverlayHelperCommand,
		sandbox.source,
		sandbox.upper,
		sandbox.work,
		sandbox.target,
		"--",
	}
	args = append(args, command...)
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
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ExitError{Code: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}

func OverlayHelperMain(argv []string) int {
	if len(argv) < 6 || argv[4] != "--" {
		fmt.Fprintln(os.Stderr, "shadowtree overlay helper: missing command")
		return 125
	}
	lower, upper, work, target := argv[0], argv[1], argv[2], argv[3]
	command := argv[5:]
	if lower == "" || upper == "" || work == "" || target == "" || len(command) == 0 {
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
	if err := os.Chdir(target); err != nil {
		fmt.Fprintf(os.Stderr, "shadowtree overlay helper: chdir: %v\n", err)
		return 125
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

func createSkipWhiteouts(source, upper string) (map[string]struct{}, error) {
	protected := map[string]struct{}{}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrPermission) {
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		protectedWhiteout := shouldSkip(rel, entry)
		whiteout := protectedWhiteout
		if !whiteout {
			info, err := entry.Info()
			if err != nil {
				if errors.Is(err, os.ErrPermission) {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				return err
			}
			mode := info.Mode()
			whiteout = !mode.IsDir() && mode.Type() != 0 && mode.Type() != fs.ModeSymlink
		}
		if whiteout {
			if err := createOverlayWhiteout(source, upper, rel); err != nil {
				return fmt.Errorf("whiteout %s: %w", rel, err)
			}
			if protectedWhiteout {
				protected[filepath.ToSlash(rel)] = struct{}{}
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	return protected, err
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
	for _, part := range strings.Split(filepath.ToSlash(parent), "/") {
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
