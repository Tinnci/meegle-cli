//go:build windows

// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

// Windows does not support fsync on a directory handle through os.File.
// The file itself has already been flushed before the atomic rename.
func syncParentDirectory(string) error { return nil }
