// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	frameworkadapter "github.com/larksuite/meegle-cli/pkg/framework/adapter"
	frameworkerrors "github.com/larksuite/meegle-cli/pkg/framework/errors"
	"github.com/larksuite/meegle-cli/pkg/framework/registry"
)

func TestCommandRouterRoutesAndSeparatesExplicitFlags(t *testing.T) {
	manager := registry.NewManager(registry.NewStaticSetup(testRouterTree()))
	if err := manager.Init(context.Background()); err != nil {
		t.Fatalf("init manager: %v", err)
	}
	r, err := NewCommandRouter(manager, "test")
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	parsed, err := r.Route(&frameworkadapter.RawInput{Context: context.Background(), Args: []string{"workitem", "update", "WI-1", "--query", "abc"}})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if parsed == nil || parsed.Node == nil || parsed.Node.Name != "update" {
		t.Fatalf("parsed node = %#v", parsed)
	}
	if got := parsed.Args[0]; got != "WI-1" {
		t.Fatalf("arg id = %q", got)
	}
	if got := parsed.Flags["limit"]; got != 20 {
		t.Fatalf("effective limit = %#v", got)
	}
	if got := parsed.Flags["query"]; got != "abc" {
		t.Fatalf("effective query = %#v", got)
	}
	if _, ok := parsed.ExplicitFlags["limit"]; ok {
		t.Fatalf("limit should not be explicit: %#v", parsed.ExplicitFlags)
	}
	if got := parsed.ExplicitFlags["query"]; got != "abc" {
		t.Fatalf("explicit query = %#v", got)
	}
}

func TestCommandRouterDefersRequiredFlagValidationToPipeline(t *testing.T) {
	r := newTestRouter(t)
	parsed, err := r.Route(&frameworkadapter.RawInput{Context: context.Background(), Args: []string{"workitem", "create"}})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if parsed == nil || parsed.Node == nil || parsed.Node.Name != "create" {
		t.Fatalf("parsed node = %#v", parsed)
	}
}

func TestCommandRouterWrapsUnknownCommandError(t *testing.T) {
	r := newTestRouter(t)
	_, err := r.Route(&frameworkadapter.RawInput{Context: context.Background(), Args: []string{"missing", "command"}})
	assertCLIError(t, err, frameworkerrors.CodeCommandNotFound)
}

func TestCommandRouterStillRendersValidGroupHelp(t *testing.T) {
	r := newTestRouter(t)
	var stdout bytes.Buffer
	err := r.Execute(&frameworkadapter.RawInput{
		Context: context.Background(),
		Args:    []string{"workitem", "--profile", "demo", "--help"},
		IOMode:  frameworkadapter.IOModeTTY,
		Stdout:  &stdout,
	}, nil)
	if err != nil {
		t.Fatalf("execute valid group help: %v", err)
	}
	if !strings.Contains(stdout.String(), "Available Commands:") {
		t.Fatalf("valid group help missing commands:\n%s", stdout.String())
	}
}

func TestCommandRouterCommandGroupWithoutLeafStillShowsHelpSuccessfully(t *testing.T) {
	r := newTestRouter(t)
	parsed, err := r.Route(&frameworkadapter.RawInput{
		Context: context.Background(),
		Args:    []string{"workitem"},
		IOMode:  frameworkadapter.IOModeProgrammatic,
	})
	if err != nil {
		t.Fatalf("route command group: %v", err)
	}
	if parsed != nil {
		t.Fatalf("parsed command group = %#v, want nil", parsed)
	}
}

func TestCommandRouterHelpIncludesAppNameAndRequiredFlag(t *testing.T) {
	r := newTestRouter(t)
	var stdout bytes.Buffer
	err := r.Execute(&frameworkadapter.RawInput{
		Context: context.Background(),
		Args:    []string{"workitem", "create", "--help"},
		IOMode:  frameworkadapter.IOModeTTY,
		Stdout:  &stdout,
	}, nil)
	if err != nil {
		t.Fatalf("execute help: %v", err)
	}
	for _, fragment := range []string{
		"Usage:\n  test workitem create [flags]",
		"--key string",
		"(required)",
		"--profile string",
	} {
		if !strings.Contains(stdout.String(), fragment) {
			t.Fatalf("help output missing %q:\n%s", fragment, stdout.String())
		}
	}
}

func TestCommandRouterWrapsUnknownFlagError(t *testing.T) {
	r := newTestRouter(t)
	_, err := r.Route(&frameworkadapter.RawInput{Context: context.Background(), Args: []string{"workitem", "create", "--not-exist"}})
	assertCLIError(t, err, frameworkerrors.CodeParamInvalid)
}

