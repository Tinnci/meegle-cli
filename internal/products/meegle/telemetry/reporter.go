// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type ReporterConfig struct {
	Queue        *Queue
	Settings     *SettingsStore
	Policies     *PolicyStore
	CommitLock   *ProcessLock
	NextSequence func(context.Context) (uint64, error)
}

type Reporter struct {
	queue        *Queue
	settings     *SettingsStore
	policies     *PolicyStore
	commitLock   *ProcessLock
	nextSequence func(context.Context) (uint64, error)
}

func NewReporter(config ReporterConfig) *Reporter {
	return &Reporter{
		queue: config.Queue, settings: config.Settings, policies: config.Policies,
		commitLock: config.CommitLock, nextSequence: config.NextSequence,
	}
}

func (reporter *Reporter) Record(event Event) (bool, error) {
	return reporter.RecordContext(context.Background(), event)
}

func (reporter *Reporter) RecordContext(ctx context.Context, event Event) (bool, error) {
	return reporter.recordContext(ctx, event, nil)
}

// RecordSelectedContext preserves the sampling decision made at handoff and
// rejects delayed events from an earlier consent state.
func (reporter *Reporter) RecordSelectedContext(ctx context.Context, event Event, generation uint64) (bool, error) {
	return reporter.recordContext(ctx, event, &generation)
}

func (reporter *Reporter) recordContext(ctx context.Context, event Event, generation *uint64) (bool, error) {
	if reporter == nil || reporter.queue == nil || reporter.settings == nil || reporter.policies == nil {
		return false, errors.New("telemetry reporter is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	settings, err := reporter.settings.Load()
	if err != nil || !settings.Enabled || (generation != nil && settings.Generation != *generation) {
		return false, err
	}
	state, err := reporter.policies.Load()
	if err != nil || !EffectiveEnabled(settings, state.Policy) {
		return false, err
	}
	event = event.Normalize()
	if generation == nil {
		rate, selected := state.Policy.SampleRate(event)
		if !selected || !ShouldSample(event.CallID, rate) {
			return false, nil
		}
		event.SampleRate = rate
		event.SampleWeight = float64(BasisPoints) / float64(rate)
		event.PolicyFromValidTCC = state.PolicyFromValidTCC
	} else if event.Success || event.SampleRate <= 0 || event.SampleWeight != float64(BasisPoints)/float64(event.SampleRate) {
		return false, errors.New("telemetry record has no valid sampling decision")
	}
	if err := event.Validate(); err != nil {
		return false, err
	}
	if payload, marshalErr := json.Marshal(event); marshalErr != nil {
		return false, marshalErr
	} else if len(payload) > MaxEventBytes {
		return false, ErrEventTooLarge
	}

	release := func() {}
	if reporter.commitLock != nil {
		lockCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		acquiredRelease, lockErr := reporter.commitLock.AcquireShared(lockCtx)
		cancel()
		if lockErr != nil {
			return false, lockErr
		}
		release = acquiredRelease
	}
	defer release()

	currentSettings, err := reporter.settings.Load()
	if err != nil || !currentSettings.Enabled || (generation != nil && currentSettings.Generation != *generation) {
		return false, err
	}
	var id string
	var created bool
	if reporter.nextSequence != nil {
		sequence, err := reporter.nextSequence(ctx)
		if err != nil {
			return false, err
		}
		id, created, err = reporter.queue.enqueueWithIDContext(ctx, event, sequence)
		if err != nil {
			return false, err
		}
	} else {
		var err error
		id, created, err = reporter.queue.enqueueWithIDContext(ctx, event, 0)
		if err != nil {
			return false, err
		}
	}
	latestSettings, err := reporter.settings.Load()
	if err != nil || !latestSettings.Enabled || (generation != nil && latestSettings.Generation != *generation) {
		if created {
			_ = reporter.queue.Ack([]string{id})
		}
		return false, err
	}
	return created, nil
}

func (reporter *Reporter) Enabled() (bool, error) {
	if reporter == nil || reporter.settings == nil || reporter.policies == nil {
		return false, errors.New("telemetry reporter is not configured")
	}
	settings, err := reporter.settings.Load()
	if err != nil || !settings.Enabled {
		return false, err
	}
	state, err := reporter.policies.Load()
	if err != nil {
		return false, err
	}
	return EffectiveEnabled(settings, state.Policy), nil
}
