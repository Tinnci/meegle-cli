// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

// Package jsonvalue decodes JSON values without converting numbers stored in
// interface slots to float64.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Unmarshal decodes exactly one JSON value into target. Numbers that land in
// interface{} slots are retained as json.Number, while explicitly typed fields
// keep the normal encoding/json behavior.
func Unmarshal(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		if err == io.EOF {
			return fmt.Errorf("unexpected end of JSON input")
		}
		return err
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid JSON: multiple top-level values")
		}
		return err
	}
	return nil
}

// ParseNumber validates one JSON number and retains its original lexical form.
func ParseNumber(raw string) (json.Number, error) {
	var value any
	if err := Unmarshal([]byte(raw), &value); err != nil {
		return "", err
	}
	number, ok := value.(json.Number)
	if !ok {
		return "", fmt.Errorf("expected JSON number")
	}
	return number, nil
}

// IsInteger reports whether number is a valid JSON number whose mathematical
// value is integral. It examines the decimal lexeme directly, so an exponent
// such as 1e1000001 does not allocate an integer with a million digits.
func IsInteger(number json.Number) bool {
	raw := number.String()
	if _, err := ParseNumber(raw); err != nil {
		return false
	}
	unsigned := strings.TrimPrefix(raw, "-")
	mantissa, exponentText, hasExponent := strings.Cut(unsigned, "e")
	if !hasExponent {
		mantissa, exponentText, hasExponent = strings.Cut(unsigned, "E")
	}

	integerPart, fraction, hasFraction := strings.Cut(mantissa, ".")
	coefficient := integerPart
	if hasFraction {
		coefficient += fraction
	}
	if strings.Trim(coefficient, "0") == "" {
		return true
	}

	trailingZeros := len(coefficient) - len(strings.TrimRight(coefficient, "0"))
	fractionDigits := len(fraction)
	if !hasExponent || exponentText == "" {
		return trailingZeros >= fractionDigits
	}

	negativeExponent := strings.HasPrefix(exponentText, "-")
	exponentDigits := strings.TrimPrefix(strings.TrimPrefix(exponentText, "+"), "-")
	if negativeExponent {
		availableZeros := trailingZeros - fractionDigits
		if availableZeros < 0 {
			return false
		}
		_, fits := parseDecimalAtMost(exponentDigits, availableZeros)
		return fits
	}

	exponent, fits := parseDecimalAtMost(exponentDigits, fractionDigits)
	if !fits {
		return true
	}
	return trailingZeros >= fractionDigits-exponent
}

// Int64 converts an integral JSON number to int64 without first converting it
// to a floating-point value or expanding an unbounded exponent.
func Int64(number json.Number) (int64, error) {
	if !IsInteger(number) {
		return 0, fmt.Errorf("JSON number %q is not an integer", number)
	}
	raw := number.String()
	negative := strings.HasPrefix(raw, "-")
	unsigned := strings.TrimPrefix(raw, "-")
	mantissa, exponentText, hasExponent := strings.Cut(unsigned, "e")
	if !hasExponent {
		mantissa, exponentText, hasExponent = strings.Cut(unsigned, "E")
	}
	integerPart, fraction, hasFraction := strings.Cut(mantissa, ".")
	coefficient := integerPart
	if hasFraction {
		coefficient += fraction
	}
	coefficient = strings.TrimLeft(coefficient, "0")
	if coefficient == "" {
		return 0, nil
	}

	exponent := 0
	if hasExponent {
		exponentNegative := strings.HasPrefix(exponentText, "-")
		exponentDigits := strings.TrimPrefix(strings.TrimPrefix(exponentText, "+"), "-")
		limit := len(fraction) + 20
		if exponentNegative {
			limit = len(coefficient)
		}
		parsed, fits := parseDecimalAtMost(exponentDigits, limit)
		if !fits {
			return 0, fmt.Errorf("JSON number %q is outside int64 range", number)
		}
		if exponentNegative {
			exponent = -parsed
		} else {
			exponent = parsed
		}
	}

	scale := exponent - len(fraction)
	if scale < 0 {
		keep := len(coefficient) + scale
		if keep <= 0 {
			return 0, fmt.Errorf("JSON number %q is outside int64 range", number)
		}
		coefficient = coefficient[:keep]
	} else {
		if len(coefficient)+scale > 19 {
			return 0, fmt.Errorf("JSON number %q is outside int64 range", number)
		}
		coefficient += strings.Repeat("0", scale)
	}
	if negative {
		coefficient = "-" + coefficient
	}
	parsed, err := strconv.ParseInt(coefficient, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("JSON number %q is outside int64 range", number)
	}
	return parsed, nil
}

func parseDecimalAtMost(raw string, limit int) (int, bool) {
	normalized := strings.TrimLeft(raw, "0")
	if normalized == "" {
		return 0, true
	}
	limitText := strconv.Itoa(limit)
	if len(normalized) > len(limitText) || (len(normalized) == len(limitText) && normalized > limitText) {
		return 0, false
	}
	value, err := strconv.Atoi(normalized)
	return value, err == nil
}
