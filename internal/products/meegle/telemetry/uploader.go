// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"time"

	"github.com/larksuite/meegle-cli/internal/products/meegle/httptransport"
)

const (
	PolicyPath = "/goapi/v5/meeglecli/telemetry/policy"
	ReportPath = "/goapi/v5/meeglecli/telemetry/report"
)

var ErrInvalidAcknowledgement = errors.New("telemetry endpoint returned an invalid acknowledgement")

type telemetryTransport interface {
	Do(context.Context, httptransport.Request) (*httptransport.Response, error)
}

type UploaderConfig struct {
	Queue     *Queue
	Settings  *SettingsStore
	Transport telemetryTransport
	RequestID func() string
	LockDir   string
}

type Uploader struct {
	queue     *Queue
	settings  *SettingsStore
	transport telemetryTransport
	requestID func() string
	lockDir   string
}

type reportRequest struct {
	RequestID string  `json:"request_id"`
	Events    []Event `json:"events"`
}

type reportResponse struct {
	FailedCallIDs []string `json:"failed_call_ids"`
}

func NewUploader(config UploaderConfig) *Uploader {
	if config.RequestID == nil {
		config.RequestID = randomID
	}
	if config.LockDir == "" && config.Queue != nil {
		config.LockDir = filepath.Join(filepath.Dir(config.Queue.dir), ".uploader-lock")
	}
	return &Uploader{
		queue: config.Queue, settings: config.Settings,
		transport: config.Transport, requestID: config.RequestID, lockDir: config.LockDir,
	}
}

func (uploader *Uploader) Run(ctx context.Context) error {
	if uploader == nil || uploader.queue == nil || uploader.settings == nil {
		return errors.New("telemetry uploader is not configured")
	}
	if uploader.transport == nil || isNilTelemetryTransport(uploader.transport) {
		return nil
	}
	release, err := NewProcessLock(uploader.lockDir).Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	settings, err := uploader.settings.Load()
	if err != nil || !settings.Enabled {
		return err
	}
	attemptRecorded := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		latest, err := uploader.settings.Load()
		if err != nil || !latest.Enabled || latest.Generation != settings.Generation {
			return err
		}
		items, err := uploader.queue.ListContext(ctx)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return uploader.queue.ClearUploadAttempt()
		}
		if !attemptRecorded {
			now := uploader.queue.options.Now()
			if !uploader.queue.UploadDue(now) {
				return nil
			}
			if err := uploader.queue.RecordUploadAttempt(now); err != nil {
				return err
			}
			attemptRecorded = true
		}
		matching := items[:0]
		for _, item := range items {
			if item.IdentityKey == uploader.queue.options.IdentityKey {
				matching = append(matching, item)
			}
		}
		items = matching
		if len(items) == 0 {
			return nil
		}
		batch, payload, err := uploader.buildBatch(items)
		if err != nil {
			return err
		}
		response, err := uploader.transport.Do(ctx, httptransport.Request{
			Method: http.MethodPost,
			Path:   ReportPath,
			Body:   payload,
			Headers: http.Header{
				"Accept":       []string{"application/json"},
				"Content-Type": []string{"application/json"},
			},
		})
		if err != nil {
			return err
		}
		var result reportResponse
		if err := decodeTelemetryResponse(response, &result); err != nil {
			return err
		}
		ids, partial, err := acknowledgedQueueIDs(batch, result.FailedCallIDs)
		if err != nil {
			return err
		}
		if err := uploader.queue.AckContext(ctx, ids); err != nil {
			return err
		}
		if partial {
			return nil
		}
	}
}

func (uploader *Uploader) buildBatch(items []QueuedEvent) ([]QueuedEvent, []byte, error) {
	limit := min(MaxReportEvents, len(items))
	requestID := uploader.requestID()
	batch := make([]QueuedEvent, 0, limit)
	var payload []byte
	for _, item := range items[:limit] {
		if err := item.Event.Validate(); err != nil {
			return nil, nil, fmt.Errorf("invalid queued telemetry event: %w", err)
		}
		candidate := append(batch, item)
		events := make([]Event, 0, len(candidate))
		for _, queued := range candidate {
			events = append(events, queued.Event)
		}
		encoded, err := json.Marshal(reportRequest{RequestID: requestID, Events: events})
		if err != nil {
			return nil, nil, err
		}
		if len(encoded) > MaxReportBytes {
			if len(batch) == 0 {
				return nil, nil, ErrEventTooLarge
			}
			break
		}
		batch = candidate
		payload = encoded
	}
	return batch, payload, nil
}

