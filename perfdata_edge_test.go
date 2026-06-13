// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseValueAndUOM_Cases(t *testing.T) {
	_, _, err := parseValueAndUOM("U")
	require.Error(t, err, "U means undetermined")
	_, _, err = parseValueAndUOM("")
	require.Error(t, err)
	_, _, err = parseValueAndUOM("abc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no numeric")
	_, _, err = parseValueAndUOM(".")
	require.Error(t, err, "lone dot is not a valid float")

	v, u, err := parseValueAndUOM("0.001s")
	require.NoError(t, err)
	assert.InDelta(t, 0.001, v, 1e-9)
	assert.Equal(t, "s", u)
}

func TestParsePerfdataToken_Errors(t *testing.T) {
	_, err := parsePerfdataToken("noequalshere")
	require.Error(t, err)
	_, err = parsePerfdataToken("''=5")
	require.Error(t, err, "empty label")
	_, err = parsePerfdataToken("x=U")
	require.Error(t, err, "undetermined value")
}

func TestParseThreshold_Ranges(t *testing.T) {
	assert.Nil(t, parseThreshold(""))
	assert.Nil(t, parseThreshold("notanumber"))
	assert.Nil(t, parseThreshold("10:"), "empty end of range")

	require.NotNil(t, parseThreshold("@10:20"))
	assert.Equal(t, 20.0, *parseThreshold("@10:20"))
	require.NotNil(t, parseThreshold("~:50"))
	assert.Equal(t, 50.0, *parseThreshold("~:50"))
	require.NotNil(t, parseThreshold("80"))
	assert.Equal(t, 80.0, *parseThreshold("80"))
}

func TestParseOptionalFloat_Cases(t *testing.T) {
	assert.Nil(t, parseOptionalFloat(""))
	assert.Nil(t, parseOptionalFloat("notnum"))
	require.NotNil(t, parseOptionalFloat("5"))
	assert.Equal(t, 5.0, *parseOptionalFloat("5"))
}

func TestParsePerfdataToken_UnknownUOMDefaultsToOne(t *testing.T) {
	m, err := parsePerfdataToken("widgets=42zorp")
	require.NoError(t, err)
	assert.Equal(t, "1", m.Unit, "unknown UOM falls back to dimensionless")
	assert.Equal(t, 42.0, m.Value)
}

func TestParsePerfdataToken_AllFields(t *testing.T) {
	m, err := parsePerfdataToken("/=4096MB;7000;7500;0;8000")
	require.NoError(t, err)
	assert.Equal(t, "/", m.Label)
	assert.Equal(t, "MBy", m.Unit)
	assert.Equal(t, 4096.0, m.Value)
	require.NotNil(t, m.Warning)
	assert.Equal(t, 7000.0, *m.Warning)
	require.NotNil(t, m.Critical)
	assert.Equal(t, 7500.0, *m.Critical)
	require.NotNil(t, m.Min)
	assert.Equal(t, 0.0, *m.Min)
	require.NotNil(t, m.Max)
	assert.Equal(t, 8000.0, *m.Max)
}
