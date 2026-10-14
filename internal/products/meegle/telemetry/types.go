// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

const (
	SchemaVersion = 1
	BasisPoints   = 10_000

	EventKindCommand = "command"

	StageCLIStart            = "cli_start"
	StagePreflight           = "preflight"
	StageAuthPrepare         = "auth_prepare"
	StageTransportConnect    = "transport_connect"
	StageMCPInitialize       = "mcp_initialize"
	StageCapabilityDiscovery = "capability_discovery"
	StageRequestBuild        = "request_build"
	StageRequestSend         = "request_send"
	StageResponseWait        = "response_wait"
	StageResponseParse       = "response_parse"
	StageObjectTransfer      = "object_storage_transfer"
	StageLocalFileVerify     = "local_file_verify"
	StageLocalOutput         = "local_output"
	StageShutdown            = "shutdown"

	OutcomeSuccess             = "success"
	OutcomeClientError         = "client_error"
	OutcomeAuthError           = "auth_error"
	OutcomeTransportError      = "transport_error"
	OutcomeBackendError        = "backend_error"
	OutcomeCLIPostprocessError = "cli_postprocess_error"
	OutcomePanic               = "panic"
	OutcomeCanceled            = "canceled"
	OutcomeAbnormalExit        = "abnormal_exit"

	ReachabilityNo      = "no"
	ReachabilityYes     = "yes"
	ReachabilityUnknown = "unknown"

	CategoryUser      = "user"
	CategoryAuth      = "auth"
	CategoryTransport = "transport"
	CategoryBackend   = "backend"
	CategoryInternal  = "internal"
	CategoryCanceled  = "canceled"

	HostClassMeego       = "meego"
	HostClassLarkProject = "larkproject"
	HostClassMeegle      = "meegle"
	HostClassCustom      = "custom"
	HostClassUnresolved  = "unresolved"

	ClientTypeInteractiveCLI = "interactive_cli"

	CredentialSourceStore     = "store"
	CredentialSourceEnv       = "env"
	CredentialSourceConfig    = "config"
	CredentialSourceExtension = "extension"
	CredentialSourceUnset     = "unset"
)

// BackendRef is a bounded, structured reference returned by a backend. It
// deliberately has no free-form message field.
type BackendRef struct {
	Service   string `json:"service"`
	Operation string `json:"operation"`
	LogID     string `json:"logid"`
}

// Event is the complete telemetry wire allowlist. Raw argv, flag values,
// paths, tokens, error messages, request bodies and response bodies have no
// representation in this type and therefore cannot be serialized by it.
type Event struct {
	SchemaVersion       int          `json:"schema_version"`
	Kind                string       `json:"kind"`
	CallID              string       `json:"call_id"`
	OccurredAtMS        int64        `json:"occurred_at_ms"`
	ClientVersion       string       `json:"client_version"`
	OS                  string       `json:"os"`
	Arch                string       `json:"arch"`
	Command             string       `json:"command"`
	FlagNames           []string     `json:"flag_names,omitempty"`
	Success             bool         `json:"success"`
	DurationMS          int64        `json:"duration_ms"`
	ExitCode            int          `json:"exit_code"`
	FailureStage        string       `json:"failure_stage,omitempty"`
	FinalOutcome        string       `json:"final_outcome"`
	BackendReachability string       `json:"backend_reachability"`
	Category            string       `json:"category,omitempty"`
	ErrorCode           string       `json:"error_code,omitempty"`
	Retryable           bool         `json:"retryable"`
	HTTPStatus          int          `json:"http_status"`
	HostClass           string       `json:"host_class"`
	ClientType          string       `json:"client_type"`
	CredentialSource    string       `json:"credential_source"`
	SampleRate          int          `json:"sample_rate"`
	SampleWeight        float64      `json:"sample_weight"`
	BackendRefs         []BackendRef `json:"backend_refs,omitempty"`
	Fingerprint         string       `json:"fingerprint,omitempty"`
	PolicyFromValidTCC  bool         `json:"policy_from_valid_tcc"`
}

