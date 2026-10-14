// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

// Package httptransport provides authenticated, bounded HTTP requests for
// Meegle product clients. It deliberately contains no endpoint or payload
// semantics.
package httptransport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultTimeout          = 8 * time.Second
	DefaultMaxResponseBytes = 4 << 20
)

// ErrorKind identifies the stage at which a request failed.
type ErrorKind string

const (
	ErrorToken            ErrorKind = "token"
	ErrorCreateRequest    ErrorKind = "create_request"
	ErrorRequest          ErrorKind = "request"
	ErrorReadResponse     ErrorKind = "read_response"
	ErrorResponseTooLarge ErrorKind = "response_too_large"
)

// Error preserves the underlying error and, when a response exists, its HTTP
// status. Callers can classify it with errors.As and errors.Is.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	Err        error
}

func (err *Error) Error() string {
	if err == nil {
		return ""
	}
	if err.Err == nil {
		return string(err.Kind)
	}
	return fmt.Sprintf("%s: %v", err.Kind, err.Err)
}

func (err *Error) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

type Config struct {
	BaseURL          string
	HTTPClient       *http.Client
	Token            func() (string, error)
	Headers          http.Header
	AuthHeader       string
	UserAgent        string
	Timeout          time.Duration
	MaxResponseBytes int64
}

type Request struct {
	Method  string
	Path    string
	Body    []byte
	Headers http.Header
}

type Response struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
}

type Client struct {
	config Config
}

func New(config Config) *Client {
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	if config.Headers == nil {
		config.Headers = make(http.Header)
	} else {
		config.Headers = config.Headers.Clone()
	}
	if config.Timeout <= 0 {
		config.Timeout = DefaultTimeout
	}
	if config.MaxResponseBytes <= 0 {
		config.MaxResponseBytes = DefaultMaxResponseBytes
	}
	return &Client{config: config}
}

// Do executes one bounded request. Endpoint clients own non-2xx status handling.
func (client *Client) Do(ctx context.Context, request Request) (*Response, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, client.config.Timeout)
	defer cancel()

	method := request.Method
	if method == "" {
		method = http.MethodGet
	}
	url := strings.TrimRight(client.config.BaseURL, "/") + request.Path
	httpRequest, err := http.NewRequestWithContext(attemptCtx, method, url, bytes.NewReader(request.Body))
	if err != nil {
		return nil, &Error{Kind: ErrorCreateRequest, Err: err}
	}
	httpRequest.Header = client.config.Headers.Clone()
	for key, values := range request.Headers {
		httpRequest.Header.Del(key)
		for _, value := range values {
			httpRequest.Header.Add(key, value)
		}
	}
	if client.config.UserAgent != "" {
		httpRequest.Header.Set("User-Agent", client.config.UserAgent)
	}
	if client.config.Token != nil {
		token, tokenErr := client.config.Token()
		if tokenErr != nil {
			return nil, &Error{Kind: ErrorToken, Err: tokenErr}
		}
		if token != "" {
			if authHeader := strings.TrimSpace(client.config.AuthHeader); authHeader != "" {
				httpRequest.Header.Set(authHeader, token)
				httpRequest.Header.Del("Authorization")
			} else {
				httpRequest.Header.Set("Authorization", "Bearer "+token)
			}
		}
	}

	httpResponse, err := client.config.HTTPClient.Do(httpRequest)
	if err != nil {
		return nil, &Error{Kind: ErrorRequest, Err: err}
	}
	defer httpResponse.Body.Close()

	response := &Response{
		StatusCode: httpResponse.StatusCode,
		Status:     httpResponse.Status,
		Header:     httpResponse.Header.Clone(),
	}
	limit := client.config.MaxResponseBytes
	payload, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, limit+1))
	if readErr != nil {
		return response, &Error{Kind: ErrorReadResponse, StatusCode: response.StatusCode, Err: readErr}
	}
	if int64(len(payload)) > limit {
		return response, &Error{
			Kind:       ErrorResponseTooLarge,
			StatusCode: response.StatusCode,
			Err:        fmt.Errorf("response body exceeds %d bytes", limit),
		}
	}
	response.Body = payload
	return response, nil
}
