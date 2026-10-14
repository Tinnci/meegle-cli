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
			if err := meegle.EnsureTelemetryDisclosure(cmd.ErrOrStderr()); err != nil {
				return meerrors.NewClientError("TELEMETRY_SETTINGS_FAILED", "Failed to save the diagnostics notice.").WithCause(err)
			}
			if err := meegle.SetTelemetryEnabled(true); err != nil {
				return meerrors.NewClientError("TELEMETRY_SETTINGS_FAILED", "Failed to enable diagnostics.").WithCause(err)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "Failed-command diagnostics enabled locally.")
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
	var format string
	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether failed-command diagnostics are enabled",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			profile, _ := cmd.Flags().GetString("profile")
			enabled, err := meegle.TelemetryEnabled(profile)
			if err != nil {
				return meerrors.NewClientError("TELEMETRY_STATUS_FAILED", "Failed to read diagnostics status.").WithCause(err)
			}
			text, err := renderPayload(map[string]bool{"enabled": enabled}, format)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), text)
			return err
		},
	}
	status.Flags().StringVarP(&format, "format", "o", "json", "Output format: json, table, ndjson")
	command.AddCommand(status)
	return command
}