func acknowledgedQueueIDs(batch []QueuedEvent, failedCallIDs []string) ([]string, bool, error) {
	// An explicit [] acknowledges the entire batch. Missing/null is not an ACK.
	if failedCallIDs == nil {
		return nil, false, ErrInvalidAcknowledgement
	}
	byCallID := make(map[string]string, len(batch))
	for _, item := range batch {
		if _, duplicate := byCallID[item.Event.CallID]; duplicate {
			return nil, false, ErrInvalidAcknowledgement
		}
		byCallID[item.Event.CallID] = item.ID
	}
	seen := make(map[string]struct{}, len(failedCallIDs))
	for _, callID := range failedCallIDs {
		if _, duplicate := seen[callID]; duplicate {
			return nil, false, ErrInvalidAcknowledgement
		}
		seen[callID] = struct{}{}
		_, known := byCallID[callID]
		if !known {
			return nil, false, ErrInvalidAcknowledgement
		}
	}
	ids := make([]string, 0, len(batch))
	for _, item := range batch {
		if _, failed := seen[item.Event.CallID]; !failed {
			ids = append(ids, item.ID)
		}
	}
	return ids, len(failedCallIDs) > 0, nil
}

type PolicyRefresherConfig struct {
	Policies  *PolicyStore
	Transport telemetryTransport
	Now       func() time.Time
}

type PolicyRefresher struct {
	policies  *PolicyStore
	transport telemetryTransport
	now       func() time.Time
}

type policyResponse struct {
	Policy             Policy `json:"policy"`
	PolicyFromValidTCC bool   `json:"policy_from_valid_tcc"`
}

func NewPolicyRefresher(config PolicyRefresherConfig) *PolicyRefresher {
	if config.Now == nil {
		config.Now = time.Now
	}
	return &PolicyRefresher{policies: config.Policies, transport: config.Transport, now: config.Now}
}

func (refresher *PolicyRefresher) RefreshIfDue(ctx context.Context) error {
	if refresher == nil || refresher.policies == nil {
		return errors.New("telemetry policy refresher is not configured")
	}
	now := refresher.now()
	if !refresher.policies.RefreshDue(now) {
		return nil
	}
	if refresher.transport == nil || isNilTelemetryTransport(refresher.transport) {
		return refresher.recordFailure(now, errors.New("telemetry transport is not configured"))
	}
	response, err := refresher.transport.Do(ctx, httptransport.Request{
		Method:  http.MethodGet,
		Path:    PolicyPath,
		Headers: http.Header{"Accept": []string{"application/json"}},
	})
	if err != nil {
		return refresher.recordFailure(now, err)
	}
	var result policyResponse
	if err := decodeTelemetryResponse(response, &result); err != nil {
		return refresher.recordFailure(now, err)
	}
	if err := refresher.policies.ApplyRefresh(result.Policy, result.PolicyFromValidTCC, now); err != nil {
		return refresher.recordFailure(now, err)
	}
	return nil
}

func (refresher *PolicyRefresher) recordFailure(now time.Time, cause error) error {
	if err := refresher.policies.RecordRefreshFailure(now); err != nil {
		return fmt.Errorf("%v; record telemetry policy refresh failure: %w", cause, err)
	}
	return cause
}

func decodeTelemetryResponse(response *httptransport.Response, target interface{}) error {
	if response == nil {
		return errors.New("empty telemetry response")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("telemetry endpoint returned HTTP %d", response.StatusCode)
	}
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(response.Body, &outer); err != nil {
		return errors.New("invalid telemetry response")
	}
	if code := parseTelemetryCode(outer["code"]); code != 0 {
		return fmt.Errorf("telemetry endpoint returned gateway error %d", code)
	}
	payload := response.Body
	if data, ok := outer["data"]; ok && len(data) > 0 && string(data) != "null" {
		payload = data
		if err := json.Unmarshal(payload, &outer); err != nil {
			return errors.New("invalid telemetry response")
		}
	}
	for _, key := range []string{"BaseResp", "base_resp"} {
		baseRaw, ok := outer[key]
		if !ok {
			continue
		}
		var base map[string]json.RawMessage
		if json.Unmarshal(baseRaw, &base) != nil || base == nil {
			return errors.New("invalid telemetry business response")
		}
		if code := firstTelemetryCode(base, "StatusCode", "status_code"); code != 0 {
			return fmt.Errorf("telemetry endpoint returned business error %d", code)
		}
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return errors.New("invalid telemetry response")
	}
	return nil
}

func firstTelemetryCode(values map[string]json.RawMessage, keys ...string) int64 {
	for _, key := range keys {
		if code := parseTelemetryCode(values[key]); code != 0 {
			return code
		}
	}
	return 0
}

func parseTelemetryCode(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	if string(raw) == "null" {
		return -1
	}
	var number int64
	if json.Unmarshal(raw, &number) == nil {
		return number
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
			return parsed
		}
	}
	return -1
}

func isNilTelemetryTransport(transport telemetryTransport) bool {
	value := reflect.ValueOf(transport)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func randomID() string {
	buffer := make([]byte, 16)
	if _, err := crand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}
