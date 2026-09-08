// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package jsonvalue

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUnmarshalPreservesNumberLexemesInInterfaceSlots(t *testing.T) {
	var value any
	if err := Unmarshal([]byte(`{"integer":9007199254740993,"int64_boundary":9223372036854775807,"decimal":1.2300,"exponent":1e+30,"negative_zero":-0,"nested":{"items":[123456789012345678901234567890]}}`), &value); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	object := value.(map[string]any)
	wants := map[string]json.Number{
		"integer":        "9007199254740993",
		"int64_boundary": "9223372036854775807",
		"decimal":        "1.2300",
		"exponent":       "1e+30",
		"negative_zero":  "-0",
	}
	for key, want := range wants {
		if got := object[key]; got != want {
			t.Errorf("%s = %#v (%T), want %#v", key, got, got, want)
		}
	}
	nested := object["nested"].(map[string]any)["items"].([]any)[0]
	if want := json.Number("123456789012345678901234567890"); nested != want {
		t.Errorf("nested number = %#v (%T), want %#v", nested, nested, want)
	}
}

func TestUnmarshalKeepsTypedNumberFieldsTyped(t *testing.T) {
	target := struct {
		Count int64 `json:"count"`
	}{}
	if err := Unmarshal([]byte(`{"count":42}`), &target); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if target.Count != 42 {
		t.Fatalf("count = %d, want 42", target.Count)
	}
}

func TestUnmarshalRejectsTrailingJSONValue(t *testing.T) {
	var value any
	if err := Unmarshal([]byte(`1 2`), &value); err == nil {
		t.Fatal("expected error for multiple top-level values")
	}
}

func TestUnmarshalEmptyInputReportsInvalidJSONInsteadOfEOF(t *testing.T) {
	for _, input := range []string{"", " \n\t"} {
		var value any
		err := Unmarshal([]byte(input), &value)
		if err == nil {
			t.Fatalf("Unmarshal(%q) unexpectedly succeeded", input)
		}
		if got := err.Error(); !strings.Contains(got, "unexpected end of JSON input") {
			t.Fatalf("Unmarshal(%q) error = %q, want unexpected end of JSON input", input, got)
		}
	}
}

func TestParseNumberAcceptsOnlyJSONNumber(t *testing.T) {
	for _, raw := range []string{"9007199254740993", "1.2300", "1e+30", "-0"} {
		got, err := ParseNumber(raw)
		if err != nil {
			t.Fatalf("ParseNumber(%q): %v", raw, err)
		}
		if got.String() != raw {
			t.Errorf("ParseNumber(%q) = %q", raw, got)
		}
	}

	for _, raw := range []string{"01", "+1", "NaN", `"1"`, "null", "1 2"} {
		if _, err := ParseNumber(raw); err == nil {
			t.Errorf("ParseNumber(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestIsIntegerHandlesHugeExponentsWithoutExpandingThem(t *testing.T) {
	tests := map[json.Number]bool{
		"1":            true,
		"1.0":          true,
		"1.20":         false,
		"1.2e1":        true,
		"120e-1":       true,
		"12e-1":        false,
		"1e1000001":    true,
		"1e-1000001":   false,
		"0e-1000001":   true,
		"-0.00e999999": true,
	}
	for number, want := range tests {
		if got := IsInteger(number); got != want {
			t.Errorf("IsInteger(%q) = %v, want %v", number, got, want)
		}
	}
	if IsInteger(json.Number("not-a-number")) {
		t.Fatal("invalid json.Number must not be considered an integer")
	}
}

func TestInt64ConvertsExactIntegralFormsAndRejectsOverflow(t *testing.T) {
	valid := map[json.Number]int64{
		"1.0":                       1,
		"1e3":                       1000,
		"100000000000000000000e-20": 1,
		"9223372036854775807":       9223372036854775807,
		"-9223372036854775808":      -9223372036854775808,
	}
	for number, want := range valid {
		got, err := Int64(number)
		if err != nil || got != want {
			t.Errorf("Int64(%q) = (%d, %v), want (%d, nil)", number, got, err, want)
		}
	}
	for _, number := range []json.Number{"1.5", "9223372036854775808", "-9223372036854775809", "1e1000001"} {
		if _, err := Int64(number); err == nil {
			t.Errorf("Int64(%q) unexpectedly succeeded", number)
		}
	}
}
