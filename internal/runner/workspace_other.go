//go:build !linux

package runner

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/yusing/shadowtree/internal/recipe"
)

func createOverlayWorkspace(context.Context, string, string, string) (*sandboxWorkspace, error) {
	return nil, errors.New("overlayfs requires linux")
}

func prepareOverlayWorkspace(string, string, string, func(string, fs.DirEntry) bool, []string) (*sandboxWorkspace, error) {
	return nil, errors.New("overlayfs requires linux")
}

func (sandbox *sandboxWorkspace) runNamespaceCommand(context.Context, []string, string, []string, io.Reader, io.Writer, io.Writer) error {
	return errors.New("overlay namespace requires linux")
}

func (sandbox *sandboxWorkspace) runNamespaceScriptCommand(context.Context, []string, string, recipe.Command, io.Reader, io.Writer, io.Writer, Options, []string) error {
	return errors.New("overlay namespace requires linux")
}

func (sandbox *sandboxWorkspace) runNamespaceValueBuiltinCommand(context.Context, []string, string, recipe.Command, io.Writer, Options) ([]recipe.ValueCandidate, error) {
	return nil, errors.New("overlay namespace requires linux")
}

func (sandbox *sandboxWorkspace) runNamespaceExecutionTargets(context.Context, []string, string, recipe.TargetSource, []string, io.Writer) ([]recipe.ExecutionTarget, error) {
	return nil, errors.New("overlay namespace requires linux")
}

func OverlayHelperMain(context.Context, []string) int {
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
