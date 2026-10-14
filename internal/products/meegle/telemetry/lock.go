// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ProcessLock is an OS-backed advisory lock released when the process exits.
// Acquire uses the caller's context to bound waiting for ownership.
type ProcessLock struct{ path string }

func NewProcessLock(path string) *ProcessLock { return &ProcessLock{path: path} }

func (lock *ProcessLock) Acquire(ctx context.Context) (func(), error) {
	return lock.acquire(ctx, true)
}

// AcquireShared lets records proceed together while excluding disable/clear.
func (lock *ProcessLock) AcquireShared(ctx context.Context) (func(), error) {
	return lock.acquire(ctx, false)
}

func (lock *ProcessLock) acquire(ctx context.Context, exclusive bool) (func(), error) {
	if lock == nil || lock.path == "" {
		return nil, errors.New("telemetry process lock is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(filepath.Dir(lock.path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(lock.path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		acquired, lockErr := telemetryTryLockFile(file, exclusive)
		if lockErr != nil {
			_ = file.Close()
			return nil, lockErr
		}
		if acquired {
			return func() {
				_ = telemetryUnlockFile(file)
				_ = file.Close()
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