var (
	callIDPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	commandPattern      = regexp.MustCompile(`^[a-z0-9+][a-z0-9+-]*( [a-z0-9+][a-z0-9+-]*)*$`)
	flagNamePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	errorCodePattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	logIDPattern        = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	fingerprintPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	runtimeTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

var allowedStages = stringSet(
	StageCLIStart, StagePreflight, StageAuthPrepare, StageTransportConnect,
	StageMCPInitialize, StageCapabilityDiscovery, StageRequestBuild,
	StageRequestSend, StageResponseWait, StageResponseParse, StageObjectTransfer,
	StageLocalFileVerify, StageLocalOutput, StageShutdown,
)

var allowedOutcomes = stringSet(
	OutcomeSuccess, OutcomeClientError, OutcomeAuthError, OutcomeTransportError,
	OutcomeBackendError, OutcomeCLIPostprocessError, OutcomePanic,
	OutcomeCanceled, OutcomeAbnormalExit,
)

var allowedCategories = stringSet(
	CategoryUser, CategoryAuth, CategoryTransport, CategoryBackend,
	CategoryInternal, CategoryCanceled,
)

var allowedHostClasses = stringSet(
	HostClassMeego, HostClassLarkProject, HostClassMeegle, HostClassCustom,
	HostClassUnresolved,
)

var allowedCredentialSources = stringSet(
	CredentialSourceStore, CredentialSourceEnv, CredentialSourceConfig,
	CredentialSourceExtension, CredentialSourceUnset,
)

var allowedBackendServices = stringSet("meegle_mcp", "meegle_api", "oauth", "object_storage")

var allowedBackendOperations = stringSet(
	"mcp_initialize", "tools_list", "tools_call", "handoff_config",
	"handoff_create_link", "preference_update", "attachment_upload",
	"attachment_download", "oauth_discovery", "oauth_token",
)

// Normalize returns a deterministic event without accepting additional data.
// It sorts and de-duplicates the two repeated allowlisted fields.
func (event Event) Normalize() Event {
	event.Command = strings.Join(strings.Fields(strings.ToLower(event.Command)), " ")
	event.FlagNames = uniqueSorted(event.FlagNames)
	if len(event.BackendRefs) > 0 {
		seen := make(map[string]struct{}, len(event.BackendRefs))
		refs := make([]BackendRef, 0, len(event.BackendRefs))
		for _, ref := range event.BackendRefs {
			if _, ok := allowedBackendServices[ref.Service]; !ok {
				continue
			}
			if _, ok := allowedBackendOperations[ref.Operation]; !ok || !logIDPattern.MatchString(ref.LogID) {
				continue
			}
			key := ref.Service + "\x00" + ref.Operation + "\x00" + ref.LogID
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			refs = append(refs, ref)
			if len(refs) == 8 {
				break
			}
		}
		event.BackendRefs = refs
	}
	return event
}

func (event Event) Validate() error {
	if event.SchemaVersion != SchemaVersion || event.Kind != EventKindCommand {
		return fmt.Errorf("unsupported event schema or kind")
	}
	if !callIDPattern.MatchString(event.CallID) {
		return fmt.Errorf("call_id is invalid")
	}
	if event.OccurredAtMS < 0 {
		return fmt.Errorf("occurred_at_ms is invalid")
	}
	for name, value := range map[string]string{
		"client_version": event.ClientVersion, "os": event.OS, "arch": event.Arch,
	} {
		if value != "" && !runtimeTokenPattern.MatchString(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	if len(event.Command) > 128 || !commandPattern.MatchString(event.Command) {
		return fmt.Errorf("command is invalid")
	}
	if len(event.FlagNames) > 64 {
		return fmt.Errorf("too many flag names")
	}
	seenFlags := make(map[string]struct{}, len(event.FlagNames))
	for _, name := range event.FlagNames {
		if !flagNamePattern.MatchString(name) {
			return fmt.Errorf("flag name %q is invalid", name)
		}
		if _, exists := seenFlags[name]; exists {
			return fmt.Errorf("duplicate flag name %q", name)
		}
		seenFlags[name] = struct{}{}
	}
	if event.DurationMS < 0 || event.ExitCode < 0 || event.ExitCode > 255 {
		return fmt.Errorf("duration or exit code is invalid")
	}
	if event.Success {
		if event.ExitCode != 0 || event.FailureStage != "" || event.FinalOutcome != OutcomeSuccess || event.Category != "" || event.ErrorCode != "" || event.Retryable || event.HTTPStatus != 0 || event.Fingerprint != "" {
			return fmt.Errorf("success event contains failure fields")
		}
	} else {
		if _, ok := allowedStages[event.FailureStage]; !ok {
			return fmt.Errorf("failure_stage is invalid")
		}
		if _, ok := allowedCategories[event.Category]; !ok {
			return fmt.Errorf("category is invalid")
		}
		if !errorCodePattern.MatchString(event.ErrorCode) {
			return fmt.Errorf("error_code is invalid")
		}
		if !fingerprintPattern.MatchString(event.Fingerprint) {
			return fmt.Errorf("fingerprint is invalid")
		}
	}
	if event.HTTPStatus != 0 && (event.HTTPStatus < 100 || event.HTTPStatus > 599) {
		return fmt.Errorf("http_status is invalid")
	}
	if _, ok := allowedOutcomes[event.FinalOutcome]; !ok {
		return fmt.Errorf("final_outcome is invalid")
	}
	if event.BackendReachability != ReachabilityNo && event.BackendReachability != ReachabilityYes && event.BackendReachability != ReachabilityUnknown {
		return fmt.Errorf("backend_reachability is invalid")
	}
	if _, ok := allowedHostClasses[event.HostClass]; !ok {
		return fmt.Errorf("host_class is invalid")
	}
	if event.ClientType != ClientTypeInteractiveCLI {
		return fmt.Errorf("client_type is invalid")
	}
	if _, ok := allowedCredentialSources[event.CredentialSource]; !ok {
		return fmt.Errorf("credential_source is invalid")
	}
	if event.SampleRate < 0 || event.SampleRate > BasisPoints || event.SampleWeight < 0 || math.IsNaN(event.SampleWeight) || math.IsInf(event.SampleWeight, 0) {
		return fmt.Errorf("sampling fields are invalid")
	}
	if len(event.BackendRefs) > 8 {
		return fmt.Errorf("too many backend references")
	}
	for _, ref := range event.BackendRefs {
		if _, ok := allowedBackendServices[ref.Service]; !ok {
			return fmt.Errorf("backend service is invalid")
		}
		if _, ok := allowedBackendOperations[ref.Operation]; !ok {
			return fmt.Errorf("backend operation is invalid")
		}
		if !logIDPattern.MatchString(ref.LogID) {
			return fmt.Errorf("backend logid is invalid")
		}
	}
	return nil
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
