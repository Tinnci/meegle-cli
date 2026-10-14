// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package httptransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientSendsBearerAndManagedHeadersAsSingleValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer token-a" {
			t.Fatalf("Authorization = %v", got)
		}
		if got := request.Header.Values("User-Agent"); len(got) != 1 || got[0] != "meegle-cli/test" {
			t.Fatalf("User-Agent = %v", got)
		}
		if got := request.Header.Values("X-Extra"); len(got) != 2 || got[0] != "one" || got[1] != "two" {
			t.Fatalf("X-Extra = %v", got)
		}
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := New(Config{
		BaseURL: server.URL,
		Token:   func() (string, error) { return "token-a", nil },
		Headers: http.Header{
			"Authorization": {"caller-a", "caller-b"},
			"User-Agent":    {"caller-agent-a", "caller-agent-b"},
			"X-Extra":       {"one", "two"},
		},
		UserAgent: "meegle-cli/test",
	})
	response, err := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/v1/config",
		Headers: http.Header{
			"Authorization": {"per-request"},
			"User-Agent":    {"per-request-agent"},
		},
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if response.StatusCode != http.StatusOK || string(response.Body) != `{"ok":true}` {
		t.Fatalf("response = %#v", response)
	}
}

func TestClientSendsCustomAuthHeaderAndSuppressesAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Values("X-Meegle-Token"); len(got) != 1 || got[0] != "opaque" {
			t.Fatalf("X-Meegle-Token = %v", got)
		}
		if got := request.Header.Values("Authorization"); len(got) != 0 {
			t.Fatalf("Authorization = %v, want none", got)
		}
	}))
	defer server.Close()

	client := New(Config{
		BaseURL:    server.URL,
		Token:      func() (string, error) { return "opaque", nil },
		AuthHeader: "X-Meegle-Token",
		Headers: http.Header{
			"Authorization":  {"static"},
			"X-Meegle-Token": {"stale-a", "stale-b"},
		},
	})
	if _, err := client.Do(context.Background(), Request{Method: http.MethodGet}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestClientReturnsTokenGetterErrorWithoutSendingRequest(t *testing.T) {
	tokenErr := errors.New("token unavailable")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Token: func() (string, error) { return "", tokenErr }})
	_, err := client.Do(context.Background(), Request{Method: http.MethodGet})
	var transportErr *Error
	if !errors.As(err, &transportErr) || transportErr.Kind != ErrorToken || !errors.Is(err, tokenErr) {
		t.Fatalf("Do() error = %#v", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestClientReturnsUnauthorizedWithoutRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	response, err := New(Config{BaseURL: server.URL}).Do(context.Background(), Request{})
	if err != nil || response.StatusCode != http.StatusUnauthorized || requests != 1 {
		t.Fatalf("Do() = (%#v, %v), requests = %d", response, err, requests)
	}
}

func TestClientPerRequestTimeout(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	client := New(Config{BaseURL: "https://example.invalid", HTTPClient: httpClient, Timeout: 10 * time.Millisecond})

	_, err := client.Do(context.Background(), Request{Method: http.MethodGet})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Do() error = %v, want deadline exceeded", err)
	}
}

func TestClientParentContextCancellation(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	client := New(Config{BaseURL: "https://example.invalid", HTTPClient: httpClient, Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Do(ctx, Request{Method: http.MethodGet})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context cancelled", err)
	}
}

func TestClientBoundsResponseBodyAndKeepsHTTPStatus(t *testing.T) {
	responseHeader := make(http.Header)
	responseHeader.Set("X-Request-ID", "req-1")
	client := New(Config{
		BaseURL:          "https://example.invalid",
		MaxResponseBytes: 5,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTeapot,
				Status:     "418 I'm a teapot",
				Header:     responseHeader,
				Body:       io.NopCloser(bytes.NewBufferString("123456")),
			}, nil
		})},
	})

	response, err := client.Do(context.Background(), Request{Method: http.MethodGet})
	var transportErr *Error
	if !errors.As(err, &transportErr) || transportErr.Kind != ErrorResponseTooLarge {
		t.Fatalf("Do() error = %#v", err)
	}
	if response == nil || response.StatusCode != http.StatusTeapot || response.Status != "418 I'm a teapot" || response.Header.Get("X-Request-ID") != "req-1" {
		t.Fatalf("response = %#v", response)
	}
	if transportErr.StatusCode != http.StatusTeapot {
		t.Fatalf("transport error status = %d", transportErr.StatusCode)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
