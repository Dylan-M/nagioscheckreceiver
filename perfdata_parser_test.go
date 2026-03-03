// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func float64Ptr(v float64) *float64 {
	return &v
}

func TestParsePerfdata(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []PerfdataMetric
		wantErr  bool
	}{
		{
			name:  "simple single metric",
			input: "time=0.001s;;;0.000000;10.000000",
			expected: []PerfdataMetric{
				{
					Label: "time",
					Value: 0.001,
					Unit:  "s",
					Min:   float64Ptr(0.0),
					Max:   float64Ptr(10.0),
				},
			},
		},
		{
			name:  "multiple metrics",
			input: "time=0.000647s;;;0.000000;10.000000 size=3302B;;;0",
			expected: []PerfdataMetric{
				{
					Label: "time",
					Value: 0.000647,
					Unit:  "s",
					Min:   float64Ptr(0.0),
					Max:   float64Ptr(10.0),
				},
				{
					Label: "size",
					Value: 3302.0,
					Unit:  "By",
					Min:   float64Ptr(0.0),
				},
			},
		},
		{
			name:  "percentage with thresholds",
			input: "/=50%;80;90;0;100",
			expected: []PerfdataMetric{
				{
					Label:    "/",
					Value:    50.0,
					Unit:     "%",
					Warning:  float64Ptr(80.0),
					Critical: float64Ptr(90.0),
					Min:      float64Ptr(0.0),
					Max:      float64Ptr(100.0),
				},
			},
		},
		{
			name:  "quoted label with spaces",
			input: "'C:\\ Used Space'=1234MB;;;0;10240",
			expected: []PerfdataMetric{
				{
					Label: "C:\\ Used Space",
					Value: 1234.0,
					Unit:  "MBy",
					Min:   float64Ptr(0.0),
					Max:   float64Ptr(10240.0),
				},
			},
		},
		{
			name:  "no unit",
			input: "users=5;10;20;0;100",
			expected: []PerfdataMetric{
				{
					Label:    "users",
					Value:    5.0,
					Unit:     "1",
					Warning:  float64Ptr(10.0),
					Critical: float64Ptr(20.0),
					Min:      float64Ptr(0.0),
					Max:      float64Ptr(100.0),
				},
			},
		},
		{
			name:  "counter type",
			input: "requests=12345c",
			expected: []PerfdataMetric{
				{
					Label: "requests",
					Value: 12345.0,
					Unit:  "{count}",
				},
			},
		},
		{
			name:  "milliseconds",
			input: "rta=0.456ms;100;500;0;1000",
			expected: []PerfdataMetric{
				{
					Label:    "rta",
					Value:    0.456,
					Unit:     "ms",
					Warning:  float64Ptr(100.0),
					Critical: float64Ptr(500.0),
					Min:      float64Ptr(0.0),
					Max:      float64Ptr(1000.0),
				},
			},
		},
		{
			name:  "threshold with range notation",
			input: "load=0.5;@0:4;0:8;0;",
			expected: []PerfdataMetric{
				{
					Label:    "load",
					Value:    0.5,
					Unit:     "1",
					Warning:  float64Ptr(4.0),
					Critical: float64Ptr(8.0),
					Min:      float64Ptr(0.0),
				},
			},
		},
		{
			name:  "negative value",
			input: "offset=-0.005s;1;2",
			expected: []PerfdataMetric{
				{
					Label:    "offset",
					Value:    -0.005,
					Unit:     "s",
					Warning:  float64Ptr(1.0),
					Critical: float64Ptr(2.0),
				},
			},
		},
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:    "undetermined value",
			input:   "status=U",
			wantErr: true,
		},
		{
			name:  "missing optional fields",
			input: "time=0.5s",
			expected: []PerfdataMetric{
				{
					Label: "time",
					Value: 0.5,
					Unit:  "s",
				},
			},
		},
		{
			name:  "empty threshold fields",
			input: "time=0.5s;;;0;10",
			expected: []PerfdataMetric{
				{
					Label: "time",
					Value: 0.5,
					Unit:  "s",
					Min:   float64Ptr(0),
					Max:   float64Ptr(10),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics, err := ParsePerfdata(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, metrics)
		})
	}
}

func TestTokenizePerfdata(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "simple",
			input:    "time=0.001s size=3302B",
			expected: []string{"time=0.001s", "size=3302B"},
		},
		{
			name:     "quoted label with spaces",
			input:    "'C:\\ Used Space'=1234MB load=0.5",
			expected: []string{"'C:\\ Used Space'=1234MB", "load=0.5"},
		},
		{
			name:     "multiple spaces",
			input:    "a=1  b=2   c=3",
			expected: []string{"a=1", "b=2", "c=3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := tokenizePerfdata(tt.input)
			assert.Equal(t, tt.expected, tokens)
		})
	}
}

func TestParseValueAndUOM(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantVal     float64
		wantUOM     string
		wantErr     bool
	}{
		{"seconds", "0.001s", 0.001, "s", false},
		{"bytes", "3302B", 3302.0, "B", false},
		{"percentage", "50%", 50.0, "%", false},
		{"no unit", "42", 42.0, "", false},
		{"negative", "-0.005s", -0.005, "s", false},
		{"megabytes", "1234MB", 1234.0, "MB", false},
		{"undetermined", "U", 0, "", true},
		{"empty", "", 0, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, uom, err := parseValueAndUOM(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tt.wantVal, val, 0.0000001)
			assert.Equal(t, tt.wantUOM, uom)
		})
	}
}
