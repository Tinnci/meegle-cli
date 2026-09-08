// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package meegle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/larksuite/meegle-cli/internal/products/meegle/types"
	frameworkerrors "github.com/larksuite/meegle-cli/pkg/framework/errors"
	"github.com/larksuite/meegle-cli/pkg/framework/pipeline"
	"github.com/larksuite/meegle-cli/pkg/runtime/cliapp"
)

type precisionTestSessionStep struct {
	serverURL string
}

func (step precisionTestSessionStep) Name() string { return "precision_test_session" }

func (step precisionTestSessionStep) Execute(_ context.Context, state *pipeline.PipelineContext) error {
	state.OutputConfig = map[string]any{
		"mcp.server_url": step.serverURL,
		"mcp.token":      "test-token",
		"mcp.headers":    map[string]string{},
	}
	return nil
}

func TestCLIJSONNumbersRemainExactAcrossInputTransportOutputAndReuse(t *testing.T) {
	const outputNumber = "123456789012345678901234567890123456789012345678901234567890"
	var captured []json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var rpc struct {
			ID     int64 `json:"id"`
			Params struct {
				Arguments map[string]json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if value, ok := rpc.Params.Arguments["id"]; ok {
			captured = append(captured, append(json.RawMessage(nil), value...))
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      rpc.ID,
			"result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": outputNumber}},
			},
		})
	}))
	defer server.Close()

	run := func(args ...string) string {
		t.Helper()
		app := newPrecisionCLIApp(t, server.URL)
		var stdout, stderr bytes.Buffer
		if err := app.ExecuteWithIO(context.Background(), args, &stdout, &stderr); err != nil {
			t.Fatalf("ExecuteWithIO(%v): %v\nstderr: %s", args, err, stderr.String())
		}
		return strings.TrimSpace(stdout.String())
	}

	if got := run("precision", "echo", "--id", "9007199254740993", "--format", "json"); got != outputNumber {
		t.Fatalf("JSON output = %q, want %q", got, outputNumber)
	}
	if got := run("precision", "echo", "--params", `{"id":9223372036854775807}`, "--format", "ndjson"); got != outputNumber {
		t.Fatalf("NDJSON output = %q, want %q", got, outputNumber)
	}

	paramsFile := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(paramsFile, []byte(`{"id":123456789012345678901234567890}`), 0o600); err != nil {
		t.Fatalf("write params file: %v", err)
	}
	if got := run("precision", "echo", "--params", "@"+paramsFile, "--format", "json"); got != outputNumber {
		t.Fatalf("@file JSON output = %q, want %q", got, outputNumber)
	}
	if got := run("precision", "echo", "--set", "id=1.2300", "--format", "json"); got != outputNumber {
		t.Fatalf("--set JSON output = %q, want %q", got, outputNumber)
	}
	if got := run("precision", "echo", "--set", "id=1e+30", "--format", "json"); got != outputNumber {
		t.Fatalf("exponent JSON output = %q, want %q", got, outputNumber)
	}

	tableOutput := run("precision", "echo", "--format", "table")
	if tableOutput != outputNumber {
		t.Fatalf("table output = %q, want exact scalar %q", tableOutput, outputNumber)
	}
	_ = run("precision", "echo", "--id", tableOutput, "--format", "json")

	wants := []string{
		"9007199254740993",
		"9223372036854775807",
		"123456789012345678901234567890",
		"1.2300",
		"1e+30",
		outputNumber,
	}
	if len(captured) != len(wants) {
		t.Fatalf("captured calls = %d, want %d: %q", len(captured), len(wants), captured)
	}
	for index, want := range wants {
		if got := string(captured[index]); got != want {
			t.Errorf("wire argument[%d] = %q, want %q", index, got, want)
		}
	}
}

func TestCLIDynamicNumericFlagsRejectInvalidValuesBeforeTransport(t *testing.T) {
	longFraction := strings.Repeat("1", 80) + ".5"
	serverCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serverCalls++
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	for _, test := range []struct {
		name        string
		args        []string
		wantMessage string
		forbid      string
	}{
		{name: "number rejects non JSON leading zero", args: []string{"precision", "echo", "--id", "01"}},
		{name: "integer rejects fraction", args: []string{"precision", "echo", "--count", "1.5"}},
		{
			name:        "params integer rejects fraction",
			args:        []string{"precision", "echo", "--params", `{"count":1.5}`},
			wantMessage: `parameter "count" must be a valid JSON integer, got 1.5`,
		},
		{
			name:        "set integer rejects non number",
			args:        []string{"precision", "echo", "--set", "count=abc"},
			wantMessage: `parameter "count" must be a valid JSON integer, got "abc"`,
		},
		{
			name:        "params object reports shape without contents",
			args:        []string{"precision", "echo", "--params", `{"count":{"token":"do-not-print"}}`},
			wantMessage: `parameter "count" must be a valid JSON integer, got object (1 key)`,
			forbid:      "do-not-print",
		},
		{
			name:        "set long string is truncated",
			args:        []string{"precision", "echo", "--set", "count=" + strings.Repeat("a", 70) + "do-not-print"},
			wantMessage: `parameter "count" must be a valid JSON integer, got "` + strings.Repeat("a", 64) + `…"`,
			forbid:      "do-not-print",
		},
		{
			name:        "params long fractional number is truncated",
			args:        []string{"precision", "echo", "--params", `{"count":` + longFraction + `}`},
			wantMessage: `parameter "count" must be a valid JSON integer, got ` + strings.Repeat("1", 64) + `…`,
			forbid:      longFraction,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newPrecisionCLIApp(t, server.URL)
			var stdout, stderr bytes.Buffer
			err := app.ExecuteWithIO(context.Background(), test.args, &stdout, &stderr)
			if err == nil {
				t.Fatalf("ExecuteWithIO(%v) unexpectedly succeeded", test.args)
			}
			if !strings.Contains(err.Error(), "must be a valid JSON") {
				t.Fatalf("ExecuteWithIO(%v) error = %q, want typed numeric validation", test.args, err)
			}
			if test.wantMessage != "" && !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("ExecuteWithIO(%v) error = %q, want %q", test.args, err, test.wantMessage)
			}
			if test.forbid != "" && strings.Contains(err.Error(), test.forbid) {
				t.Fatalf("ExecuteWithIO(%v) leaked structured input in error: %q", test.args, err)
			}
		})
	}
	if serverCalls != 0 {
		t.Fatalf("backend calls = %d, want 0 for locally rejected values", serverCalls)
	}
}

