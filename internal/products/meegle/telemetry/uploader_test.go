// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/larksuite/meegle-cli/internal/products/meegle/httptransport"
)

type fakeTelemetryTransport struct {
	responses []*httptransport.Response
	errs      []error
	requests  []httptransport.Request
	onRequest func()
}

func (transport *fakeTelemetryTransport) Do(_ context.Context, request httptransport.Request) (*httptransport.Response, error) {
	transport.requests = append(transport.requests, request)
	if transport.onRequest != nil {
		transport.onRequest()
	}
	index := len(transport.requests) - 1
	var response *httptransport.Response
	if index < len(transport.responses) {
		response = transport.responses[index]
	}
	var err error
	if index < len(transport.errs) {
		err = transport.errs[index]
	}
	return response, err
}

func TestUploaderRetainsOnlyFailedEvents(t *testing.T) {
	root := t.TempDir()
	queue := NewQueue(root, QueueOptions{})
	mustEnqueue(t, queue, validFailureEvent("call-a"))
	mustEnqueue(t, queue, validFailureEvent("call-b"))
	transport := &fakeTelemetryTransport{responses: []*httptransport.Response{{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"code":0,"data":{"failed_call_ids":["call-b"],"BaseResp":{"StatusCode":0}}}`),
	}}}
	uploader := NewUploader(UploaderConfig{
		Queue: queue, Settings: NewSettingsStore(root),
		Transport: transport, RequestID: func() string { return "request-1" }, LockDir: root + "/send-lock",
	})

	if err := uploader.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Event.CallID != "call-b" {
		t.Fatalf("queue = %#v, want only call-b", items)
	}
}

func TestUploaderRetainsWholeBatchForInvalidAcknowledgement(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown", body: `{"failed_call_ids":["unknown"],"BaseResp":{"StatusCode":0}}`},
		{name: "duplicate", body: `{"failed_call_ids":["call-a","call-a"],"BaseResp":{"StatusCode":0}}`},
		{name: "missing", body: `{"BaseResp":{"StatusCode":0}}`},
		{name: "null", body: `{"failed_call_ids":null,"BaseResp":{"StatusCode":0}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			queue := NewQueue(root, QueueOptions{})
			mustEnqueue(t, queue, validFailureEvent("call-a"))
			mustEnqueue(t, queue, validFailureEvent("call-b"))
			uploader := NewUploader(UploaderConfig{
				Queue: queue, Settings: NewSettingsStore(root),
				Transport: &fakeTelemetryTransport{responses: []*httptransport.Response{{StatusCode: http.StatusOK, Body: []byte(tt.body)}}},
				LockDir:   root + "/send-lock",
			})

			err := uploader.Run(context.Background())

			if !errors.Is(err, ErrInvalidAcknowledgement) {
				t.Fatalf("Run() error = %v, want ErrInvalidAcknowledgement", err)
			}
			items, listErr := queue.List()
			if listErr != nil || len(items) != 2 {
				t.Fatalf("queue = %#v, err=%v, want whole batch", items, listErr)
			}
		})
	}
}

func TestUploaderRetainsWholeBatchOnResponseLoss(t *testing.T) {
	root := t.TempDir()
	queue := NewQueue(root, QueueOptions{})
	mustEnqueue(t, queue, validFailureEvent("call-a"))
	lost := errors.New("response lost")
	uploader := NewUploader(UploaderConfig{
		Queue: queue, Settings: NewSettingsStore(root),
		Transport: &fakeTelemetryTransport{errs: []error{lost}}, LockDir: root + "/send-lock",
	})

	if err := uploader.Run(context.Background()); !errors.Is(err, lost) {
		t.Fatalf("Run() error = %v, want response loss", err)
	}
	if items, err := queue.List(); err != nil || len(items) != 1 {
		t.Fatalf("queue = %#v, err=%v, want retained event", items, err)
	}
}

