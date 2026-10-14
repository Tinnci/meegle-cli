// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessLockDoesNotInterpretFileContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	if err := os.WriteFile(path, []byte("arbitrary contents"), 0600); err != nil {
		t.Fatal(err)
	}
	release, err := NewProcessLock(path).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestProcessLockNeverStealsFromLiveOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	release, err := NewProcessLock(path).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := NewProcessLock(path).Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire() = %v, want deadline exceeded while live owner holds lock", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live owner's lock was stolen: %v", err)
	}
}

func TestProcessLockSharedWritersExcludeDisable(t *testing.T) {
	lock := NewProcessLock(filepath.Join(t.TempDir(), ".lock"))
	first, err := lock.AcquireShared(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	second, err := lock.AcquireShared(ctx)
	cancel()
	if err != nil {
		t.Fatalf("concurrent record blocked: %v", err)
	}
	second()
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if release, err := lock.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("exclusive disable acquired while record active: %v", err)
	}
}

func TestProcessLockExclusiveDisableExcludesWriters(t *testing.T) {
	lock := NewProcessLock(filepath.Join(t.TempDir(), ".lock"))
	release, err := lock.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if release, err := lock.AcquireShared(ctx); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("record acquired while disable active: %v", err)
	}
}
