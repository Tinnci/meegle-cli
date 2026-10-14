// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package meegle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/larksuite/meegle-cli/extension/credential"
	"github.com/larksuite/meegle-cli/internal/products/meegle/auth"
	telemetryevent "github.com/larksuite/meegle-cli/internal/products/meegle/telemetry"
	"github.com/larksuite/meegle-cli/internal/products/meegle/types"
)

type telemetryCredentialProvider struct {
	host string
	mode string
}

func (*telemetryCredentialProvider) Name() string { return "telemetry-test" }
func (p *telemetryCredentialProvider) ResolveAccount(ctx context.Context) (*credential.Account, error) {
	switch p.mode {
	case "error":
		return nil, errors.New("provider unavailable")
	case "panic":
		panic("provider panic")
	case "timeout", "canceled":
		<-ctx.Done()
		return nil, ctx.Err()
	case "profile-drift":
		return &credential.Account{ProfileName: "other", Host: p.host}, nil
	case "host-drift":
		return &credential.Account{ProfileName: "selected", Host: "other.invalid"}, nil
	case "nil-account":
		return nil, nil
	default:
		return &credential.Account{ProfileName: "selected", Host: p.host}, nil
	}
}
func (p *telemetryCredentialProvider) ResolveToken(context.Context, credential.TokenSpec) (*credential.Token, error) {
	switch p.mode {
	case "nil-token":
		return nil, nil
	case "empty-token":
		return &credential.Token{}, nil
	case "header-drift":
		return &credential.Token{Value: "rotated-token", Header: "X-Other-Token"}, nil
	default:
		return &credential.Token{Value: "rotated-token", Header: "X-Test-Token"}, nil
	}
}

