// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	FilterInclude = "include"
	FilterExclude = "exclude"

	MaxReportEvents = 50
	MaxReportBytes  = 256 * 1024
	MaxEventBytes   = 16 * 1024

	PolicyRefreshInterval = 12 * time.Hour
	PolicyRetryInterval   = time.Hour

	policyFilename = "policy.json"
)

type Filter struct {
	Mode   string   `json:"mode"`
	Values []string `json:"values,omitempty"`
}

type SamplingRates struct {
	SuccessRate int `json:"success_rate"`
	FailureRate int `json:"failure_rate"`
}

type CommandSamplingRule struct {
	Command     string `json:"command"`
	SuccessRate int    `json:"success_rate"`
	FailureRate int    `json:"failure_rate"`
}

type SamplingPolicy struct {
	Default      SamplingRates         `json:"default"`
	CommandRules []CommandSamplingRule `json:"command_rules,omitempty"`
}

type Policy struct {
	Enabled       bool           `json:"enabled"`
	CommandFilter Filter         `json:"command_filter"`
	ErrorFilter   Filter         `json:"error_filter"`
	Sampling      SamplingPolicy `json:"sampling"`
}

type PolicyState struct {
	Policy             Policy    `json:"policy"`
	PolicyFromValidTCC bool      `json:"policy_from_valid_tcc"`
	HasValidTCCPolicy  bool      `json:"has_valid_tcc_policy,omitempty"`
	LastRefreshAttempt time.Time `json:"last_refresh_attempt,omitempty"`
}

func DefaultPolicy() Policy {
	return Policy{
		Enabled:       true,
		CommandFilter: Filter{Mode: FilterExclude, Values: []string{}},
		ErrorFilter:   Filter{Mode: FilterExclude, Values: []string{}},
		Sampling: SamplingPolicy{
			Default:      SamplingRates{SuccessRate: 0, FailureRate: BasisPoints},
			CommandRules: []CommandSamplingRule{},
		},
	}
}

func (policy Policy) Validate() error {
	if err := validatePolicyFilter("command_filter", policy.CommandFilter); err != nil {
		return err
	}
	if err := validatePolicyFilter("error_filter", policy.ErrorFilter); err != nil {
		return err
	}
	if err := validatePolicyRates("sampling.default", policy.Sampling.Default); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(policy.Sampling.CommandRules))
	for index, rule := range policy.Sampling.CommandRules {
		if rule.Command == "" || strings.TrimSpace(rule.Command) != rule.Command {
			return fmt.Errorf("sampling.command_rules[%d].command is invalid", index)
		}
		if _, ok := seen[rule.Command]; ok {
			return fmt.Errorf("sampling.command_rules contains duplicate command %q", rule.Command)
		}
		seen[rule.Command] = struct{}{}
		if err := validatePolicyRates(fmt.Sprintf("sampling.command_rules[%d]", index), SamplingRates{
			SuccessRate: rule.SuccessRate,
			FailureRate: rule.FailureRate,
		}); err != nil {
			return err
		}
	}
	return nil
}

func validatePolicyFilter(name string, filter Filter) error {
	if filter.Mode != FilterInclude && filter.Mode != FilterExclude {
		return fmt.Errorf("%s.mode must be include or exclude", name)
	}
	return nil
}

func validatePolicyRates(name string, rates SamplingRates) error {
	if rates.SuccessRate != 0 {
		return fmt.Errorf("%s.success_rate must be 0 in v1", name)
	}
	if rates.FailureRate < 0 || rates.FailureRate > BasisPoints {
		return fmt.Errorf("%s.failure_rate must be between 0 and %d", name, BasisPoints)
	}
	return nil
}

func (policy Policy) SampleRate(event Event) (int, bool) {
	if !policy.Enabled || !filterAllows(policy.CommandFilter, event.Command) || !filterAllows(policy.ErrorFilter, event.ErrorCode) {
		return 0, false
	}
	rate := policy.Sampling.Default.FailureRate
	if event.Success {
		rate = policy.Sampling.Default.SuccessRate
	}
	for _, rule := range policy.Sampling.CommandRules {
		if rule.Command == event.Command {
			rate = rule.FailureRate
			if event.Success {
				rate = rule.SuccessRate
			}
			break
		}
	}
	return rate, rate > 0
}

