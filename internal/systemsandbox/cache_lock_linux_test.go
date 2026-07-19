//go:build linux

package systemsandbox

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcquireCacheLocksWaitsAndSupportsCancellation(t *testing.T) {
	plan := testCachePlan(t)
	plan.Concurrency = "exclusive"
	release, err := AcquireCacheExecutionLocks(t.Context(), []CachePlan{plan}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	var progress bytes.Buffer
	_, err = AcquireCacheExecutionLocks(ctx, []CachePlan{plan}, &progress)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireCacheExecutionLocks error = %v, want deadline", err)
	}
	if progress.String() != "shadowtree: waiting for cache lock go-build\n" {
		t.Fatalf("progress = %q", progress.String())
	}
}

func TestCacheResetWaitsForSharedExecutionLock(t *testing.T) {
	plan := testCachePlan(t)
	release, err := AcquireCacheExecutionLocks(t.Context(), []CachePlan{plan}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	called := false
	err = resetCachesWith(ctx, Docker, []CachePlan{plan}, nil, func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("resetCachesWith error = %v, want deadline", err)
	}
	if called {
		t.Fatal("reset inspected runtime before acquiring the exclusive cache lock")
	}
}
