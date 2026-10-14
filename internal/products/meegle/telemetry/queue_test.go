// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestQueueEvictsOldestByCountAndKeepsNewest(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	queue := NewQueue(t.TempDir(), QueueOptions{
		MaxEvents: 3,
		MaxBytes:  1 << 20,
		Retention: 7 * 24 * time.Hour,
		Now:       func() time.Time { return now },
	})

	for index := 1; index <= 4; index++ {
		event := testEvent(fmt.Sprintf("call-%d", index))
		if err := queue.Enqueue(event); err != nil {
			t.Fatalf("Enqueue(%d): %v", index, err)
		}
		now = now.Add(time.Second)
	}

	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("queue length = %d, want 3", len(items))
	}
	for index, want := range []string{"call-2", "call-3", "call-4"} {
		if items[index].Event.CallID != want {
			t.Fatalf("item %d call_id = %q, want %q", index, items[index].Event.CallID, want)
		}
	}
}

func TestQueuePreservesFIFOWhenTimestampsAreEqual(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	queue := NewQueue(t.TempDir(), QueueOptions{Now: func() time.Time { return now }})
	for _, id := range []string{"first", "second", "third"} {
		if err := queue.Enqueue(testEvent(id)); err != nil {
			t.Fatal(err)
		}
	}
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []string{"first", "second", "third"} {
		if items[index].Event.CallID != want {
			t.Fatalf("item %d = %q, want %q", index, items[index].Event.CallID, want)
		}
	}
}

func TestQueueKeepsOneEventPerCallID(t *testing.T) {
	queue := NewQueue(t.TempDir(), QueueOptions{})
	first := testEvent("same-call")
	first.DurationMS = 10
	firstID, err := queue.EnqueueWithID(first)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := first
	duplicate.DurationMS = 999
	duplicateID, err := queue.EnqueueWithID(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if firstID != duplicateID || len(items) != 1 || items[0].Event.DurationMS != 10 {
		t.Fatalf("firstID=%q duplicateID=%q items=%#v", firstID, duplicateID, items)
	}
}

func TestQueueCommittedEventSurvivesProcessExit(t *testing.T) {
	const rootEnv = "MEEGLE_TEST_COMMITTED_QUEUE_ROOT"
	const modeEnv = "MEEGLE_TEST_COMMITTED_QUEUE_EXIT"
	if root := os.Getenv(rootEnv); root != "" {
		if err := NewQueue(root, QueueOptions{}).Enqueue(testEvent("committed-before-exit")); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(os.Stdout, "queue committed"); err != nil {
			t.Fatal(err)
		}
		if os.Getenv(modeEnv) == "killed" {
			select {}
		}
		return
	}
	for _, mode := range []string{"normal", "killed"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQueueCommittedEventSurvivesProcessExit$", "-test.timeout=8s")
			child.Env = append(os.Environ(), rootEnv+"="+root, modeEnv+"="+mode)
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdout.Close()
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if child.ProcessState == nil {
					_ = child.Process.Kill()
					_ = child.Wait()
				}
			})
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "queue committed" {
				t.Fatalf("child did not commit an event: %q, %v", scanner.Text(), scanner.Err())
			}
			if mode == "killed" {
				if err := child.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := child.Wait(); err == nil {
					t.Fatal("terminated child unexpectedly exited successfully")
				}
			} else if err := child.Wait(); err != nil {
				t.Fatal(err)
			}
			items, err := NewQueue(root, QueueOptions{}).List()
			if err != nil || len(items) != 1 {
				t.Fatalf("queue after writer exit = %#v, %v", items, err)
			}
			if !reflect.DeepEqual(items[0].Event, testEvent("committed-before-exit").Normalize()) || items[0].Sequence != 1 || items[0].QueuedAt.IsZero() {
				t.Fatalf("committed event changed after writer exit: %#v", items[0])
			}
		})
	}
}

func TestQueuePreservesCaseSensitiveCallIDs(t *testing.T) {
	queue := NewQueue(t.TempDir(), QueueOptions{})
	for _, id := range []string{"Case-ID", "case-id"} {
		if err := queue.Enqueue(testEvent(id)); err != nil {
			t.Fatal(err)
		}
	}
	items, err := queue.List()
	if err != nil || len(items) != 2 {
		t.Fatalf("distinct call IDs = %#v, %v", items, err)
	}
}

