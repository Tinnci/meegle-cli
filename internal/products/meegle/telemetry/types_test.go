// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"strings"
	"testing"
)

func TestEventNormalizeDropsUntrustedBackendRefsWithoutDroppingEvent(t *testing.T) {
	event := testEvent("refs")
	event.BackendRefs = []BackendRef{
		{Service: "meegle_mcp", Operation: "tools_call", LogID: "valid.log-id:1"},
		{Service: "meegle_mcp", Operation: "tools_call", LogID: strings.Repeat("x", 129)},
		{Service: "unknown", Operation: "tools_call", LogID: "looks-valid"},
		{Service: "meegle_mcp", Operation: "tools_call", LogID: "contains secret spaces"},
	}
	normalized := event.Normalize()
	if len(normalized.BackendRefs) != 1 || normalized.BackendRefs[0].LogID != "valid.log-id:1" {
		t.Fatalf("normalized refs = %#v", normalized.BackendRefs)
	}
	if err := normalized.Validate(); err != nil {
		t.Fatalf("event with valid remaining ref was dropped: %v", err)
	}
}

func TestEventValidateRejectsFailureWithoutFingerprint(t *testing.T) {
	event := testEvent("missing-fingerprint")
	event.Fingerprint = ""

	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("Validate() = %v, want fingerprint validation error", err)
	}
}

func TestEventValidateRejectsSuccessWithFingerprint(t *testing.T) {
	event := testEvent("success-with-fingerprint")
	event.Success = true
	event.ExitCode = 0
	event.FailureStage = ""
	event.FinalOutcome = OutcomeSuccess
	event.Category = ""
	event.ErrorCode = ""
	event.Retryable = false
	event.Fingerprint = strings.Repeat("a", 64)

	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "failure fields") {
		t.Fatalf("Validate() = %v, want success failure-fields error", err)
	}
}

func TestEventValidateRejectsCommandShapedLikeRawArgv(t *testing.T) {
	event := testEvent("raw-argv")
	event.Command = "workitem get --token secret"

	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("Validate() = %v, want command validation error", err)
	}
}
