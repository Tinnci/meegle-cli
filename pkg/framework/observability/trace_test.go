// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package observability

import (
	"context"
	"sync"
	"testing"
)

func TestTraceIsContextScopedBoundedAndRaceSafe(t *testing.T) {
	trace := NewTrace()
	ctx := WithTrace(context.Background(), trace)
	SetCommand(ctx, []string{"workitem", "get"}, []string{"work-item-id", "profile"})
	SetStage(ctx, "request_send")
	SetReachability(ctx, "yes")

	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			AddBackendRef(ctx, BackendRef{Service: "meegle_mcp", Operation: "tools_call", LogID: "same-logid"})
		}()
	}
	group.Wait()
	snapshot := trace.Snapshot()
	if snapshot.Command != "workitem get" || snapshot.Stage != "request_send" || snapshot.Reachability != "yes" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if len(snapshot.BackendRefs) != 1 {
		t.Fatalf("backend refs = %#v, want one deduplicated ref", snapshot.BackendRefs)
	}
}

func TestTraceStageBelongsToCurrentStep(t *testing.T) {
	trace := NewTrace()
	ctx := WithTrace(context.Background(), trace)
	SetStep(ctx, "runtime_executor")
	SetStage(ctx, "response_parse")
	if trace.Snapshot().Stage != "response_parse" {
		t.Fatal("current step lost its detailed stage")
	}
	SetStep(ctx, "output")
	if snapshot := trace.Snapshot(); snapshot.Step != "output" || snapshot.Stage != "" {
		t.Fatalf("new step retained previous stage: %#v", snapshot)
	}
	SetStage(ctx, "local_output")
	SetStep(ctx, "output")
	if trace.Snapshot().Stage != "" {
		t.Fatal("re-entered step retained previous invocation's stage")
	}
}
