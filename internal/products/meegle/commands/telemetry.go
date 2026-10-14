// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/larksuite/meegle-cli/internal/products/meegle"
	meerrors "github.com/larksuite/meegle-cli/internal/products/meegle/errors"
)

func NewTelemetryCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "telemetry",
		Short: "Control failed-command diagnostics",
	}
	command.AddCommand(&cobra.Command{
		Use:   "enable",
		Short: "Enable failed-command diagnostics (remote policy may still disable collection)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := meegle.SetTelemetryEnabled(true); err != nil {
				return meerrors.NewClientError("TELEMETRY_SETTINGS_FAILED", "Failed to enable diagnostics.").WithCause(err)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "Failed-command diagnostics enabled.")
			return err
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "disable",
		Short: "Permanently disable diagnostics until manually enabled again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := meegle.SetTelemetryEnabled(false); err != nil {
				return meerrors.NewClientError("TELEMETRY_SETTINGS_FAILED", "Failed to disable diagnostics.").WithCause(err)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "Failed-command diagnostics disabled; queued events cleared.")
			return err
		},
	})
	return command
}
