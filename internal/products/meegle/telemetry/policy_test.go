// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func TestDefaultPolicyReportsFailuresOnly(t *testing.T) {
	policy := DefaultPolicy()

	if rate, ok := policy.SampleRate(Event{Command: "workitem get", ErrorCode: "BACKEND_ERROR"}); !ok || rate != BasisPoints {
		t.Fatalf("failure sample decision = (%d, %v), want (%d, true)", rate, ok, BasisPoints)
	}
	if rate, ok := policy.SampleRate(Event{Command: "workitem get", Success: true}); ok || rate != 0 {
		t.Fatalf("success sample decision = (%d, %v), want (0, false)", rate, ok)
	}
}

func TestPolicyStoreValidRefreshRepairsCorruptCache(t *testing.T) {
	root := t.TempDir()
	store := NewPolicyStore(root)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, []byte(`{"policy":`), 0600); err != nil {
		t.Fatal(err)
	}
	want := DefaultPolicy()
	want.Sampling.Default.FailureRate = 4321
	if err := store.ApplyRefresh(want, true, time.Now()); err != nil {
		t.Fatalf("ApplyRefresh() did not repair corrupt cache: %v", err)
	}
	state, err := store.Load()
	if err != nil || !state.PolicyFromValidTCC || state.Policy.Sampling.Default.FailureRate != 4321 {
		t.Fatalf("repaired state = %#v, err=%v", state, err)
	}
}

func TestPolicyStoreFailureMarkerRepairsCorruptCacheWithDefault(t *testing.T) {
	root := t.TempDir()
	store := NewPolicyStore(root)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, []byte(`not-json`), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.RecordRefreshFailure(now); err != nil {
		t.Fatalf("RecordRefreshFailure() did not repair corrupt cache: %v", err)
	}
	state, err := store.Load()
	if err != nil || state.PolicyFromValidTCC || state.Policy.Sampling.Default.FailureRate != BasisPoints {
		t.Fatalf("repaired failure state = %#v, err=%v", state, err)
	}
}

func TestPolicyStoreUsesTwelveHourSoftTTLForValidTCC(t *testing.T) {
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	store := NewPolicyStore(t.TempDir())
	policy := DefaultPolicy()
	policy.Sampling.Default.FailureRate = 5000

	if err := store.ApplyRefresh(policy, true, now); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !state.PolicyFromValidTCC || state.Policy.Sampling.Default.FailureRate != 5000 {
		t.Fatalf("state = %#v", state)
	}
	if store.RefreshDue(now.Add(12*time.Hour - time.Second)) {
		t.Fatal("valid TCC policy became due before twelve hours")
	}
	if !store.RefreshDue(now.Add(12 * time.Hour)) {
		t.Fatal("valid TCC policy did not become due at twelve hours")
	}
}

func TestPolicyStoreKeepsLastValidPolicyAndRetriesUnhealthyStateAfterOneHour(t *testing.T) {
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	store := NewPolicyStore(t.TempDir())
	valid := DefaultPolicy()
	valid.CommandFilter = Filter{Mode: FilterInclude, Values: []string{"workitem get"}}
	if err := store.ApplyRefresh(valid, true, now); err != nil {
		t.Fatal(err)
	}

	if err := store.RecordRefreshFailure(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.PolicyFromValidTCC {
		t.Fatal("latest failed refresh must mark policy health false")
	}
	if state.Policy.CommandFilter.Mode != FilterInclude {
		t.Fatalf("last valid policy was replaced: %#v", state.Policy)
	}
	if store.RefreshDue(now.Add(2*time.Hour - time.Second)) {
		t.Fatal("unhealthy policy became due before one hour")
	}
	if !store.RefreshDue(now.Add(2 * time.Hour)) {
		t.Fatal("unhealthy policy did not become due after one hour")
	}
}

func TestPolicyStoreMissingTCCResponseDoesNotReplaceLastValidPolicy(t *testing.T) {
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	store := NewPolicyStore(t.TempDir())
	valid := DefaultPolicy()
	valid.Enabled = false
	if err := store.ApplyRefresh(valid, true, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyRefresh(DefaultPolicy(), false, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Policy.Enabled || state.PolicyFromValidTCC {
		t.Fatalf("state = %#v, want retained disabled policy with unhealthy flag", state)
	}
}

func TestPolicyStoreRejectsInvalidPolicyWithoutChangingState(t *testing.T) {
	store := NewPolicyStore(t.TempDir())
	invalid := DefaultPolicy()
	invalid.Sampling.Default.SuccessRate = 1

	if err := store.ApplyRefresh(invalid, true, time.Now()); err == nil {
		t.Fatal("ApplyRefresh(invalid) succeeded")
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.PolicyFromValidTCC || !reflect.DeepEqual(state.Policy, DefaultPolicy()) {
		t.Fatalf("state changed after invalid policy: %#v", state)
	}
}