func filterAllows(filter Filter, value string) bool {
	found := false
	for _, candidate := range filter.Values {
		if candidate == value {
			found = true
			break
		}
	}
	if filter.Mode == FilterInclude {
		return found
	}
	return !found
}

func ShouldSample(callID string, rate int) bool {
	if rate <= 0 {
		return false
	}
	if rate >= BasisPoints {
		return true
	}
	digest := sha256.Sum256([]byte(callID))
	value := binary.BigEndian.Uint64(digest[:8]) % BasisPoints
	return int(value) < rate
}

type PolicyStore struct {
	path    string
	lockDir string
}

func NewPolicyStore(root string) *PolicyStore {
	return &PolicyStore{
		path:    filepath.Join(root, policyFilename),
		lockDir: filepath.Join(root, ".policy-lock"),
	}
}

func (store *PolicyStore) Load() (PolicyState, error) {
	state, found, err := store.loadRaw()
	if err != nil {
		return PolicyState{}, err
	}
	if !found {
		return PolicyState{Policy: DefaultPolicy()}, nil
	}
	if err := state.Policy.Validate(); err != nil {
		return PolicyState{}, err
	}
	return state, nil
}

func (store *PolicyStore) RefreshDue(now time.Time) bool {
	state, err := store.Load()
	if err != nil || state.LastRefreshAttempt.IsZero() {
		return true
	}
	interval := PolicyRetryInterval
	if state.PolicyFromValidTCC {
		interval = PolicyRefreshInterval
	}
	return !now.Before(state.LastRefreshAttempt.Add(interval))
}

func (store *PolicyStore) ApplyRefresh(candidate Policy, fromValidTCC bool, attemptedAt time.Time) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	return store.update(func(state *PolicyState) {
		if fromValidTCC || !state.HasValidTCCPolicy {
			state.Policy = clonePolicy(candidate)
		}
		if fromValidTCC {
			state.HasValidTCCPolicy = true
		}
		state.PolicyFromValidTCC = fromValidTCC
		state.LastRefreshAttempt = attemptedAt.UTC()
	})
}

func (store *PolicyStore) RecordRefreshFailure(attemptedAt time.Time) error {
	return store.update(func(state *PolicyState) {
		state.PolicyFromValidTCC = false
		state.LastRefreshAttempt = attemptedAt.UTC()
	})
}

func (store *PolicyStore) update(mutate func(*PolicyState)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := NewProcessLock(store.lockDir).Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	state, found, err := store.loadRaw()
	if err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &syntaxErr) && !errors.As(err, &typeErr) {
			return err
		}
		// A malformed local cache is not authoritative. Rebuild it from the
		// incoming refresh state so one bad write cannot permanently prevent
		// future valid policies from taking effect.
		state = PolicyState{Policy: DefaultPolicy()}
		found = false
	}
	if !found {
		state.Policy = DefaultPolicy()
	}
	mutate(&state)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicWriteFile(store.path, data, 0600)
}

func (store *PolicyStore) loadRaw() (PolicyState, bool, error) {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return PolicyState{}, false, nil
	}
	if err != nil {
		return PolicyState{}, false, err
	}
	var state PolicyState
	if err := json.Unmarshal(data, &state); err != nil {
		return PolicyState{}, false, err
	}
	return state, true, nil
}

func clonePolicy(policy Policy) Policy {
	policy.CommandFilter.Values = append([]string(nil), policy.CommandFilter.Values...)
	policy.ErrorFilter.Values = append([]string(nil), policy.ErrorFilter.Values...)
	policy.Sampling.CommandRules = append([]CommandSamplingRule(nil), policy.Sampling.CommandRules...)
	return policy
}