func TestQueueSnapshotDoesNotWaitForForegroundWriter(t *testing.T) {
	root := t.TempDir()
	queue := NewQueue(root, QueueOptions{})
	if err := queue.Enqueue(testEvent("committed")); err != nil {
		t.Fatal(err)
	}
	release, err := NewProcessLock(queue.lockDir).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	items, err := queue.ListContext(ctx)
	if err != nil || len(items) != 1 || items[0].Event.CallID != "committed" {
		t.Fatalf("snapshot while writer owns lock = %#v, %v", items, err)
	}
}

func TestQueueCleanupPreservesActiveTemporaryWrite(t *testing.T) {
	queue := NewQueue(t.TempDir(), QueueOptions{})
	if err := queue.Enqueue(testEvent("committed")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(queue.dir, ".event-active")
	if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	release, err := NewProcessLock(queue.lockDir).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := queue.ListContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		release()
		t.Fatalf("cleanup should yield to active writer: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		release()
		t.Fatalf("background cleanup removed an active write: %v", err)
	}
	release()
	if _, err := queue.List(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted write was not reclaimed after lock release: %v", err)
	}
}

func TestConcurrentQueueWritersKeepOneEventPerCallID(t *testing.T) {
	queue := NewQueue(t.TempDir(), QueueOptions{})
	const writers = 16
	var group sync.WaitGroup
	errorsByWriter := make(chan error, writers)
	for index := 0; index < writers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsByWriter <- queue.Enqueue(testEvent("concurrent-same-call"))
		}()
	}
	group.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatal(err)
		}
	}
	items, err := queue.List()
	if err != nil || len(items) != 1 {
		t.Fatalf("queue = %#v, %v", items, err)
	}
}

func TestQueuePreservesFIFOWhenWallClockMovesBackwards(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	queue := NewQueue(t.TempDir(), QueueOptions{Now: func() time.Time { return now }})
	if err := queue.Enqueue(testEvent("first")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(-time.Hour)
	if err := queue.Enqueue(testEvent("second")); err != nil {
		t.Fatal(err)
	}
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Event.CallID != "first" || items[1].Event.CallID != "second" {
		t.Fatalf("clock rollback reordered FIFO: %#v", items)
	}
}

func TestQueueCleansInterruptedTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	queue := NewQueue(root, QueueOptions{})
	if err := os.MkdirAll(queue.dir, 0700); err != nil {
		t.Fatal(err)
	}
	tempPath := filepath.Join(queue.dir, ".event-interrupted")
	if err := os.WriteFile(tempPath, make([]byte, DefaultMaxQueueBytes), 0600); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(testEvent("after-interruption")); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.List(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted temp file still exists: %v", err)
	}
}

