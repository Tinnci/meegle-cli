// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

// Package observability carries transport-neutral execution facts through a
// command context. Products decide whether and how those facts are reported.
package observability

import (
	"context"
	"sort"
	"strings"
	"sync"
)

type contextKey struct{}

type BackendRef struct {
	Service   string
	Operation string
	LogID     string
}

type Snapshot struct {
	Command          string
	FlagNames        []string
	Stage            string
	Step             string
	Reachability     string
	BackendRefs      []BackendRef
	HostClass        string
	CredentialSource string
}

type Trace struct {
	mu       sync.Mutex
	snapshot Snapshot
}

func NewTrace() *Trace {
	return &Trace{snapshot: Snapshot{Reachability: "unknown"}}
}

func WithTrace(ctx context.Context, trace *Trace) context.Context {
	if trace == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, trace)
}

func FromContext(ctx context.Context) *Trace {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(contextKey{}).(*Trace)
	return trace
}

func SetCommand(ctx context.Context, path []string, flagNames []string) {
	trace := FromContext(ctx)
	if trace == nil {
		return
	}
	flags := append([]string(nil), flagNames...)
	sort.Strings(flags)
	trace.mu.Lock()
	trace.snapshot.Command = strings.Join(path, " ")
	trace.snapshot.FlagNames = flags
	trace.mu.Unlock()
}

func SetStage(ctx context.Context, stage string) {
	trace := FromContext(ctx)
	if trace == nil {
		return
	}
	trace.mu.Lock()
	trace.snapshot.Stage = stage
	trace.mu.Unlock()
}

func SetStep(ctx context.Context, step string) {
	trace := FromContext(ctx)
	if trace == nil {
		return
	}
	trace.mu.Lock()
	trace.snapshot.Step = step
	// A detailed stage belongs only to the step that recorded it.
	trace.snapshot.Stage = ""
	trace.mu.Unlock()
}

func SetReachability(ctx context.Context, reachability string) {
	trace := FromContext(ctx)
	if trace == nil {
		return
	}
	trace.mu.Lock()
	trace.snapshot.Reachability = reachability
	trace.mu.Unlock()
}

// SetEnvironment stores only product-defined enum labels. Tokens, raw hosts
// and profile names have no field in the trace.
func SetEnvironment(ctx context.Context, hostClass, credentialSource string) {
	trace := FromContext(ctx)
	if trace == nil {
		return
	}
	trace.mu.Lock()
	trace.snapshot.HostClass = hostClass
	trace.snapshot.CredentialSource = credentialSource
	trace.mu.Unlock()
}

func AddBackendRef(ctx context.Context, ref BackendRef) {
	trace := FromContext(ctx)
	if trace == nil || ref.LogID == "" || len(ref.Service) > 64 || len(ref.Operation) > 64 || len(ref.LogID) > 128 {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	for _, existing := range trace.snapshot.BackendRefs {
		if existing == ref {
			return
		}
	}
	if len(trace.snapshot.BackendRefs) < 8 {
		trace.snapshot.BackendRefs = append(trace.snapshot.BackendRefs, ref)
	}
}

func (trace *Trace) Snapshot() Snapshot {
	if trace == nil {
		return Snapshot{Reachability: "unknown"}
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	result := trace.snapshot
	result.FlagNames = append([]string(nil), result.FlagNames...)
	result.BackendRefs = append([]BackendRef(nil), result.BackendRefs...)
	return result
}