func TestUploaderCanceledOrPanickedTransportRetainsBatchAndReleasesLock(t *testing.T) {
	for _, mode := range []string{"cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
			queue := NewQueue(root, QueueOptions{Now: func() time.Time { return now }})
			mustEnqueue(t, queue, validFailureEvent("call-a"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ok := &httptransport.Response{StatusCode: 200, Body: []byte(`{"failed_call_ids":[]}`)}
			transport := &fakeTelemetryTransport{responses: []*httptransport.Response{ok, ok}, onRequest: func() {
				if mode == "panic" {
					panic("transport panic")
				}
				cancel()
			}}
			uploader := NewUploader(UploaderConfig{Queue: queue, Settings: NewSettingsStore(root), Transport: transport})
			var runErr error
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				runErr = uploader.Run(ctx)
			}()
			if mode == "panic" && !panicked {
				t.Fatal("transport panic was not exercised")
			}
			if mode == "cancel" && !errors.Is(runErr, context.Canceled) {
				t.Fatalf("Run error=%v", runErr)
			}
			if items, err := queue.List(); err != nil || len(items) != 1 {
				t.Fatalf("queue=%v err=%v", items, err)
			}
			transport.onRequest = nil
			now = now.Add(uploadRetryInterval)
			retryCtx, retryCancel := context.WithTimeout(context.Background(), time.Second)
			defer retryCancel()
			if err := uploader.Run(retryCtx); err != nil {
				t.Fatalf("lock not released: %v", err)
			}
			if items, err := queue.List(); err != nil || len(items) != 0 {
				t.Fatalf("retry queue=%v err=%v", items, err)
			}
		})
	}
}

func TestUploaderFailedIDsContractAndRetry(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		remaining int
	}{
		{"all succeed", `{"failed_call_ids":[]}`, 0},
		{"partial failure", `{"failed_call_ids":["call-b"]}`, 1},
		{"all fail", `{"failed_call_ids":["call-b","call-a"]}`, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
			queue := NewQueue(root, QueueOptions{Now: func() time.Time { return now }})
			mustEnqueue(t, queue, validFailureEvent("call-a"))
			mustEnqueue(t, queue, validFailureEvent("call-b"))
			transport := &fakeTelemetryTransport{responses: []*httptransport.Response{
				{StatusCode: http.StatusOK, Body: []byte(test.body)},
				{StatusCode: http.StatusOK, Body: []byte(`{"failed_call_ids":[]}`)},
			}}
			uploader := NewUploader(UploaderConfig{Queue: queue, Settings: NewSettingsStore(root), Transport: transport})
			if err := uploader.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			items, err := queue.List()
			if err != nil || len(items) != test.remaining {
				t.Fatalf("queue=%v, err=%v", items, err)
			}
			if len(transport.requests) != 1 {
				t.Fatal("failed events must not immediately loop")
			}
			now = now.Add(uploadRetryInterval)
			if err := uploader.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if test.remaining > 0 {
				var retried reportRequest
				if err := json.Unmarshal(transport.requests[1].Body, &retried); err != nil {
					t.Fatal(err)
				}
				if len(retried.Events) != test.remaining {
					t.Fatalf("retried=%v", retried.Events)
				}
				if test.remaining == 1 && retried.Events[0].CallID != "call-b" {
					t.Fatal("retried successful event")
				}
			}
			if items, err := queue.List(); err != nil || len(items) != 0 {
				t.Fatalf("retry queue=%v err=%v", items, err)
			}
		})
	}
}

func TestUploaderHTTPPartialFailureRetriesOnlyFailedID(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	queue := NewQueue(root, QueueOptions{Now: func() time.Time { return now }})
	mustEnqueue(t, queue, validFailureEvent("call-a"))
	mustEnqueue(t, queue, validFailureEvent("call-b"))
	requests := make(chan reportRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != ReportPath || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected telemetry request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request reportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- request
		failed := []string{}
		if len(request.Events) == 2 {
			failed = []string{"call-b"}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": reportResponse{FailedCallIDs: failed}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	uploader := NewUploader(UploaderConfig{
		Queue: queue, Settings: NewSettingsStore(root),
		Transport: httptransport.New(httptransport.Config{BaseURL: server.URL, HTTPClient: server.Client(), Token: func() (string, error) { return "test-token", nil }}),
	})
	for i := 0; i < 2; i++ {
		if i > 0 {
			now = now.Add(uploadRetryInterval)
		}
		if err := uploader.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		select {
		case request := <-requests:
			if len(request.Events) != 2-i || (i == 1 && request.Events[0].CallID != "call-b") {
				t.Fatalf("request %d: %#v", i, request)
			}
		default:
			t.Fatal("HTTP request was not observed")
		}
	}
	if items, err := queue.List(); err != nil || len(items) != 0 {
		t.Fatalf("queue after retry=%v, err=%v", items, err)
	}
}