func TestTelemetryResolvedIdentityControlsQueueAndHTTPDelivery(t *testing.T) {
	const childEnv = "MEEGLE_TEST_TELEMETRY_IDENTITY"
	mode := os.Getenv(childEnv)
	if mode == "" {
		for _, mode := range []string{"same", "deferred", "nil-account", "nil-token", "empty-token", "error", "panic", "timeout", "canceled", "profile-drift", "host-drift", "header-drift", "redirect"} {
			t.Run(mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTelemetryResolvedIdentityControlsQueueAndHTTPDelivery$")
				child.Env = append(os.Environ(), childEnv+"="+mode)
				if output, err := child.CombinedOutput(); err != nil {
					t.Fatalf("identity child: %v\n%s", err, output)
				}
			})
		}
		return
	}
	setupTestDir(t)
	t.Setenv(telemetryControlEnv, "")
	if err := telemetryevent.NewSettingsStore(telemetryConfigRoot()).MarkDisclosureShown(telemetryDisclosureVersion); err != nil {
		t.Fatal(err)
	}
	var reports, redirects, redirectedRequests atomic.Int32
	var redirectTarget *httptest.Server
	if mode == "redirect" {
		redirectTarget = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			redirectedRequests.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"failed_call_ids": []string{}})
		}))
		defer redirectTarget.Close()
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirectTarget != nil {
			redirects.Add(1)
			if r.Header.Get("X-Test-Token") != "rotated-token" {
				t.Error("redirect scenario did not exercise credential-bearing requests")
			}
			http.Redirect(w, r, redirectTarget.URL+r.URL.Path, http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == telemetryevent.PolicyPath {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"policy": telemetryevent.DefaultPolicy(), "policy_from_valid_tcc": true}})
			return
		}
		if r.URL.Path != telemetryevent.ReportPath {
			t.Errorf("unexpected request path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reports.Add(1)
		if r.Header.Get("X-Test-Token") != "rotated-token" || r.Header.Get("Authorization") != "" {
			t.Error("report did not use the extension's authentication header")
		}
		var body struct {
			Events []telemetryevent.Event `json:"events"`
		}
		payload, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Error(readErr)
		}
		for _, forbidden := range []string{"identity_key", "rotated-token", "builtin-token", "telemetry-test", "selected"} {
			if strings.Contains(string(payload), forbidden) {
				t.Errorf("report contains local identity metadata %q", forbidden)
			}
		}
		if err := json.Unmarshal(payload, &body); err != nil || len(body.Events) != 1 || body.Events[0].CallID == "different-source" || body.Events[0].CallID == "unbound" {
			t.Errorf("unexpected report body: events=%d err=%v", len(body.Events), err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"failed_call_ids": []string{}}})
	}))
	defer server.Close()
	if err := SaveProfileConfig("selected", MeegleConfig{Host: server.URL, AccessToken: "builtin-token"}); err != nil {
		t.Fatal(err)
	}
	if err := NewToolCache(GetCacheDir(), "selected", DefaultTTL).Set([]types.ToolDefinition{{Name: "get_workitem"}}); err != nil {
		t.Fatal(err)
	}
	provider := &telemetryCredentialProvider{host: server.URL}
	var nilProvider *telemetryCredentialProvider
	credential.Register(nilProvider)
	credential.Register(provider)
	oldArgs, oldSpawn := os.Args, spawnTelemetryMaintenanceProcess
	os.Args = []string{"meegle", "--profile", "original", "probe"}
	var staticCommands *StaticCommands
	if mode == "deferred" {
		os.Args[3] = "inspect"
		staticCommands = &StaticCommands{Inspect: &cobra.Command{Use: "inspect", RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := ResolveCLIIdentity(cmd.Context(), MeegleConfig{}, "original")
			if err != nil {
				return err
			}
			return context.DeadlineExceeded
		}}}
	}
	spawnTelemetryMaintenanceProcess = func(profile, key string, _ *telemetryRecord) error {
		t.Setenv(telemetryProfileEnv, profile)
		t.Setenv(telemetryRouteEnv, key)
		return nil
	}
	t.Cleanup(func() { os.Args, spawnTelemetryMaintenanceProcess = oldArgs, oldSpawn })
	tracker := StartCommandTelemetry(os.Args[1:])
	originalRoute := tracker.route
	ctx := tracker.Context(context.Background())
	app, err := NewCLIAppContext(ctx, "test", staticCommands)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "deferred" && tracker.route.IdentityKey != "" {
		t.Fatal("deferred root resolved identity at bootstrap")
	}
	app.RootCommand().AddCommand(&cobra.Command{Use: "probe", RunE: func(*cobra.Command, []string) error { return context.DeadlineExceeded }})
	tracker.ObserveKnownCommand(ctx, app.RootCommand(), os.Args[1:])
	executionErr := app.Execute(ctx, os.Args[1:])
	if tracker.route.Profile != "selected" || tracker.route.Host != sanitizeHost(server.URL) || tracker.route.IdentityKey == "" {
		t.Fatalf("incorrect resolved queue route: %#v", tracker.route)
	}
	// Verify identity routing through the reporter/queue and real HTTP boundaries.
	recordCtx, recordCancel := context.WithTimeout(context.Background(), 2*time.Second)
	recorded, recordErr := tracker.reporter.RecordContext(recordCtx, tracker.eventFromResult(executionErr, 1, false))
	recordCancel()
	if recordErr != nil || !recorded {
		t.Fatalf("record resolved event: recorded=%v err=%v", recorded, recordErr)
	}
	maybeSpawnTelemetryMaintenance(tracker.route)
	if telemetryevent.NewQueue(originalRoute.Root, telemetryevent.QueueOptions{}).HasEvents() {
		t.Fatal("event was written under the requested rather than resolved profile")
	}
	queue := telemetryevent.NewQueue(tracker.route.Root, telemetryevent.QueueOptions{})
	items, err := queue.List()
	if err != nil || len(items) != 1 || items[0].IdentityKey != tracker.route.IdentityKey {
		t.Fatalf("resolved queue events=%d err=%v", len(items), err)
	}
	for callID, binding := range map[string]string{"different-source": "different-source", "unbound": ""} {
		other := items[0].Event
		other.CallID = callID
		if err := telemetryevent.NewQueue(tracker.route.Root, telemetryevent.QueueOptions{IdentityKey: binding}).Enqueue(other); err != nil {
			t.Fatal(err)
		}
	}
	provider.mode = mode
	maintenanceCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if mode == "timeout" {
		cancel()
		maintenanceCtx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	} else if mode == "canceled" {
		cancel()
	}
	defer cancel()
	client := server.Client()
	_ = runTelemetryMaintenanceWithHTTPClient(maintenanceCtx, client)
	if client.CheckRedirect != nil {
		t.Fatal("maintenance mutated the supplied HTTP client")
	}
	if mode == "redirect" && (redirects.Load() != 2 || redirectedRequests.Load() != 0) {
		t.Fatalf("policy/report redirects: attempts=%d requests to other origin=%d, want 2/0", redirects.Load(), redirectedRequests.Load())
	}
	items, err = queue.List()
	wantReports, wantRemaining := int32(0), 3
	if mode == "same" || mode == "deferred" || mode == "nil-account" {
		wantReports, wantRemaining = 1, 2
	}
	if err != nil || reports.Load() != wantReports || len(items) != wantRemaining {
		t.Fatalf("reports=%d remaining=%d err=%v; want %d/%d", reports.Load(), len(items), err, wantReports, wantRemaining)
	}
}

