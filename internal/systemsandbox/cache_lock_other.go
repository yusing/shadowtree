//go:build !linux

package systemsandbox

import (
	"context"
	"errors"
	"io"
)

func AcquireCacheExecutionLocks(context.Context, []CachePlan, io.Writer) (func(), error) {
	return nil, errors.New("system cache locking requires Linux")
}

func acquireCacheResetLocks(context.Context, []CachePlan, io.Writer) (func(), error) {
	return nil, errors.New("system cache locking requires Linux")
}
