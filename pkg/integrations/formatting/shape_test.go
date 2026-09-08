// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package formatting

import (
	"encoding/json"
	"testing"
)

func TestDetectShape(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want DataShape
	}{
		{"nil", nil, ShapeEmpty},
		{"empty map", map[string]any{}, ShapeEmpty},
		{"empty array", []any{}, ShapeEmpty},
		{"primitive string", "s", ShapePrimitive},
		{"primitive int", 42, ShapePrimitive},
		{"map", map[string]any{"a": 1}, ShapeObject},
		{"primitive array", []any{"a", "b"}, ShapePrimitiveArray},
		{"object array", []any{map[string]any{"a": 1}, map[string]any{"b": 2}}, ShapeObjectArray},
		{"mixed array", []any{"x", map[string]any{"a": 1}}, ShapeMixedArray},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectShape(c.in); got != c.want {
				t.Fatalf("want %v got %v", c.want, got)
			}
		})
	}
}

func TestDetectShape_DecodesRawMessage(t *testing.T) {
	raw := json.RawMessage(`[{"a":1},{"a":2}]`)
	if got := DetectShape(raw); got != ShapeObjectArray {
		t.Fatalf("want ObjectArray, got %v", got)
	}
}

func TestNormalizeRawMessagePreservesJSONNumberLexemes(t *testing.T) {
	raw := json.RawMessage(`{"large":9007199254740993,"decimal":1.2300,"exponent":1e+30}`)
	got := Normalize(raw).(map[string]any)
	wants := map[string]json.Number{
		"large":    "9007199254740993",
		"decimal":  "1.2300",
		"exponent": "1e+30",
	}
	for key, want := range wants {
		if got[key] != want {
			t.Errorf("%s = %#v (%T), want %#v", key, got[key], got[key], want)
		}
	}
}
