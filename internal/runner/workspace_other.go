//go:build !linux

package runner

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
)

func createOverlayWorkspace(context.Context, string, string, string) (*sandboxWorkspace, error) {
	return nil, errors.New("overlayfs requires linux")
}

func (sandbox *sandboxWorkspace) runNamespaceCommand(context.Context, []string, string, []string, io.Reader, io.Writer, io.Writer) error {
	return errors.New("overlay namespace requires linux")
}

func OverlayHelperMain([]string) int {
	return 125
}

func reflinkRegularFile(_, _ string, _ os.FileMode) error {
	return errReflinkUnsupported
}

func isReflinkUnsupported(err error) bool {
	return errors.Is(err, errReflinkUnsupported)
}

func isOverlayWhiteout(string, fs.FileInfo) bool {
	return false
}

func isOverlayOpaqueDir(string) bool {
	return false
}
