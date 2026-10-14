// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/larksuite/meegle-cli/internal/products/meegle/httptransport"
)

func TestConcurrentRoutedReportersKeepSequenceAndDeduplicate(t *testing.T) {
	root := t.TempDir()
	routesRoot := filepath.Join(root, "routes")
	settings := NewSettingsStore(root)
	start := make(chan struct{})
	results := make(chan error, 24)
	var writers sync.WaitGroup
	for index := 0; index < 24; index++ {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			<-start
			routeRoot := filepath.Join(routesRoot, fmt.Sprintf("route-%d", index%2))
			reporter := NewReporter(ReporterConfig{
				Queue: NewQueue(routeRoot, QueueOptions{}), Settings: settings,
				Policies: NewPolicyStore(routeRoot), CommitLock: NewProcessLock(filepath.Join(root, ".quota-lock")),
				NextSequence: func(ctx context.Context) (uint64, error) {
					return NextRoutedQueueSequenceContext(ctx, routesRoot)
				},
			})
			_, err := reporter.Record(testEvent(fmt.Sprintf("call-%d", index%12)))
			results <- err
		}(index)
	}
	close(start)
	writers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	items := routedItemsForTest(t, routesRoot, "route-0", "route-1")
	if len(items) != 12 {
		t.Fatalf("durable unique events = %d, want 12", len(items))
	}
	sequences := make(map[uint64]bool)
	for _, item := range items {
		if item.GlobalSequence == 0 || sequences[item.GlobalSequence] {
			t.Fatalf("missing or duplicate global sequence: %d", item.GlobalSequence)
		}
		sequences[item.GlobalSequence] = true
	}
}

