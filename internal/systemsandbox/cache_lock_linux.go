//go:build linux

package systemsandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// AcquireCacheExecutionLocks holds shared-provider read locks and
// exclusive-provider write locks through container cleanup.
func AcquireCacheExecutionLocks(ctx context.Context, plans []CachePlan, progress io.Writer) (func(), error) {
	return acquireCacheLocks(ctx, plans, false, progress)
}

func acquireCacheResetLocks(ctx context.Context, plans []CachePlan, progress io.Writer) (func(), error) {
	return acquireCacheLocks(ctx, plans, true, progress)
}

func acquireCacheLocks(ctx context.Context, plans []CachePlan, reset bool, progress io.Writer) (func(), error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	lockDir := filepath.Join(cacheDir, "shadowtree", "cache-locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, err
	}
	var files []*os.File
	release := func() {
		for index := len(files) - 1; index >= 0; index-- {
			_ = syscall.Flock(int(files[index].Fd()), syscall.LOCK_UN)
			_ = files[index].Close()
		}
	}
	for _, plan := range plans {
		operation := syscall.LOCK_SH
		if reset || plan.Concurrency == "exclusive" {
			operation = syscall.LOCK_EX
		}
		file, err := os.OpenFile(filepath.Join(lockDir, plan.Key+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			release()
			return nil, err
		}
		waiting := false
		for {
			err = syscall.Flock(int(file.Fd()), operation|syscall.LOCK_NB)
			if err == nil {
				files = append(files, file)
				break
			}
			if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
				_ = file.Close()
				release()
				return nil, fmt.Errorf("lock cache %s: %w", plan.Provider, err)
			}
			if !waiting && progress != nil {
				_, _ = fmt.Fprintf(progress, "shadowtree: waiting for cache lock %s\n", plan.Provider)
				waiting = true
			}
			select {
			case <-ctx.Done():
				_ = file.Close()
				release()
				return nil, context.Cause(ctx)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return release, nil
}