func TestAtomicWriteCleansOnlyExpiredGenericTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	oldTemp := filepath.Join(root, ".telemetry-old")
	recentTemp := filepath.Join(root, ".telemetry-recent")
	for _, path := range []string{oldTemp, recentTemp} {
		if err := os.WriteFile(path, []byte("interrupted"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldTemp, old, old); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(filepath.Join(root, "settings.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldTemp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired generic temp still exists: %v", err)
	}
	if _, err := os.Stat(recentTemp); err != nil {
		t.Fatalf("recent generic temp was removed: %v", err)
	}
}

func TestQueueDropsCorruptFilesInsteadOfBlockingNewEvents(t *testing.T) {
	root := t.TempDir()
	queue := NewQueue(root, QueueOptions{})
	if err := os.MkdirAll(filepath.Join(root, "queue"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "queue", "corrupt.json"), []byte(`{"event":{"command":"--token=secret"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(testEvent("after-corruption")); err != nil {
		t.Fatal(err)
	}
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Event.CallID != "after-corruption" {
		t.Fatalf("items = %#v, want only the valid new event", items)
	}
}

func TestEventAllowsClientSideCombinationCommandNames(t *testing.T) {
	event := testEvent("combination")
	event.Command = "attachment +download"
	if err := event.Validate(); err != nil {
		t.Fatalf("combination command rejected: %v", err)
	}
}

func TestQueueEvictsExpiredBeforeCapacity(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	queue := NewQueue(t.TempDir(), QueueOptions{
		MaxEvents: 10,
		MaxBytes:  1 << 20,
		Retention: time.Hour,
		Now:       func() time.Time { return now },
	})
	if err := queue.Enqueue(testEvent("expired")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if err := queue.Enqueue(testEvent("current")); err != nil {
		t.Fatal(err)
	}
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Event.CallID != "current" {
		t.Fatalf("items = %#v, want only current", items)
	}
}

func TestQueueDropsImplausiblyFutureEnvelope(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	queue := NewQueue(root, QueueOptions{Now: func() time.Time { return now }})
	if err := queue.Enqueue(testEvent("future")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(-8 * 24 * time.Hour)
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("future envelope survived cleanup: %#v", items)
	}
}

func TestQueueEvictsByBytesAndRejectsOversizedEvent(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	probeDir := t.TempDir()
	probe := NewQueue(probeDir, QueueOptions{MaxEvents: 10, MaxBytes: 1 << 20, Retention: time.Hour, Now: func() time.Time { return now }})
	if err := probe.Enqueue(testEvent("probe")); err != nil {
		t.Fatal(err)
	}
	probeItems, err := probe.List()
	if err != nil {
		t.Fatal(err)
	}
	oneEventBytes := probeItems[0].Size

	queue := NewQueue(t.TempDir(), QueueOptions{
		MaxEvents: 10,
		MaxBytes:  oneEventBytes*2 + 32,
		Retention: time.Hour,
		Now:       func() time.Time { return now },
	})
	for _, id := range []string{"one", "two", "three"} {
		if err := queue.Enqueue(testEvent(id)); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Event.CallID != "two" || items[1].Event.CallID != "three" {
		t.Fatalf("byte-bounded FIFO items = %#v", items)
	}

	oversized := testEvent("huge")
	oversized.FlagNames = make([]string, 64)
	for index := range oversized.FlagNames {
		oversized.FlagNames[index] = fmt.Sprintf("flag-%02d-%s", index, strings.Repeat("a", 45))
	}
	if err := queue.Enqueue(oversized); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("Enqueue(oversized) = %v, want ErrEventTooLarge", err)
	}
}

func TestQueueConcurrentWritersDoNotLoseNewEvents(t *testing.T) {
	queue := NewQueue(t.TempDir(), QueueOptions{MaxEvents: 100, MaxBytes: 1 << 20, Retention: time.Hour})
	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			if err := queue.Enqueue(testEvent(fmt.Sprintf("concurrent-%02d", index))); err != nil {
				t.Errorf("Enqueue(%d): %v", index, err)
			}
		}(index)
	}
	group.Wait()
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 20 {
		t.Fatalf("queue length = %d, want 20", len(items))
	}
}

func TestQueueContextDeadlineBoundsLockWait(t *testing.T) {
	root := t.TempDir()
	queue := NewQueue(root, QueueOptions{LockTimeout: time.Second})
	release, err := NewProcessLock(queue.lockDir).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := queue.AckContext(ctx, []string{"pending.json"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AckContext() = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("AckContext exceeded caller deadline: %v", elapsed)
	}
}

func testEvent(callID string) Event {
	return Event{
		SchemaVersion:       SchemaVersion,
		Kind:                EventKindCommand,
		CallID:              callID,
		Command:             "workitem get",
		Success:             false,
		DurationMS:          10,
		ExitCode:            2,
		FailureStage:        StageRequestSend,
		FinalOutcome:        OutcomeTransportError,
		BackendReachability: ReachabilityUnknown,
		Category:            CategoryTransport,
		ErrorCode:           "TRANSPORT_TIMEOUT",
		HostClass:           HostClassMeegle,
		ClientType:          ClientTypeInteractiveCLI,
		CredentialSource:    CredentialSourceStore,
		SampleRate:          BasisPoints,
		SampleWeight:        1,
		Fingerprint:         strings.Repeat("a", 64),
	}
}