func TestGlobalSequencePreservesCrossRouteFIFOWhenClockMovesBackwards(t *testing.T) {
	root := t.TempDir()
	routesRoot := filepath.Join(root, "routes")
	settings := NewSettingsStore(root)
	quotaLock := NewProcessLock(filepath.Join(root, ".quota-lock"))
	now := time.Now().UTC().Add(-time.Hour)
	newReporter := func(route string) *Reporter {
		routeRoot := filepath.Join(routesRoot, route)
		return NewReporter(ReporterConfig{
			Queue:    NewQueue(routeRoot, QueueOptions{Now: func() time.Time { return now }}),
			Settings: settings, Policies: NewPolicyStore(routeRoot), CommitLock: quotaLock,
			NextSequence: func(ctx context.Context) (uint64, error) { return NextRoutedQueueSequenceContext(ctx, routesRoot) },
		})
	}
	if queued, err := newReporter("a").Record(testEvent("first-route-a")); err != nil || !queued {
		t.Fatalf("first Record() = (%v, %v)", queued, err)
	}
	now = now.Add(-time.Hour)
	if queued, err := newReporter("b").Record(testEvent("second-route-b")); err != nil || !queued {
		t.Fatalf("second Record() = (%v, %v)", queued, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := quotaLock.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := pruneRoutedQueuesContext(ctx, routesRoot, 1, 1<<20); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	items := routedItemsForTest(t, routesRoot, "a", "b")
	if len(items) != 1 || items[0].Event.CallID != "second-route-b" {
		t.Fatalf("cross-route clock rollback broke FIFO: %#v", items)
	}
}

func TestProductionRoutedQuotaEnforcesExactCountLimit(t *testing.T) {
	root := t.TempDir()
	routesRoot := filepath.Join(root, "routes")
	base := time.Now().UTC().Add(-time.Hour)
	for index := 0; index < DefaultMaxQueueEvents+1; index++ {
		route := "a"
		if index%2 == 1 {
			route = "b"
		}
		writeRoutedEnvelope(t, filepath.Join(routesRoot, route), index+1, base.Add(time.Duration(index)*time.Millisecond), 0)
	}
	if err := PruneRoutedQueuesContext(context.Background(), routesRoot); err != nil {
		t.Fatal(err)
	}
	items := routedItemsForTest(t, routesRoot, "a", "b")
	if len(items) != DefaultMaxQueueEvents {
		t.Fatalf("global count = %d, want %d", len(items), DefaultMaxQueueEvents)
	}
	for _, item := range items {
		if item.Event.CallID == "seed-0000" {
			t.Fatal("global count pruning did not evict the oldest event")
		}
	}
}

func TestProductionRoutedQuotaEnforcesExactByteLimit(t *testing.T) {
	root := t.TempDir()
	routesRoot := filepath.Join(root, "routes")
	base := time.Now().UTC().Add(-time.Hour)
	for index := 0; index < 6; index++ {
		route := "a"
		if index%2 == 1 {
			route = "b"
		}
		writeRoutedEnvelope(t, filepath.Join(routesRoot, route), index+1, base.Add(time.Duration(index)*time.Second), 1<<20)
	}
	if err := PruneRoutedQueuesContext(context.Background(), routesRoot); err != nil {
		t.Fatal(err)
	}
	items := routedItemsForTest(t, routesRoot, "a", "b")
	var total int64
	for _, item := range items {
		total += item.Size
	}
	if total > DefaultMaxQueueBytes {
		t.Fatalf("global bytes = %d, limit = %d", total, DefaultMaxQueueBytes)
	}
	if len(items) >= 6 {
		t.Fatalf("byte quota did not evict an old event: count=%d bytes=%d", len(items), total)
	}
}

func writeRoutedEnvelope(t *testing.T, routeRoot string, sequence int, queuedAt time.Time, padding int) {
	t.Helper()
	queueDir := filepath.Join(routeRoot, "queue")
	if err := os.MkdirAll(queueDir, 0700); err != nil {
		t.Fatal(err)
	}
	envelope := map[string]any{
		"queued_at": queuedAt, "sequence": sequence,
		"event": testEvent(fmt.Sprintf("seed-%04d", sequence-1)),
	}
	if padding > 0 {
		envelope["padding"] = strings.Repeat("x", padding)
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%020d-seed.json", sequence)
	if err := os.WriteFile(filepath.Join(queueDir, name), payload, 0600); err != nil {
		t.Fatal(err)
	}
}

func routedItemsForTest(t *testing.T, routesRoot string, routes ...string) []QueuedEvent {
	t.Helper()
	var all []QueuedEvent
	for _, route := range routes {
		items, err := NewQueue(filepath.Join(routesRoot, route), QueueOptions{}).List()
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, items...)
	}
	return all
}

func TestRoutedPruneCleansExpiredAtomicTempsFromInactiveRoute(t *testing.T) {
	root := t.TempDir()
	routeRoot := filepath.Join(root, "routes", "inactive")
	if err := os.MkdirAll(routeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	policyTemp := filepath.Join(routeRoot, ".telemetry-old-policy")
	if err := os.WriteFile(policyTemp, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(policyTemp, old, old); err != nil {
		t.Fatal(err)
	}
	if err := pruneRoutedQueuesContext(context.Background(), filepath.Join(root, "routes"), 10, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(policyTemp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired inactive-route policy temp still exists: %v", err)
	}
}

func TestUploaderDrainsOnlySelectedRoute(t *testing.T) {
	root := t.TempDir()
	rootA := filepath.Join(root, "routes", "profile-a")
	rootB := filepath.Join(root, "routes", "profile-b")
	queueA := NewQueue(rootA, QueueOptions{})
	queueB := NewQueue(rootB, QueueOptions{})
	if err := queueA.Enqueue(testEvent("event-a")); err != nil {
		t.Fatal(err)
	}
	if err := queueB.Enqueue(testEvent("event-b")); err != nil {
		t.Fatal(err)
	}
	uploader := NewUploader(UploaderConfig{
		Queue: queueB, Settings: NewSettingsStore(root),
		Transport: &fakeTelemetryTransport{
			responses: []*httptransport.Response{{
				StatusCode: http.StatusOK,
				Body:       []byte(`{"failed_call_ids":[],"BaseResp":{"StatusCode":0}}`),
			}},
		},
		LockDir: filepath.Join(root, ".send-lock"),
	})
	if err := uploader.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	itemsA, _ := queueA.List()
	itemsB, _ := queueB.List()
	if len(itemsA) != 1 || itemsA[0].Event.CallID != "event-a" || len(itemsB) != 0 {
		t.Fatalf("route isolation failed: A=%#v B=%#v", itemsA, itemsB)
	}
}