func TestCLIMultipleInvalidNumericParamsReportFirstByName(t *testing.T) {
	serverCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serverCalls++
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	const attempts = 32
	for attempt := 0; attempt < attempts; attempt++ {
		app := newPrecisionCLIApp(t, server.URL)
		var stdout, stderr bytes.Buffer
		err := app.ExecuteWithIO(
			context.Background(),
			[]string{"precision", "echo", "--params", `{"id":"bad-id","count":"bad-count"}`},
			&stdout,
			&stderr,
		)
		if err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", attempt)
		}
		if want := `parameter "count" must be a valid JSON integer`; !strings.Contains(err.Error(), want) {
			t.Fatalf("attempt %d error = %q, want %q", attempt, err, want)
		}
	}
	if serverCalls != 0 {
		t.Fatalf("backend calls = %d, want 0", serverCalls)
	}
}

func TestCLIDynamicIntegerFlagPreservesExactValue(t *testing.T) {
	const want = "123456789012345678901234567890"
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var rpc struct {
			ID     int64 `json:"id"`
			Params struct {
				Arguments map[string]json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		captured = string(rpc.Params.Arguments["count"])
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      rpc.ID,
			"result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": `{"ok":true}`}},
			},
		})
	}))
	defer server.Close()

	app := newPrecisionCLIApp(t, server.URL)
	var stdout, stderr bytes.Buffer
	if err := app.ExecuteWithIO(context.Background(), []string{"precision", "echo", "--count", want, "--format", "json"}, &stdout, &stderr); err != nil {
		t.Fatalf("ExecuteWithIO: %v\nstderr: %s", err, stderr.String())
	}
	if captured != want {
		t.Fatalf("wire count = %q, want %q", captured, want)
	}
}

func TestCLIEmptyParamsFileReportsStableJSONError(t *testing.T) {
	serverCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serverCalls++
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	paramsFile := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(paramsFile, nil, 0o600); err != nil {
		t.Fatalf("write empty params file: %v", err)
	}
	app := newPrecisionCLIApp(t, server.URL)
	var stdout, stderr bytes.Buffer
	err := app.ExecuteWithIO(context.Background(), []string{"precision", "echo", "--params", "@" + paramsFile}, &stdout, &stderr)
	if err == nil {
		t.Fatal("ExecuteWithIO unexpectedly succeeded")
	}
	var cliErr *frameworkerrors.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("error = %T, want CLIError", err)
	}
	if cliErr.Code != frameworkerrors.CodeInvalidParams {
		t.Fatalf("code = %q, want %q", cliErr.Code, frameworkerrors.CodeInvalidParams)
	}
	if !strings.Contains(cliErr.Message, "unexpected end of JSON input") {
		t.Fatalf("message = %q, want stable empty JSON error", cliErr.Message)
	}
	if serverCalls != 0 {
		t.Fatalf("backend calls = %d, want 0", serverCalls)
	}
}

func newPrecisionCLIApp(t *testing.T, serverURL string) *cliapp.App {
	t.Helper()
	lister := &successLister{tools: []types.ToolDefinition{{
		Name:        "precision_echo",
		Description: "Echo a precision test value",
		Metadata:    &types.ToolMetadata{Resource: "precision", Method: "echo"},
		Parameters: []types.ToolParameter{
			{Name: "id", Type: "number"},
			{Name: "count", Type: "integer"},
		},
	}}}
	setup := NewDynamicRegistrySetup(lister, nil, WithGlobalFlags(MeegleGlobalFlags))
	app, err := cliapp.New(
		cliapp.WithAppName("meegle"),
		cliapp.WithVersion("test"),
		cliapp.WithSetup(setup),
		cliapp.WithPipelineFactory(func(cliapp.Config) (*pipeline.Pipeline, error) {
			return &pipeline.Pipeline{Steps: []pipeline.PipelineStep{
				&pipeline.ParamMergeStep{},
				&StructuredFlagNameNormalizeStep{},
				precisionTestSessionStep{serverURL: serverURL},
				&McpExecutorStep{},
				&pipeline.OutputStep{Processor: meegleOutputProcessor()},
			}}, nil
		}),
	)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	return app
}