// FlagTypeStringArray must bypass cobra's CSV splitting so JSON-shaped values
// survive verbatim. Each --flag invocation stores one element; commas and
// double quotes inside the value are preserved. This is the difference from
// FlagTypeStringSlice that makes --fields usable for object-shaped payloads.
func TestCommandRouterStringArrayPreservesJSONAndRepeats(t *testing.T) {
	manager := registry.NewManager(registry.NewStaticSetup(&registry.CommandTree{
		Nodes: []*registry.CommandNode{{
			Name:       "run",
			Help:       registry.HelpText{Brief: "Run with string-array flag"},
			HandlerRef: "core.run",
			Flags: []registry.FlagDef{
				{Name: "fields", Type: registry.FlagTypeStringArray},
			},
		}},
	}))
	if err := manager.Init(context.Background()); err != nil {
		t.Fatalf("init manager: %v", err)
	}
	r, err := NewCommandRouter(manager, "test")
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	parsed, err := r.Route(&frameworkadapter.RawInput{Context: context.Background(), Args: []string{
		"run",
		"--fields", `{"field_key":"name","field_value":"A"}`,
		"--fields", `{"field_key":"priority","field_value":"P1"}`,
	}})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	raw, ok := parsed.Flags["fields"].([]string)
	if !ok {
		t.Fatalf("Flags[fields] type = %T, want []string", parsed.Flags["fields"])
	}
	want := []string{
		`{"field_key":"name","field_value":"A"}`,
		`{"field_key":"priority","field_value":"P1"}`,
	}
	if len(raw) != len(want) {
		t.Fatalf("got %d elements, want %d: %#v", len(raw), len(want), raw)
	}
	for i, w := range want {
		if raw[i] != w {
			t.Errorf("element %d = %q, want %q (CSV splitting must not fire for StringArray)", i, raw[i], w)
		}
	}
}

func TestParseInputPreservesExactNumericFlags(t *testing.T) {
	reg, err := registry.New(&registry.CommandTree{Nodes: []*registry.CommandNode{{
		Name:       "run",
		Help:       registry.HelpText{Brief: "Run"},
		HandlerRef: "core.run",
		Flags: []registry.FlagDef{
			{Name: "amount", Type: registry.FlagTypeNumber},
			{Name: "id", Type: registry.FlagTypeInteger},
			{Name: "ids", Type: registry.FlagTypeIntegerSlice},
		},
	}}})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	parsed, err := ParseInput(reg, &frameworkadapter.RawInput{Args: []string{
		"run",
		"--amount=1.2300",
		"--id=123456789012345678901234567890",
		"--ids=9007199254740993,9223372036854775808",
		"--ids=3",
	}})
	if err != nil {
		t.Fatalf("parse input: %v", err)
	}
	if got, want := parsed.Flags["amount"], json.Number("1.2300"); got != want {
		t.Fatalf("amount = %#v (%T), want %#v", got, got, want)
	}
	if got, want := parsed.Flags["id"], json.Number("123456789012345678901234567890"); got != want {
		t.Fatalf("id = %#v (%T), want %#v", got, got, want)
	}
	ids, ok := parsed.Flags["ids"].([]json.Number)
	if !ok {
		t.Fatalf("ids type = %T, want []json.Number", parsed.Flags["ids"])
	}
	for i, want := range []json.Number{"9007199254740993", "9223372036854775808", "3"} {
		if got := ids[i]; got != want {
			t.Fatalf("ids[%d] = %s, want %s", i, got, want)
		}
	}
}

func TestParseInputAcceptsSeparatedNegativeExactNumbers(t *testing.T) {
	reg, err := registry.New(&registry.CommandTree{Nodes: []*registry.CommandNode{{
		Name:       "run",
		Help:       registry.HelpText{Brief: "Run"},
		HandlerRef: "core.run",
		Flags: []registry.FlagDef{
			{Name: "amount", Short: "a", Type: registry.FlagTypeNumber},
			{Name: "id", Short: "i", Type: registry.FlagTypeInteger},
			{Name: "ids", Short: "s", Type: registry.FlagTypeIntegerSlice},
		},
	}}})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	parsed, err := ParseInput(reg, &frameworkadapter.RawInput{Args: []string{
		"run", "--amount", "-1.2300", "-i", "-0", "--ids", "-1,-2", "-s", "-3",
	}})
	if err != nil {
		t.Fatalf("parse input: %v", err)
	}
	if got, want := parsed.Flags["amount"], json.Number("-1.2300"); got != want {
		t.Fatalf("amount = %#v, want %#v", got, want)
	}
	if got, want := parsed.Flags["id"], json.Number("-0"); got != want {
		t.Fatalf("id = %#v, want %#v", got, want)
	}
	ids, ok := parsed.Flags["ids"].([]json.Number)
	if !ok {
		t.Fatalf("ids type = %T, want []json.Number", parsed.Flags["ids"])
	}
	for i, want := range []json.Number{"-1", "-2", "-3"} {
		if got := ids[i]; got != want {
			t.Fatalf("ids[%d] = %s, want %s", i, got, want)
		}
	}
}