func TestUploaderWaitsBeforeRetryingFailedBatch(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	queue := NewQueue(root, QueueOptions{Now: func() time.Time { return now }})
	mustEnqueue(t, queue, validFailureEvent("call-a"))
	lost := errors.New("response lost")
	transport := &fakeTelemetryTransport{
		responses: []*httptransport.Response{nil, {StatusCode: http.StatusOK, Body: []byte(`{"failed_call_ids":[]}`)}},
		errs:      []error{lost, nil},
	}
	uploader := NewUploader(UploaderConfig{Queue: queue, Settings: NewSettingsStore(root), Transport: transport})

	if err := uploader.Run(context.Background()); !errors.Is(err, lost) {
		t.Fatalf("first Run() error = %v, want response loss", err)
	}
	if err := uploader.Run(context.Background()); err != nil {
		t.Fatalf("cooldown Run() error = %v", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests during cooldown = %d, want 1", len(transport.requests))
	}
	now = now.Add(uploadRetryInterval)
	if err := uploader.Run(context.Background()); err != nil {
		t.Fatalf("retry Run() error = %v", err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests after cooldown = %d, want 2", len(transport.requests))
	}
	if items, err := queue.List(); err != nil || len(items) != 0 {
		t.Fatalf("queue after successful retry = %#v, err=%v", items, err)
	}
	if !queue.UploadDue(now) {
		t.Fatal("successful drain did not clear upload cooldown")
	}
}

func TestUploaderRetainsBatchOnEndpointFailure(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *httptransport.Response
		err      error
	}{
		{"nil response", nil, nil},
		{"timeout", nil, context.DeadlineExceeded},
		{"canceled", nil, context.Canceled},
		{"HTTP failure", &httptransport.Response{StatusCode: 503, Body: []byte(`{"failed_call_ids":[]}`)}, nil},
		{"business failure", &httptransport.Response{StatusCode: 200, Body: []byte(`{"failed_call_ids":[],"BaseResp":{"StatusCode":50000}}`)}, nil},
		{"invalid JSON", &httptransport.Response{StatusCode: 200, Body: []byte(`{`)}, nil},
		{"wrong type", &httptransport.Response{StatusCode: 200, Body: []byte(`{"failed_call_ids":false}`)}, nil},
		{"invalid status", &httptransport.Response{StatusCode: 200, Body: []byte(`{"failed_call_ids":[],"code":"invalid"}`)}, nil},
		{"null status", &httptransport.Response{StatusCode: 200, Body: []byte(`{"failed_call_ids":[],"code":null}`)}, nil},
		{"null base", &httptransport.Response{StatusCode: 200, Body: []byte(`{"failed_call_ids":[],"BaseResp":null}`)}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			queue := NewQueue(root, QueueOptions{})
			mustEnqueue(t, queue, validFailureEvent("call-a"))
			uploader := NewUploader(UploaderConfig{Queue: queue, Settings: NewSettingsStore(root),
				Transport: &fakeTelemetryTransport{responses: []*httptransport.Response{test.response}, errs: []error{test.err}}})
			if err := uploader.Run(context.Background()); err == nil {
				t.Fatal("expected endpoint error")
			}
			if items, err := queue.List(); err != nil || len(items) != 1 {
				t.Fatalf("queue=%v err=%v", items, err)
			}
		})
	}
}

