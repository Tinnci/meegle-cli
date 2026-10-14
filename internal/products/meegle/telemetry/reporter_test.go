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

func TestReporterPersistsNewFailureWithFullBacklog(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < DefaultMaxQueueEvents; index++ {
		writeRoutedEnvelope(t, root, index+1, time.Now().Add(-time.Hour), 0)
	}
	if err := os.WriteFile(filepath.Join(root, ".sequence"), []byte("1000"), 0600); err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(root, QueueOptions{})
	reporter := NewReporter(ReporterConfig{Queue: queue, Settings: NewSettingsStore(root), Policies: NewPolicyStore(root)})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if recorded, err := reporter.RecordContext(ctx, validFailureEvent("new-failure")); err != nil || !recorded {
		t.Fatalf("RecordContext(full backlog) = (%v, %v), want (true, nil)", recorded, err)
	}
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Event.CallID == "new-failure" {
			return
		}
	}
	t.Fatal("successfully recorded failure is absent from the queue")
}

func TestReporterDoesNotAcknowledgeCanceledOrFailedPersistence(t *testing.T) {
	for _, mode := range []string{"canceled", "disk-error"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			queueRoot := filepath.Join(root, "route")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			} else if err := os.WriteFile(queueRoot, []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
			queue := NewQueue(queueRoot, QueueOptions{})
			reporter := NewReporter(ReporterConfig{Queue: queue, Settings: NewSettingsStore(root), Policies: NewPolicyStore(root)})
			recorded, err := reporter.RecordContext(ctx, validFailureEvent("not-persisted"))
			if recorded || err == nil {
				t.Fatalf("failed persistence acknowledged: recorded=%v err=%v", recorded, err)
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled record = %v", err)
			}
			if queue.HasEvents() {
				t.Fatal("failed persistence left a committed event")
			}
		})
	}
}