func TestCommandRouterPreservesExactIntegerSliceDefault(t *testing.T) {
	manager := registry.NewManager(registry.NewStaticSetup(&registry.CommandTree{Nodes: []*registry.CommandNode{{
		Name:       "run",
		Help:       registry.HelpText{Brief: "Run"},
		HandlerRef: "core.run",
		Flags: []registry.FlagDef{{
			Name: "ids", Type: registry.FlagTypeIntegerSlice,
			Default: []json.Number{"9007199254740993", "1e3"},
		}},
	}}}))
	if err := manager.Init(context.Background()); err != nil {
		t.Fatalf("init manager: %v", err)
	}
	r, err := NewCommandRouter(manager, "test")
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	parsed, err := r.Route(&frameworkadapter.RawInput{Context: context.Background(), Args: []string{"run"}})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	ids, ok := parsed.Flags["ids"].([]json.Number)
	if !ok || len(ids) != 2 || ids[0] != "9007199254740993" || ids[1] != "1e3" {
		t.Fatalf("ids = %#v (%T)", parsed.Flags["ids"], parsed.Flags["ids"])
	}
}

func TestParseInputRejectsInvalidExactNumericFlags(t *testing.T) {
	reg, err := registry.New(&registry.CommandTree{Nodes: []*registry.CommandNode{{
		Name:       "run",
		Help:       registry.HelpText{Brief: "Run"},
		HandlerRef: "core.run",
		Flags: []registry.FlagDef{
			{Name: "amount", Type: registry.FlagTypeNumber},
			{Name: "id", Type: registry.FlagTypeInteger},
			{Name: "ids", Type: registry.FlagTypeIntegerSlice},
		},
	}}})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	for _, args := range [][]string{
		{"run", "--amount=01"},
		{"run", "--id=1.5"},
		{"run", "--ids=1,two"},
		{"run", "--amount", "-01"},
		{"run", "--id", "-1.5"},
		{"run", "--ids", "-1,two"},
	} {
		_, err := ParseInput(reg, &frameworkadapter.RawInput{Args: args})
		assertCLIError(t, err, frameworkerrors.CodeParamInvalid)
	}
}

func newTestRouter(t *testing.T) *CommandRouter {
	t.Helper()
	manager := registry.NewManager(registry.NewStaticSetup(testRouterTree()))
	if err := manager.Init(context.Background()); err != nil {
		t.Fatalf("init manager: %v", err)
	}
	r, err := NewCommandRouter(manager, "test")
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	return r
}

func testRouterTree() *registry.CommandTree {
	return &registry.CommandTree{
		GlobalFlags: []registry.FlagDef{{Name: "profile", Type: registry.FlagTypeString, Description: "Select profile"}},
		Nodes: []*registry.CommandNode{{
			Name: "workitem",
			Help: registry.HelpText{Brief: "Manage work items"},
			Children: []*registry.CommandNode{{
				Name:       "update",
				Help:       registry.HelpText{Brief: "Update a work item"},
				HandlerRef: "core.workitem.update",
				Args:       []registry.ArgDef{{Name: "id", Required: true}},
				Flags: []registry.FlagDef{
					{Name: "query", Type: registry.FlagTypeString},
					{Name: "limit", Type: registry.FlagTypeInt, Default: 20},
				},
			}, {
				Name:       "create",
				Help:       registry.HelpText{Brief: "Create a work item"},
				HandlerRef: "core.workitem.create",
				Flags: []registry.FlagDef{
					{Name: "key", Type: registry.FlagTypeString, Required: true},
				},
			}},
		}},
	}
}

func assertCLIError(t *testing.T, err error, code string) {
	t.Helper()
	cliErr := frameworkerrors.As(err)
	if cliErr == nil {
		t.Fatalf("expected CLIError, got %T (%v)", err, err)
	}
	if cliErr.Code != code {
		t.Fatalf("error code = %q, want %q", cliErr.Code, code)
	}
}
