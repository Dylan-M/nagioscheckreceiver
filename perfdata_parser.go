// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// PerfdataMetric represents a single parsed Nagios performance data metric.
type PerfdataMetric struct {
	Label    string
	Value    float64
	Unit     string   // OTel-mapped unit
	Warning  *float64 // nil if not present
	Critical *float64 // nil if not present
	Min      *float64 // nil if not present
	Max      *float64 // nil if not present
}

// nagiosUOMToOTel maps Nagios units of measurement to OTel conventions.
var nagiosUOMToOTel = map[string]string{
	"s":  "s",
	"ms": "ms",
	"%":  "%",
	"B":  "By",
	"KB": "KBy",
	"MB": "MBy",
	"GB": "GBy",
	"TB": "TBy",
	"c":  "{count}",
	"":   "1",
}

// ParsePerfdata parses a Nagios performance data string into structured metrics.
// Format: 'label'=value[UOM];[warn];[crit];[min];[max]
// Multiple metrics are space-separated.
func ParsePerfdata(perfdata string) ([]PerfdataMetric, error) {
	perfdata = strings.TrimSpace(perfdata)
	if perfdata == "" {
		return nil, nil
	}

	var metrics []PerfdataMetric
	var parseErrors []string

	tokens := tokenizePerfdata(perfdata)
	for _, token := range tokens {
		m, err := parsePerfdataToken(token)
		if err != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("token %q: %v", token, err))
			continue
		}
		metrics = append(metrics, m)
	}

	if len(parseErrors) > 0 && len(metrics) == 0 {
		return nil, fmt.Errorf("all perfdata tokens failed to parse: %s", strings.Join(parseErrors, "; "))
	}

	return metrics, nil
}

// tokenizePerfdata splits a perfdata string into individual metric tokens,
// handling quoted labels with spaces.
func tokenizePerfdata(perfdata string) []string {
	var tokens []string
	var current strings.Builder
	inQuote := false

	for i := 0; i < len(perfdata); i++ {
		ch := perfdata[i]
		switch {
		case ch == '\'':
			inQuote = !inQuote
			current.WriteByte(ch)
		case ch == ' ' && !inQuote:
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	return tokens
}

// parsePerfdataToken parses a single perfdata token like "time=0.001s;;;0;10"
func parsePerfdataToken(token string) (PerfdataMetric, error) {
	eqIdx := strings.Index(token, "=")
	if eqIdx < 0 {
		return PerfdataMetric{}, fmt.Errorf("no '=' found")
	}

	label := token[:eqIdx]
	// Strip surrounding single quotes from label
	label = strings.Trim(label, "'")
	if label == "" {
		return PerfdataMetric{}, fmt.Errorf("empty label")
	}

	rest := token[eqIdx+1:]
	parts := strings.Split(rest, ";")

	// Parse value and UOM from first part
	valuePart := parts[0]
	value, uom, err := parseValueAndUOM(valuePart)
	if err != nil {
		return PerfdataMetric{}, fmt.Errorf("parsing value: %w", err)
	}

	otelUnit, ok := nagiosUOMToOTel[uom]
	if !ok {
		otelUnit = "1"
	}

	m := PerfdataMetric{
		Label: label,
		Value: value,
		Unit:  otelUnit,
	}

	// Parse optional fields: warn, crit, min, max
	if len(parts) > 1 {
		m.Warning = parseThreshold(parts[1])
	}
	if len(parts) > 2 {
		m.Critical = parseThreshold(parts[2])
	}
	if len(parts) > 3 {
		m.Min = parseOptionalFloat(parts[3])
	}
	if len(parts) > 4 {
		m.Max = parseOptionalFloat(parts[4])
	}

	return m, nil
}

// parseValueAndUOM splits "0.001s" into (0.001, "s")
func parseValueAndUOM(s string) (float64, string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "U" {
		// "U" means the plugin could not determine the value
		return 0, "", fmt.Errorf("undetermined value")
	}

	// Find where the numeric part ends and UOM begins
	numEnd := 0
	for numEnd < len(s) {
		ch := s[numEnd]
		if ch == '-' || ch == '+' || ch == '.' || (ch >= '0' && ch <= '9') || ch == 'e' || ch == 'E' {
			numEnd++
		} else {
			break
		}
	}

	if numEnd == 0 {
		return 0, "", fmt.Errorf("no numeric value in %q", s)
	}

	val, err := strconv.ParseFloat(s[:numEnd], 64)
	if err != nil {
		return 0, "", fmt.Errorf("parsing float %q: %w", s[:numEnd], err)
	}

	uom := strings.TrimSpace(s[numEnd:])
	// Normalize UOM - strip any trailing whitespace/control chars
	uom = strings.TrimFunc(uom, unicode.IsSpace)

	return val, uom, nil
}

// parseThreshold extracts a simple numeric threshold value.
// Nagios thresholds can be complex ranges like "@10:20" or "~:10",
// but for metric emission we extract the upper bound as a simple float.
func parseThreshold(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	// Strip range prefix characters
	s = strings.TrimPrefix(s, "@")
	s = strings.TrimPrefix(s, "~")

	// If it's a range "start:end", take the end value
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		s = s[idx+1:]
	}

	if s == "" {
		return nil
	}

	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &val
}

// parseOptionalFloat parses an optional float field (min/max).
func parseOptionalFloat(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &val
}
