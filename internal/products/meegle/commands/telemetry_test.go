// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	meerrors "github.com/larksuite/meegle-cli/internal/products/meegle/errors"
)

func TestTelemetrySettingsFailureUsesClientError(t *testing.T) {
	const childEnv = "MEEGLE_TEST_TELEMETRY_SETTINGS_ERROR"
	if os.Getenv(childEnv) == "1" {
		for _, action := range []string{"enable", "disable"} {
			command := NewTelemetryCmd()
			command.SetArgs([]string{action})
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			err := command.Execute()
			var clientErr *meerrors.MeegleError
			if !errors.As(err, &clientErr) || clientErr == nil {
				t.Fatalf("%s error type = %T, want MeegleError", action, err)
			}
			if clientErr.Code != "TELEMETRY_SETTINGS_FAILED" || clientErr.ExitCode != 1 || clientErr.Cause == nil {
				t.Fatalf("%s error = %#v, want stable client error with cause", action, clientErr)
			}
		}
		return
	}
	// Config paths are initialized once, so isolate the command in a child process.
	fixtureHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(fixtureHome, ".meegle"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", fixtureHome)
	t.Setenv(childEnv, "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestTelemetrySettingsFailureUsesClientError$")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated command test: %v\n%s", err, output)
	}
}
