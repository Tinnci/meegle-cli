// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package router

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	frameworkerrors "github.com/larksuite/meegle-cli/pkg/framework/errors"
	"github.com/larksuite/meegle-cli/pkg/framework/jsonvalue"
	"github.com/larksuite/meegle-cli/pkg/framework/registry"
)

const exactNumericFlagTypeAnnotation = "lark-project-cli/exact-numeric-flag-type"

func collectFlags(cmd *cobra.Command) (map[string]any, map[string]any, map[string]string, []RouteDiagnostic, error) {
	effective := make(map[string]any)
	explicit := make(map[string]any)
	raw := make(map[string]string)
	diagnostics := make([]RouteDiagnostic, 0)
	collect := func(fs *pflag.FlagSet) error {
		var visitErr error
		fs.VisitAll(func(flag *pflag.Flag) {
			if visitErr != nil {
				return
			}
			if flag == nil || flag.Name == "help" {
				return
			}
			value, err := readFlagValue(fs, flag)
			if err != nil {
				visitErr = err
				return
			}
			effective[flag.Name] = value
			if flag.Changed {
				explicit[flag.Name] = value
				raw[flag.Name] = flag.Value.String()
			}
		})
		return visitErr
	}
	if err := collect(cmd.InheritedFlags()); err != nil {
		return nil, nil, nil, nil, err
	}
	if err := collect(cmd.Flags()); err != nil {
		return nil, nil, nil, nil, err
	}
	_ = diagnostics
	return effective, explicit, raw, diagnostics, nil
}

func readFlagValue(fs *pflag.FlagSet, flag *pflag.Flag) (any, error) {
	exactType := exactNumericFlagType(flag)
	if exactType == registry.FlagTypeIntegerSlice {
		rawValues, err := fs.GetStringSlice(flag.Name)
		if err != nil {
			return nil, err
		}
		values := make([]json.Number, 0, len(rawValues))
		for _, raw := range rawValues {
			number, err := jsonvalue.ParseNumber(raw)
			if err != nil || !jsonvalue.IsInteger(number) {
				return nil, invalidExactNumericFlag(flag.Name, exactType)
			}
			values = append(values, number)
		}
		return values, nil
	}
	if exactType == registry.FlagTypeNumber || exactType == registry.FlagTypeInteger {
		raw, err := fs.GetString(flag.Name)
		if err != nil {
			return nil, err
		}
		number, err := jsonvalue.ParseNumber(raw)
		if err != nil {
			return nil, invalidExactNumericFlag(flag.Name, exactType)
		}
		if exactType == registry.FlagTypeInteger && !jsonvalue.IsInteger(number) {
			return nil, invalidExactNumericFlag(flag.Name, exactType)
		}
		return number, nil
	}
	switch flag.Value.Type() {
	case "string":
		return fs.GetString(flag.Name)
	case "int":
		return fs.GetInt(flag.Name)
	case "bool":
		return fs.GetBool(flag.Name)
	case "float64":
		return fs.GetFloat64(flag.Name)
	case "stringSlice":
		return fs.GetStringSlice(flag.Name)
	case "stringArray":
		return fs.GetStringArray(flag.Name)
	case "intSlice":
		return fs.GetIntSlice(flag.Name)
	default:
		return nil, frameworkerrors.New(frameworkerrors.CategoryInternal, frameworkerrors.CodeInternal, fmt.Sprintf("unsupported flag type %s", flag.Value.Type()))
	}
}

func invalidExactNumericFlag(name, exactType string) error {
	return frameworkerrors.New(frameworkerrors.CategoryUser, frameworkerrors.CodeParamInvalid, fmt.Sprintf("--%s must be a valid JSON %s", name, exactType))
}

func exactNumericFlagType(flag *pflag.Flag) string {
	if flag == nil || flag.Annotations == nil {
		return ""
	}
	values := flag.Annotations[exactNumericFlagTypeAnnotation]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func stringDefault(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return ""
}

func intDefault(value any) int {
	if typed, ok := value.(int); ok {
		return typed
	}
	return 0
}

func boolDefault(value any) bool {
	if typed, ok := value.(bool); ok {
		return typed
	}
	return false
}

func floatDefault(value any) float64 {
	if typed, ok := value.(float64); ok {
		return typed
	}
	return 0
}

func exactNumberDefault(value any) string {
	switch typed := value.(type) {
	case json.Number:
		return typed.String()
	case string:
		if _, err := jsonvalue.ParseNumber(typed); err == nil {
			return typed
		}
	case float64:
		return fmt.Sprint(typed)
	case float32:
		return fmt.Sprint(typed)
	case int:
		return fmt.Sprint(typed)
	case int64:
		return fmt.Sprint(typed)
	}
	return "0"
}

func exactIntegerSliceDefault(value any) []string {
	var raw []string
	switch typed := value.(type) {
	case []json.Number:
		raw = make([]string, len(typed))
		for i, item := range typed {
			raw[i] = item.String()
		}
	case []string:
		raw = append([]string(nil), typed...)
	case []int:
		raw = make([]string, len(typed))
		for i, item := range typed {
			raw[i] = fmt.Sprint(item)
		}
	case []int64:
		raw = make([]string, len(typed))
		for i, item := range typed {
			raw[i] = fmt.Sprint(item)
		}
	default:
		return nil
	}
	for _, item := range raw {
		number, err := jsonvalue.ParseNumber(item)
		if err != nil || !jsonvalue.IsInteger(number) {
			return nil
		}
	}
	return raw
}

func stringSliceDefault(value any) []string {
	if typed, ok := value.([]string); ok {
		return append([]string(nil), typed...)
	}
	return nil
}

func intSliceDefault(value any) []int {
	if typed, ok := value.([]int); ok {
		return append([]int(nil), typed...)
	}
	return nil
}
