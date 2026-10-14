// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package meegle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	telemetryevent "github.com/larksuite/meegle-cli/internal/products/meegle/telemetry"
)

func TestTelemetryDisableWaitsForActiveRecordAndReenableStartsEmpty(t *testing.T) {
	setupTestDir(t)
	t.Setenv(telemetryControlEnv, "")
	t.Setenv("MEEGLE_HOST", "127.0.0.1:9")
	settings := telemetryevent.NewSettingsStore(telemetryConfigRoot())
	if err := settings.MarkDisclosureShown(telemetryDisclosureVersion); err != nil {
		t.Fatal(err)
	}
	tracker := StartCommandTelemetry([]string{"config", "get"})
	event := tracker.eventFromResult(context.DeadlineExceeded, 1, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Pause a real record at its persistent sequence boundary.
	releaseSequence, err := telemetryevent.NewProcessLock(filepath.Join(telemetryRoutesRoot(), ".sequence-lock")).Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sequenceHeld := true
	defer func() {
		if sequenceHeld {
			releaseSequence()
		}
	}()
	recordDone := make(chan error, 1)
	go func() {
		queued, err := tracker.reporter.RecordContext(ctx, event)
		if queued {
			err = errors.New("record acknowledged after disable")
		}
		recordDone <- err
	}()
	for {
		if err := ctx.Err(); err != nil {
			t.Fatal("record did not enter commit guard")
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Millisecond)
		release, err := telemetryevent.NewProcessLock(telemetryQuotaLockDir()).Acquire(probeCtx)
		probeCancel()
		if errors.Is(err, context.DeadlineExceeded) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		release()
		time.Sleep(time.Millisecond)
	}
	disableDone := make(chan error, 1)
	go func() { disableDone <- SetTelemetryEnabled(false) }()
	for {
		state, err := settings.Load()
		if err != nil {
			t.Fatal(err)
		}
		if !state.Enabled {
			break
		}
		if err := ctx.Err(); err != nil {
			t.Fatal("disable did not persist the setting")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-disableDone:
		t.Fatalf("disable returned before active record ended: %v", err)
	default:
	}
	releaseSequence()
	sequenceHeld = false
	if err := <-recordDone; err != nil {
		t.Fatal(err)
	}
	if err := <-disableDone; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(telemetryRoutesRoot()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disable left pending data: %v", err)
	}
	if queued, err := tracker.reporter.Record(event); queued || err != nil {
		t.Fatalf("disabled record = (%v, %v)", queued, err)
	}
	if err := SetTelemetryEnabled(true); err != nil {
		t.Fatal(err)
	}
	event.CallID = "after-reenable"
	if queued, err := tracker.reporter.Record(event); !queued || err != nil {
		t.Fatalf("re-enabled record = (%v, %v)", queued, err)
	}
	items, err := telemetryevent.NewQueue(tracker.route.Root, telemetryevent.QueueOptions{}).List()
	if err != nil || len(items) != 1 || items[0].Event.CallID != "after-reenable" {
		t.Fatalf("re-enable recovered prior data: items=%v err=%v", items, err)
	}
}

func TestTelemetryCleanupFailureDoesNotBlockHealthyRouteDelivery(t *testing.T) {
	setupTestDir(t)
	t.Setenv(telemetryControlEnv, "")
	if err := telemetryevent.NewSettingsStore(telemetryConfigRoot()).MarkDisclosureShown(telemetryDisclosureVersion); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEEGLE_USER_ACCESS_TOKEN", "local-test-token")
	var reports, policies atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case telemetryevent.PolicyPath:
			policies.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"policy": telemetryevent.DefaultPolicy(), "policy_from_valid_tcc": true})
		case telemetryevent.ReportPath:
			reports.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"failed_call_ids": []string{}})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("MEEGLE_HOST", strings.TrimPrefix(server.URL, "https://"))
	identity, err := resolveCLIIdentity(context.Background(), MeegleConfig{}, "default", false)
	if err != nil {
		t.Fatal(err)
	}
	route := telemetryRouteForIdentity(identity)
	tracker := StartCommandTelemetry([]string{"config", "get"})
	// Prepare pending data independently of the foreground collection budget.
	event := tracker.eventFromResult(context.DeadlineExceeded, 1, false)
	event.SampleRate, event.SampleWeight = telemetryevent.BasisPoints, 1
	queue := telemetryevent.NewQueue(route.Root, telemetryevent.QueueOptions{IdentityKey: route.IdentityKey})
	if err := queue.Enqueue(event); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(telemetryRoutesRoot(), "broken", "queue")
	if err := os.MkdirAll(filepath.Dir(broken), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runTelemetryMaintenanceWithHTTPClient(context.Background(), server.Client()); err == nil {
		t.Fatal("cleanup failure was not returned")
	}
	if reports.Load() != 1 || policies.Load() != 1 {
		t.Fatalf("unrelated queue blocked delivery: reports=%d policies=%d", reports.Load(), policies.Load())
	}
	if queue.HasEvents() {
		t.Fatal("successful report was not acknowledged")
	}
}

