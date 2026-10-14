// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultMaxQueueEvents = 1000
	DefaultMaxQueueBytes  = 5 * 1024 * 1024
	DefaultQueueRetention = 7 * 24 * time.Hour
	uploadRetryInterval   = time.Hour
	uploadAttemptFilename = ".last-upload-attempt"
)

var (
	ErrEventTooLarge = errors.New("telemetry event exceeds queue byte limit")
	ErrQueueBusy     = errors.New("telemetry queue is busy")
)

type QueueOptions struct {
	// IdentityKey binds local delivery to credential provenance; it is not sent.
	IdentityKey string
	MaxEvents   int
	MaxBytes    int64
	Retention   time.Duration
	LockTimeout time.Duration
	Now         func() time.Time
}

type queuedEnvelope struct {
	IdentityKey    string    `json:"identity_key,omitempty"`
	QueuedAt       time.Time `json:"queued_at"`
	Sequence       uint64    `json:"sequence,omitempty"`
	GlobalSequence uint64    `json:"global_sequence,omitempty"`
	Event          Event     `json:"event"`
}

type QueuedEvent struct {
	IdentityKey    string
	ID             string
	QueuedAt       time.Time
	Sequence       uint64
	GlobalSequence uint64
	Event          Event
	Size           int64
}

type Queue struct {
	root    string
	dir     string
	lockDir string
	options QueueOptions
}

func NewQueue(root string, options QueueOptions) *Queue {
	if options.MaxEvents <= 0 {
		options.MaxEvents = DefaultMaxQueueEvents
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultMaxQueueBytes
	}
	if options.Retention <= 0 {
		options.Retention = DefaultQueueRetention
	}
	if options.LockTimeout <= 0 {
		options.LockTimeout = 2 * time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	dir := filepath.Join(root, "queue")
	return &Queue{root: root, dir: dir, lockDir: filepath.Join(root, ".queue-lock"), options: options}
}

func (q *Queue) Enqueue(event Event) error {
	_, err := q.EnqueueWithID(event)
	return err
}

func (q *Queue) EnqueueWithID(event Event) (string, error) {
	id, _, err := q.enqueueWithID(event, 0)
	return id, err
}

func (q *Queue) enqueueWithID(event Event, globalSequence uint64) (string, bool, error) {
	return q.enqueueWithIDContext(context.Background(), event, globalSequence)
}

func (q *Queue) enqueueWithIDContext(ctx context.Context, event Event, globalSequence uint64) (string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var storedID string
	var created bool
	err := q.withLockContext(ctx, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(q.dir, 0700); err != nil {
			return err
		}
		if err := os.Chmod(q.dir, 0700); err != nil {
			return err
		}
		event = event.Normalize()
		if err := event.Validate(); err != nil {
			return err
		}
		existingID, err := q.findByCallIDUnlocked(event.CallID)
		if err != nil {
			return err
		}
		if existingID != "" {
			storedID = existingID
			return nil
		}
		sequence := globalSequence
		if sequence == 0 {
			sequence, err = nextQueueSequence(filepath.Join(q.root, ".sequence"))
			if err != nil {
				return err
			}
		}
		envelope := queuedEnvelope{
			IdentityKey: q.options.IdentityKey,
			QueuedAt:    q.options.Now().UTC(), Sequence: sequence,
			GlobalSequence: globalSequence, Event: event,
		}
		data, err := json.Marshal(envelope)
		if err != nil {
			return err
		}
		if int64(len(data)) > q.options.MaxBytes {
			return ErrEventTooLarge
		}
		temp, err := os.CreateTemp(q.dir, ".event-*")
		if err != nil {
			return err
		}
		tempPath := temp.Name()
		cleanup := func() {
			_ = temp.Close()
			_ = os.Remove(tempPath)
		}
		if err := temp.Chmod(0600); err != nil {
			cleanup()
			return err
		}
		if _, err := temp.Write(data); err != nil {
			cleanup()
			return err
		}
		if err := temp.Close(); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
		id := queueEventID(event.CallID)
		if err := os.Rename(tempPath, filepath.Join(q.dir, id)); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
		// Completed event writes survive process exit without forcing stable
		// storage. Power loss or an OS crash can lose recently queued events.
		storedID = id
		created = true
		return nil
	})
	return storedID, created, err
}

func (q *Queue) List() ([]QueuedEvent, error) {
	return q.ListContext(context.Background())
}

func (q *Queue) ListContext(ctx context.Context) ([]QueuedEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Committed files are immutable. Scanning them does not hold the writer
	// lock; only the short deletion of a selected event needs that lock.
	return q.prune(ctx)
}