func TestUploaderDoesNotApplyNewPolicyToAlreadyQueuedEvents(t *testing.T) {
	root := t.TempDir()
	queue := NewQueue(root, QueueOptions{})
	mustEnqueue(t, queue, validFailureEvent("call-a"))
	disabled := DefaultPolicy()
	disabled.Enabled = false
	if err := NewPolicyStore(root).ApplyRefresh(disabled, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	transport := &fakeTelemetryTransport{responses: []*httptransport.Response{{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"failed_call_ids":[],"BaseResp":{"StatusCode":0}}`),
	}}}
	uploader := NewUploader(UploaderConfig{
		Queue: queue, Settings: NewSettingsStore(root), Transport: transport, LockDir: root + "/send-lock",
	})

	if err := uploader.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if items, err := queue.List(); err != nil || len(items) != 0 {
		t.Fatalf("queue = %#v, err=%v, want queued snapshot uploaded", items, err)
	}
}

func TestPolicyRefresherAppliesValidResponseAndRecordsFailures(t *testing.T) {
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	t.Run("valid", func(t *testing.T) {
		root := t.TempDir()
		store := NewPolicyStore(root)
		transport := &fakeTelemetryTransport{responses: []*httptransport.Response{{StatusCode: http.StatusOK, Body: []byte(`{
			"policy":{"enabled":true,"command_filter":{"mode":"exclude","values":[]},"error_filter":{"mode":"exclude","values":[]},"sampling":{"default":{"success_rate":0,"failure_rate":5000},"command_rules":[]}},
			"policy_from_valid_tcc":true,"BaseResp":{"StatusCode":0}
		}`)}}}
		refresher := NewPolicyRefresher(PolicyRefresherConfig{Policies: store, Transport: transport, Now: func() time.Time { return now }})

		if err := refresher.RefreshIfDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		state, err := store.Load()
		if err != nil || !state.PolicyFromValidTCC || state.Policy.Sampling.Default.FailureRate != 5000 {
			t.Fatalf("state = %#v, err=%v", state, err)
		}
	})

	t.Run("server failure", func(t *testing.T) {
		root := t.TempDir()
		store := NewPolicyStore(root)
		transportErr := errors.New("server unavailable")
		refresher := NewPolicyRefresher(PolicyRefresherConfig{
			Policies: store, Transport: &fakeTelemetryTransport{errs: []error{transportErr}}, Now: func() time.Time { return now },
		})

		if err := refresher.RefreshIfDue(context.Background()); !errors.Is(err, transportErr) {
			t.Fatalf("RefreshIfDue() error = %v", err)
		}
		state, err := store.Load()
		if err != nil || state.PolicyFromValidTCC || !state.LastRefreshAttempt.Equal(now) {
			t.Fatalf("state = %#v, err=%v", state, err)
		}
	})

	t.Run("missing TCC default", func(t *testing.T) {
		root := t.TempDir()
		store := NewPolicyStore(root)
		transport := &fakeTelemetryTransport{responses: []*httptransport.Response{{StatusCode: http.StatusOK, Body: []byte(`{
			"policy":{"enabled":true,"command_filter":{"mode":"exclude","values":[]},"error_filter":{"mode":"exclude","values":[]},"sampling":{"default":{"success_rate":0,"failure_rate":10000},"command_rules":[]}},
			"policy_from_valid_tcc":false,"BaseResp":{"StatusCode":0}
		}`)}}}
		refresher := NewPolicyRefresher(PolicyRefresherConfig{Policies: store, Transport: transport, Now: func() time.Time { return now }})

		if err := refresher.RefreshIfDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		state, err := store.Load()
		if err != nil || state.PolicyFromValidTCC || state.HasValidTCCPolicy || state.Policy.Sampling.Default.FailureRate != BasisPoints {
			t.Fatalf("state = %#v, err=%v", state, err)
		}
	})
}

func mustEnqueue(t *testing.T, queue *Queue, event Event) {
	t.Helper()
	if err := queue.Enqueue(event); err != nil {
		t.Fatal(err)
	}
}
