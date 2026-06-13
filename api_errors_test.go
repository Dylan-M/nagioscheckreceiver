// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAPIClient_ParseServiceListResponse_Errors(t *testing.T) {
	c := &apiClient{logger: zap.NewNop()}

	cases := []struct {
		name, body, wantErr string
	}{
		{"malformed json", "not json at all", "parsing JSON response"},
		{"nagios api error", `{"result":{"type_code":1,"type_text":"Error","message":"boom"}}`, "nagios API error"},
		{"no data field", `{"result":{"type_code":0}}`, "no data field"},
		{"bad data payload", `{"result":{"type_code":0},"data":123}`, "parsing servicelist data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.parseServiceListResponse([]byte(tc.body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestAPIClient_UnknownStatusBitmask_MapsToUnknown(t *testing.T) {
	c := &apiClient{logger: zap.NewNop()}
	body := `{"result":{"type_code":0},"data":{"servicelist":{"h":{"s":{"host_name":"h","description":"s","status":999}}}}}`
	results, err := c.parseServiceListResponse([]byte(body))
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, 3, results[0].State) // unknown
}

func TestAPIClient_StatusBitmaskMapping(t *testing.T) {
	// CGI bitmask -> state: 2=OK, 4=WARNING, 16=CRITICAL, 8=UNKNOWN, 1=PENDING->UNKNOWN
	c := &apiClient{logger: zap.NewNop()}
	for bitmask, want := range map[int]int{2: 0, 4: 1, 16: 2, 8: 3, 1: 3} {
		body := `{"result":{"type_code":0},"data":{"servicelist":{"h":{"s":{"host_name":"h","description":"s","status":` +
			strconv.Itoa(bitmask) + `}}}}}`
		results, err := c.parseServiceListResponse([]byte(body))
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equalf(t, want, results[0].State, "bitmask %d", bitmask)
	}
}

func TestTransientError(t *testing.T) {
	base := errors.New("underlying")
	te := &transientError{err: base}
	assert.Equal(t, "underlying", te.Error())
	assert.Equal(t, base, te.Unwrap())
	assert.True(t, isTransientError(te))
	assert.False(t, isTransientError(base))
}