func TestReporterFreezesPolicyHealthWhenEventIsEnqueued(t *testing.T) {
	root := t.TempDir()
	store := NewPolicyStore(root)
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	if err := store.ApplyRefresh(DefaultPolicy(), true, now); err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(root, QueueOptions{Now: func() time.Time { return now }})
	reporter := NewReporter(ReporterConfig{Queue: queue, Settings: NewSettingsStore(root), Policies: store})

	recorded, err := reporter.RecordContext(context.Background(), validFailureEvent("call-a"))
	if err != nil || !recorded {
		t.Fatalf("RecordContext() = (%v, %v), want (true, nil)", recorded, err)
	}
	if err := store.RecordRefreshFailure(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Event.PolicyFromValidTCC {
		t.Fatalf("queued event changed retroactively: %#v", items)
	}
}

func TestReporterUsesLatestUnhealthyStateForNewEvents(t *testing.T) {
	root := t.TempDir()
	store := NewPolicyStore(root)
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	if err := store.RecordRefreshFailure(now); err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(root, QueueOptions{Now: func() time.Time { return now }})
	reporter := NewReporter(ReporterConfig{Queue: queue, Settings: NewSettingsStore(root), Policies: store})

	if recorded, err := reporter.Record(validFailureEvent("call-a")); err != nil || !recorded {
		t.Fatalf("Record() = (%v, %v), want (true, nil)", recorded, err)
	}
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Event.PolicyFromValidTCC {
		t.Fatalf("queued event health = %#v, want false", items)
	}
}

func TestReporterDoesNotQueueSuccessOrLocallyDisabledTelemetry(t *testing.T) {
	root := t.TempDir()
	queue := NewQueue(root, QueueOptions{})
	settings := NewSettingsStore(root)
	reporter := NewReporter(ReporterConfig{Queue: queue, Settings: settings, Policies: NewPolicyStore(root)})

	success := validFailureEvent("call-success")
	success.Success = true
	success.ExitCode = 0
	success.FailureStage = ""
	success.FinalOutcome = OutcomeSuccess
	success.Category = ""
	success.ErrorCode = ""
	success.Fingerprint = ""
	if recorded, err := reporter.Record(success); err != nil || recorded {
		t.Fatalf("Record(success) = (%v, %v), want (false, nil)", recorded, err)
	}
	if err := settings.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if recorded, err := reporter.Record(validFailureEvent("call-failure")); err != nil || recorded {
		t.Fatalf("Record(disabled) = (%v, %v), want (false, nil)", recorded, err)
	}
	if queue.HasEvents() {
		t.Fatal("disabled reporter queued an event")
	}
}

func TestReporterRejectsDelayedRecordAfterDisableAndReenable(t *testing.T) {
	root := t.TempDir()
	settings := NewSettingsStore(root)
	original, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(root, QueueOptions{})
	reporter := NewReporter(ReporterConfig{Queue: queue, Settings: settings, Policies: NewPolicyStore(root)})
	if err := settings.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if recorded, err := reporter.RecordSelectedContext(context.Background(), validFailureEvent("before-disable"), original.Generation); err != nil || recorded {
		t.Fatalf("stale consent record = (%v, %v), want (false, nil)", recorded, err)
	}
	if queue.HasEvents() {
		t.Fatal("delayed event returned after disable/re-enable")
	}
	current, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	event := validFailureEvent("after-reenable")
	event.SampleRate, event.SampleWeight = BasisPoints, 1
	if recorded, err := reporter.RecordSelectedContext(context.Background(), event, current.Generation); err != nil || !recorded {
		t.Fatalf("current consent record = (%v, %v), want (true, nil)", recorded, err)
	}
}

func TestReporterRemovesDelayedRecordWhenConsentChangesDuringWrite(t *testing.T) {
	root := t.TempDir()
	settings := NewSettingsStore(root)
	original, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(root, QueueOptions{})
	reporter := NewReporter(ReporterConfig{
		Queue: queue, Settings: settings, Policies: NewPolicyStore(root),
		NextSequence: func(context.Context) (uint64, error) {
			if err := settings.SetEnabled(false); err != nil {
				return 0, err
			}
			return 1, settings.SetEnabled(true)
		},
	})
	event := validFailureEvent("during-disable")
	event.SampleRate, event.SampleWeight = BasisPoints, 1
	if recorded, err := reporter.RecordSelectedContext(context.Background(), event, original.Generation); err != nil || recorded {
		t.Fatalf("changed consent record = (%v, %v), want (false, nil)", recorded, err)
	}
	if queue.HasEvents() {
		t.Fatal("record survived a consent change during persistence")
	}
}

func TestDelayedRecordPreservesSelectedSamplingAndPolicyHealth(t *testing.T) {
	root := t.TempDir()
	policies := NewPolicyStore(root)
	if err := policies.ApplyRefresh(DefaultPolicy(), true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := policies.RecordRefreshFailure(time.Now()); err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(root, QueueOptions{})
	reporter := NewReporter(ReporterConfig{Queue: queue, Settings: NewSettingsStore(root), Policies: policies})
	selected := validFailureEvent("selected-before-refresh")
	selected.SampleRate, selected.SampleWeight, selected.PolicyFromValidTCC = 1000, 10, true
	if recorded, err := reporter.RecordSelectedContext(context.Background(), selected, 0); err != nil || !recorded {
		t.Fatalf("selected record = (%v, %v), want (true, nil)", recorded, err)
	}
	items, err := queue.List()
	if err != nil || len(items) != 1 {
		t.Fatalf("selected queue=%#v err=%v", items, err)
	}
	if items[0].Event.SampleRate != 1000 || items[0].Event.SampleWeight != 10 || !items[0].Event.PolicyFromValidTCC {
		t.Fatalf("worker relabelled selected event: %#v", items[0].Event)
	}
}

func validFailureEvent(callID string) Event {
	return Event{
		SchemaVersion:       SchemaVersion,
		Kind:                EventKindCommand,
		CallID:              callID,
		OccurredAtMS:        time.Now().UnixMilli(),
		Command:             "workitem get",
		ExitCode:            1,
		FailureStage:        StageResponseWait,
		FinalOutcome:        OutcomeBackendError,
		BackendReachability: ReachabilityYes,
		Category:            CategoryBackend,
		ErrorCode:           "BACKEND_ERROR",
		HostClass:           HostClassMeego,
		ClientType:          ClientTypeInteractiveCLI,
		CredentialSource:    CredentialSourceStore,
		Fingerprint:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}
