package runner

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func reflinkRegularFile(dst, src string, perm os.FileMode) error {
	const flags = unix.CLONE_NOFOLLOW | unix.CLONE_NOOWNERCOPY
	err := unix.Clonefile(src, dst, flags)
	if errors.Is(err, unix.EEXIST) {
		if err := os.Remove(dst); err != nil {
			return err
		}
		err = unix.Clonefile(src, dst, flags)
	}
	if err != nil {
		return fmt.Errorf("clonefile %s: %w", dst, err)
	}
	return os.Chmod(dst, perm)
}

func isReflinkUnsupported(err error) bool {
	return errors.Is(err, errReflinkUnsupported) ||
		errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EINVAL)
}