func TestTelemetryMaintenanceCleansBacklogWithoutCredentialsDuringUploadCooldown(t *testing.T) {
	setupTestDir(t)
	t.Setenv(telemetryControlEnv, "")
	t.Setenv("MEEGLE_HOST", "127.0.0.1:9")
	settings := telemetryevent.NewSettingsStore(telemetryConfigRoot())
	if err := settings.MarkDisclosureShown(telemetryDisclosureVersion); err != nil {
		t.Fatal(err)
	}
	tracker := StartCommandTelemetry([]string{"config", "get"})
	queue := telemetryevent.NewQueue(tracker.route.Root, telemetryevent.QueueOptions{})
	queueDir := filepath.Join(tracker.route.Root, "queue")
	if err := os.MkdirAll(queueDir, 0700); err != nil {
		t.Fatal(err)
	}
	event := tracker.eventFromResult(context.DeadlineExceeded, 1, false)
	event.SampleRate, event.SampleWeight = telemetryevent.BasisPoints, 1
	for index := 0; index < 3; index++ {
		event.CallID = fmt.Sprintf("seed-%04d", index)
		queuedAt := time.Now().Add(-time.Hour)
		if index < 2 {
			queuedAt = time.Now().Add(-8 * 24 * time.Hour)
		}
		data, err := json.Marshal(map[string]any{
			"event": event, "queued_at": queuedAt, "sequence": index + 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(queueDir, event.CallID+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := queue.RecordUploadAttempt(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := telemetryevent.NewPolicyStore(tracker.route.PolicyRoot).ApplyRefresh(telemetryevent.DefaultPolicy(), true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := RunTelemetryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Inspect the durable boundary before List(), which can itself prune.
	entries, err := os.ReadDir(queueDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "seed-0002.json" {
		t.Fatalf("maintenance left %d files, err=%v", len(entries), err)
	}
	if _, err := os.Stat(filepath.Join(queueDir, "seed-0000.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oldest event was not evicted: %v", err)
	}
	if telemetryCleanupDue(time.Now()) || queue.UploadDue(time.Now()) {
		t.Fatal("cleanup bypassed the independent cleanup/upload cooldowns")
	}
	if err := SetTelemetryEnabled(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(telemetryRoutesRoot()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disable did not remove pending queues: %v", err)
	}
}

func TestTelemetryMissingCredentialsBackOffWithoutDroppingEvents(t *testing.T) {
	setupTestDir(t)
	t.Setenv(telemetryControlEnv, "")
	t.Setenv("MEEGLE_HOST", "127.0.0.1:9")
	if err := telemetryevent.NewSettingsStore(telemetryConfigRoot()).MarkDisclosureShown(telemetryDisclosureVersion); err != nil {
		t.Fatal(err)
	}
	tracker := StartCommandTelemetry([]string{"config", "get"})
	recorded, err := tracker.reporter.Record(tracker.eventFromResult(context.DeadlineExceeded, 1, false))
	if err != nil || !recorded {
		t.Fatalf("recorded=%v err=%v", recorded, err)
	}
	if err := RunTelemetryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	queue := telemetryevent.NewQueue(tracker.route.Root, telemetryevent.QueueOptions{})
	items, err := queue.List()
	if err != nil || len(items) != 1 || queue.UploadDue(time.Now()) {
		t.Fatalf("missing credentials lost data or skipped cooldown: items=%d err=%v", len(items), err)
	}
}

func TestTelemetryPolicyCacheIsHostOnly(t *testing.T) {
	one := telemetryPolicyRoot("https://project.feishu.cn/")
	two := telemetryPolicyRoot("project.feishu.cn")
	other := telemetryPolicyRoot("meegle.com")

	if one != two {
		t.Fatalf("same host produced different policy roots: %q != %q", one, two)
	}
	if one == other {
		t.Fatalf("different hosts produced the same policy root: %q", one)
	}
	if strings.Contains(one, "project.feishu.cn") || filepath.Base(one) == "" {
		t.Fatalf("policy root exposes raw host or has no route key: %q", one)
	}
}

func TestTelemetryMaintenanceRunsRefreshAndUploadIndependently(t *testing.T) {
	refreshErr := errors.New("refresh failed")
	uploadErr := errors.New("upload failed")
	refreshCalls, uploadCalls := 0, 0
	err := runTelemetryMaintenanceFlows(context.Background(), time.Second, func(context.Context) error {
		refreshCalls++
		return refreshErr
	}, func(context.Context) error {
		uploadCalls++
		return uploadErr
	})
	if refreshCalls != 1 || uploadCalls != 1 {
		t.Fatalf("calls = refresh:%d upload:%d", refreshCalls, uploadCalls)
	}
	if !errors.Is(err, refreshErr) || !errors.Is(err, uploadErr) {
		t.Fatalf("joined error = %v", err)
	}
}

func TestTelemetryMaintenanceContainsPanicAndGivesOtherFlowItsOwnDeadline(t *testing.T) {
	uploaded := make(chan struct{}, 1)
	err := runTelemetryMaintenanceFlows(context.Background(), 50*time.Millisecond,
		func(context.Context) error { panic("refresh bug") },
		func(ctx context.Context) error {
			uploaded <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		},
	)
	select {
	case <-uploaded:
	default:
		t.Fatal("upload flow did not start after refresh panic")
	}
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("maintenance error = %v", err)
	}
}

func TestTelemetryMaintenanceArgumentIsExact(t *testing.T) {
	if !IsTelemetryMaintenanceArgs([]string{"__telemetry-maintain"}) {
		t.Fatal("maintenance command was not recognized")
	}
	for _, args := range [][]string{nil, {"__telemetry-upload"}, {"__telemetry-maintain", "extra"}} {
		if IsTelemetryMaintenanceArgs(args) {
			t.Fatalf("unexpected maintenance args accepted: %#v", args)
		}
	}
}

func TestTelemetryRecordQueuesBeforeMaintenanceLockAndRespectsUploadCooldown(t *testing.T) {
	for _, mode := range []string{"maintenance-busy", "upload-cooldown", "upload-due"} {
		t.Run(mode, func(t *testing.T) {
			setupTelemetryCommandRuntime(t)
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != telemetryevent.ReportPath {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"failed_call_ids": []string{}})
			}))
			defer server.Close()
			t.Setenv("MEEGLE_HOST", strings.TrimPrefix(server.URL, "https://"))
			t.Setenv("MEEGLE_USER_ACCESS_TOKEN", "local-test-token")
			identity, err := resolveCLIIdentity(context.Background(), MeegleConfig{}, "default", false)
			if err != nil {
				t.Fatal(err)
			}
			tracker := StartCommandTelemetry([]string{"config", "get"})
			observeTelemetryIdentity(tracker.Context(context.Background()), identity)
			route := tracker.route
			now := time.Now()
			if err := telemetryevent.NewPolicyStore(route.PolicyRoot).ApplyRefresh(telemetryevent.DefaultPolicy(), true, now); err != nil {
				t.Fatal(err)
			}
			queue := telemetryevent.NewQueue(route.Root, telemetryevent.QueueOptions{})
			if mode == "upload-cooldown" {
				if err := queue.RecordUploadAttempt(now); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(telemetryCacheRoot(), ".last-cleanup"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "maintenance-busy" {
				release, err := telemetryevent.NewProcessLock(telemetryMaintenanceLockDir()).Acquire(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			event := tracker.eventFromResult(context.DeadlineExceeded, 1, false)
			event.SampleRate, event.SampleWeight, event.PolicyFromValidTCC = telemetryevent.BasisPoints, 1, true
			data, err := json.Marshal(telemetryRecord{Event: event, Profile: route.Profile, Host: route.Host, IdentityKey: route.IdentityKey, Generation: tracker.consentGeneration})
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(telemetryRecordEnv, string(data))
			t.Setenv(telemetryProfileEnv, route.Profile)
			t.Setenv(telemetryRouteEnv, route.Key)
			if err := runTelemetryMaintenanceWithHTTPClient(context.Background(), server.Client()); err != nil {
				t.Fatal(err)
			}
			if os.Getenv(telemetryRecordEnv) != "" {
				t.Fatal("worker left a record in its child environment")
			}
			items, err := queue.List()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "upload-due" {
				if requests.Load() != 1 || len(items) != 0 {
					t.Fatalf("record delivery: requests=%d queued=%d", requests.Load(), len(items))
				}
			} else {
				if requests.Load() != 0 || len(items) != 1 || items[0].Event.CallID != event.CallID || items[0].IdentityKey != route.IdentityKey {
					t.Fatalf("record retention: requests=%d queued=%#v", requests.Load(), items)
				}
			}
		})
	}
}

func TestTelemetryRecordRejectsInvalidOrCanceledHandoff(t *testing.T) {
	for _, mode := range []string{"malformed", "oversized", "route-drift", "invalid-event", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			setupTelemetryCommandRuntime(t)
			tracker := StartCommandTelemetry([]string{"config", "get"})
			record := telemetryRecord{Event: tracker.eventFromResult(context.DeadlineExceeded, 1, false), Profile: tracker.route.Profile, Host: tracker.route.Host, Generation: tracker.consentGeneration}
			record.Event.SampleRate, record.Event.SampleWeight = telemetryevent.BasisPoints, 1
			if mode == "invalid-event" {
				record.Event.CallID = ""
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "malformed" {
				data = []byte("{")
			} else if mode == "oversized" {
				data = []byte(strings.Repeat("x", 2*telemetryevent.MaxEventBytes+1))
			}
			t.Setenv(telemetryRecordEnv, string(data))
			t.Setenv(telemetryProfileEnv, tracker.route.Profile)
			t.Setenv(telemetryRouteEnv, tracker.route.Key)
			if mode == "route-drift" {
				t.Setenv(telemetryRouteEnv, "other-route")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if err := RunTelemetryMaintenance(ctx); err == nil {
				t.Fatal("invalid or canceled handoff was acknowledged")
			}
			if telemetryevent.NewQueue(tracker.route.Root, telemetryevent.QueueOptions{}).HasEvents() {
				t.Fatal("invalid or canceled handoff left an event")
			}
		})
	}
}
