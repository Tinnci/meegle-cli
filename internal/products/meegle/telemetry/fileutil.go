// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	return writeAtomicFile(path, data, mode, true)
}

// atomicWriteFileBuffered survives writer exit, but does not promise durability
// across power loss or an OS crash.
func atomicWriteFileBuffered(path string, data []byte, mode os.FileMode) error {
	return writeAtomicFile(path, data, mode, false)
}

func writeAtomicFile(path string, data []byte, mode os.FileMode, durable bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	cleanupStaleAtomicTemps(dir, time.Now().Add(-DefaultQueueRetention))
	temp, err := os.CreateTemp(dir, ".telemetry-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(mode); err != nil {
		cleanup()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return err
	}
	if durable {
		if err := temp.Sync(); err != nil {
			cleanup()
			return err
		}
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Chmod(path, mode); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if durable {
		return syncParentDirectory(dir)
	}
	return nil
}

func cleanupStaleAtomicTemps(dir string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), ".telemetry-") {
			continue
		}
		if info, infoErr := entry.Info(); infoErr == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