func TestTelemetryIdentityBindingTracksProvenanceNotToken(t *testing.T) {
	base := ResolvedIdentity{ProfileName: "p", Host: "meegle.com", Source: SourceExtension, CredentialProvider: "one", AccessTokenHeader: "X-Token", Token: "secret"}
	want := telemetryRouteForIdentity(base)
	for _, field := range []string{"profile", "host", "source", "provider", "header", "token"} {
		changed := base
		switch field {
		case "profile":
			changed.ProfileName = "other"
		case "host":
			changed.Host = "project.feishu.cn"
		case "source":
			changed.Source = SourceConfig
		case "provider":
			changed.CredentialProvider = "two"
		case "header":
			changed.AccessTokenHeader = "X-Other"
		case "token":
			changed.Token = "rotated"
		}
		got := telemetryRouteForIdentity(changed)
		if (got.IdentityKey == want.IdentityKey) != (field == "token") {
			t.Errorf("%s changed binding incorrectly", field)
		}
		if strings.Contains(got.IdentityKey, changed.Token) {
			t.Error("binding contains token")
		}
	}
}

func TestTelemetryMaintenanceDoesNotRefreshStoredToken(t *testing.T) {
	setupTestDir(t)
	t.Setenv(telemetryControlEnv, "")
	t.Setenv(telemetryProfileEnv, "stored")
	t.Setenv(telemetryRouteEnv, "")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != telemetryevent.PolicyPath || r.Header.Get("Authorization") != "Bearer expired-token" {
			t.Error("maintenance attempted a refresh or changed stored authentication")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"policy": telemetryevent.DefaultPolicy(), "policy_from_valid_tcc": true}})
	}))
	defer server.Close()
	if err := SaveProfileConfig("stored", MeegleConfig{Host: server.URL}); err != nil {
		t.Fatal(err)
	}
	store := auth.CreateTokenStore("stored")
	want := &auth.TokenData{AccessToken: "expired-token", RefreshToken: "refresh-token", ExpiresAt: time.Now().Add(-time.Hour).Unix(), ClientID: "test-client"}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	if err := runTelemetryMaintenanceWithHTTPClient(context.Background(), server.Client()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got == nil || *got != *want || requests.Load() != 1 {
		t.Fatalf("stored token changed or policy was not fetched: requests=%d err=%v", requests.Load(), err)
	}
}
