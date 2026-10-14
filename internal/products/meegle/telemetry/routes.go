// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type routedQueuedEvent struct {
	queue *Queue
	item  QueuedEvent
}

const routedSequenceFilename = ".global-sequence"

// PruneRoutedQueuesContext enforces the quota on a background snapshot.
// Concurrent writers may temporarily exceed it until the next cleanup.
func PruneRoutedQueuesContext(ctx context.Context, routesRoot string) error {
	return pruneRoutedQueuesContext(ctx, routesRoot, DefaultMaxQueueEvents, DefaultMaxQueueBytes)
}

// NextRoutedQueueSequenceContext allocates a monotonic sequence across processes.
// Only the counter update is serialized across routes; event writes may
// proceed while another process allocates its sequence. Gaps are harmless.
// Power loss or an OS crash may roll back this buffered counter.
func NextRoutedQueueSequenceContext(ctx context.Context, routesRoot string) (uint64, error) {
	release, err := NewProcessLock(filepath.Join(routesRoot, ".sequence-lock")).Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	return nextQueueSequence(filepath.Join(routesRoot, routedSequenceFilename))
}

func nextQueueSequence(path string) (uint64, error) {
	var current uint64
	if data, err := os.ReadFile(path); err == nil {
		parsed, parseErr := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if parseErr != nil {
			return 0, parseErr
		}
		current = parsed
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if current == ^uint64(0) {
		return 0, errors.New("global queue sequence exhausted")
	}
	next := current + 1
	if err := atomicWriteFileBuffered(path, []byte(strconv.FormatUint(next, 10)), 0600); err != nil {
		return 0, err
	}
	return next, nil
}

func pruneRoutedQueuesContext(ctx context.Context, routesRoot string, maxEvents int, maxBytes int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	entries, err := os.ReadDir(routesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	all := make([]routedQueuedEvent, 0)
	var totalBytes int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			continue
		}
		routeRoot := filepath.Join(routesRoot, entry.Name())
		cleanupStaleAtomicTemps(routeRoot, time.Now().Add(-DefaultQueueRetention))
		queue := NewQueue(routeRoot, QueueOptions{})
		items, listErr := queue.ListContext(ctx)
		if listErr != nil {
			return listErr
		}
		for _, item := range items {
			all = append(all, routedQueuedEvent{queue: queue, item: item})
			totalBytes += item.Size
		}
	}
	sort.Slice(all, func(left, right int) bool {
		if all[left].item.GlobalSequence > 0 && all[right].item.GlobalSequence > 0 &&
			all[left].item.GlobalSequence != all[right].item.GlobalSequence {
			return all[left].item.GlobalSequence < all[right].item.GlobalSequence
		}
		if all[left].item.QueuedAt.Equal(all[right].item.QueuedAt) {
			if all[left].item.Sequence != all[right].item.Sequence {
				return all[left].item.Sequence < all[right].item.Sequence
			}
			return all[left].item.ID < all[right].item.ID
		}
		return all[left].item.QueuedAt.Before(all[right].item.QueuedAt)
	})
	for len(all) > maxEvents || totalBytes > maxBytes {
		oldest := all[0]
		if ackErr := oldest.queue.AckContext(ctx, []string{oldest.item.ID}); ackErr != nil {
			return ackErr
		}
		totalBytes -= oldest.item.Size
		all = all[1:]
	}
	return nil
}