// HasEvents is a cheap foreground hint used to decide whether a successful
// command should wake the uploader for previously cached failures. Races are
// harmless: the uploader reads its own snapshot before sending.
func (q *Queue) HasEvents() bool {
	dir, err := os.Open(q.dir)
	if err != nil {
		return false
	}
	defer func() { _ = dir.Close() }()
	for {
		entries, err := dir.ReadDir(1)
		if len(entries) > 0 && entries[0].Type().IsRegular() && strings.HasSuffix(entries[0].Name(), ".json") {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// UploadDue limits retries for a route while preserving immediate delivery
// after its queue has been drained successfully.
func (q *Queue) UploadDue(now time.Time) bool {
	data, err := os.ReadFile(filepath.Join(q.root, uploadAttemptFilename))
	if err != nil {
		return true
	}
	milliseconds, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return true
	}
	lastAttempt := time.UnixMilli(milliseconds)
	if lastAttempt.After(now.Add(uploadRetryInterval)) {
		return true
	}
	return !now.Before(lastAttempt.Add(uploadRetryInterval))
}

func (q *Queue) RecordUploadAttempt(attemptedAt time.Time) error {
	return atomicWriteFile(
		filepath.Join(q.root, uploadAttemptFilename),
		[]byte(strconv.FormatInt(attemptedAt.UnixMilli(), 10)),
		0600,
	)
}

func (q *Queue) ClearUploadAttempt() error {
	err := os.Remove(filepath.Join(q.root, uploadAttemptFilename))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (q *Queue) findByCallIDUnlocked(callID string) (string, error) {
	id := queueEventID(callID)
	if _, err := os.Stat(filepath.Join(q.dir, id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return id, nil
}

func queueEventID(callID string) string {
	// Hashing also keeps distinct case-sensitive IDs distinct on macOS/Windows.
	return fmt.Sprintf("%x.json", sha256.Sum256([]byte(callID)))
}

func (q *Queue) Ack(ids []string) error {
	return q.AckContext(context.Background(), ids)
}

func (q *Queue) AckContext(ctx context.Context, ids []string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return q.withLockContext(ctx, func() error {
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			base := filepath.Base(id)
			if base != id || !strings.HasSuffix(base, ".json") {
				return fmt.Errorf("invalid queue event id %q", id)
			}
			if err := os.Remove(filepath.Join(q.dir, base)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	})
}

func (q *Queue) Clear() error {
	return q.withLock(func() error {
		entries, err := os.ReadDir(q.dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
				if err := os.Remove(filepath.Join(q.dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
		return nil
	})
}

func (q *Queue) prune(ctx context.Context) ([]QueuedEvent, error) {
	if err := q.cleanupTemps(ctx); err != nil {
		return nil, err
	}
	items, err := q.list(ctx)
	if err != nil {
		return nil, err
	}
	now := q.options.Now()
	cutoff := now.Add(-q.options.Retention)
	// Allow ordinary wall-clock corrections without deleting valid events, but
	// reject envelopes more than a full retention window in the future.
	futureCutoff := now.Add(q.options.Retention)
	kept := make([]QueuedEvent, 0, len(items))
	var totalBytes int64
	for _, item := range items {
		if item.QueuedAt.IsZero() || item.QueuedAt.Before(cutoff) || item.QueuedAt.After(futureCutoff) {
			if err := q.AckContext(ctx, []string{item.ID}); err != nil {
				return nil, err
			}
			continue
		}
		kept = append(kept, item)
		totalBytes += item.Size
	}
	for len(kept) > q.options.MaxEvents || totalBytes > q.options.MaxBytes {
		oldest := kept[0]
		if err := q.AckContext(ctx, []string{oldest.ID}); err != nil {
			return nil, err
		}
		totalBytes -= oldest.Size
		kept = kept[1:]
	}
	return kept, ctx.Err()
}

func (q *Queue) list(ctx context.Context) ([]QueuedEvent, error) {
	entries, err := os.ReadDir(q.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]QueuedEvent, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(q.dir, entry.Name())
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var envelope queuedEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			_ = os.Remove(path)
			continue
		}
		envelope.Event = envelope.Event.Normalize()
		if err := envelope.Event.Validate(); err != nil {
			_ = os.Remove(path)
			continue
		}
		items = append(items, QueuedEvent{
			IdentityKey: envelope.IdentityKey,
			ID:          entry.Name(), QueuedAt: envelope.QueuedAt, Sequence: envelope.Sequence,
			GlobalSequence: envelope.GlobalSequence, Event: envelope.Event, Size: int64(len(data)),
		})
	}
	sort.Slice(items, func(left, right int) bool {
		// Sequence preserves FIFO when the clock moves backwards. Missing or
		// equal sequences use timestamps and filenames for deterministic order.
		if items[left].Sequence > 0 && items[right].Sequence > 0 && items[left].Sequence != items[right].Sequence {
			return items[left].Sequence < items[right].Sequence
		}
		if items[left].QueuedAt.Equal(items[right].QueuedAt) {
			if items[left].Sequence != items[right].Sequence {
				return items[left].Sequence < items[right].Sequence
			}
			return items[left].ID < items[right].ID
		}
		return items[left].QueuedAt.Before(items[right].QueuedAt)
	})
	return items, nil
}

func (q *Queue) withLock(operation func() error) error {
	return q.withLockContext(context.Background(), operation)
}

func (q *Queue) withLockContext(parent context.Context, operation func() error) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, q.options.LockTimeout)
	defer cancel()
	release, err := NewProcessLock(q.lockDir).Acquire(ctx)
	if err != nil {
		if parentErr := parent.Err(); parentErr != nil {
			return parentErr
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return ErrQueueBusy
		}
		return err
	}
	defer release()
	if err := parent.Err(); err != nil {
		return err
	}
	return operation()
}

func (q *Queue) cleanupTemps(ctx context.Context) error {
	entries, err := os.ReadDir(q.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), ".event-") {
			// A live writer owns this lock until rename. Once acquired, any
			// remaining temporary file belongs to an interrupted write.
			if err := q.withLockContext(ctx, func() error {
				err := os.Remove(filepath.Join(q.dir, entry.Name()))
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
