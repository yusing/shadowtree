//go:build !linux && !darwin

package runner

import (
	"errors"
	"os"
)

func reflinkRegularFile(_, _ string, _ os.FileMode) error {
	return errReflinkUnsupported
}

func isReflinkUnsupported(err error) bool {
	return errors.Is(err, errReflinkUnsupported)
}
