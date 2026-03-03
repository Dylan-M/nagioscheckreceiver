// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.uber.org/zap/zaptest"
)

const sampleServiceListResponse = `{
  "format_version": 0,
  "result": {
    "type_code": 0,
    "type_text": "Success",
    "message": ""
  },
  "data": {
    "servicelist": {
      "webserver01": {
        "HTTP Check": {
          "host_name": "webserver01",
          "description": "HTTP Check",
          "plugin_output": "HTTP OK: HTTP/1.1 200 OK - 3302 bytes in 0.001 second response time",
          "perf_data": "time=0.000647s;;;0.000000;10.000000 size=3302B;;;0",
          "status": 2,
          "last_check": 1520553350000,
          "execution_time": 0.001,
          "latency": 0.05
        },
        "Disk Usage": {
          "host_name": "webserver01",
          "description": "Disk Usage",
          "plugin_output": "DISK WARNING - free space: / 1234 MB (15% inode=90%)",
          "perf_data": "/=6789MB;7000;7500;0;8000",
          "status": 4,
          "last_check": 1520553300000,
          "execution_time": 0.01,
          "latency": 0.02
        }
      },
      "dbserver01": {
        "MySQL": {
          "host_name": "dbserver01",
          "description": "MySQL",
          "plugin_output": "MySQL OK - 0.001 sec. response time",
          "perf_data": "time=0.001234s;3;5",
          "status": 2,
          "last_check": 1520553400000,
          "execution_time": 0.5,
          "latency": 0.01
        }
      }
    }
  }
}`

func TestAPIClient_Collect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "query=servicelist&details=true", r.URL.RawQuery)

		username, password, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "nagiosadmin", username)
		assert.Equal(t, "secret", password)

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleServiceListResponse))
	}))
	defer server.Close()

	cfg := &APIConfig{
		ClientConfig: confighttp.ClientConfig{
			Endpoint: server.URL,
		},
		Username: "nagiosadmin",
		Password: "secret",
	}

	logger := zaptest.NewLogger(t)
	client := newAPIClient(cfg, logger)
	// Skip start() and set client directly for testing
	client.client = server.Client()

	results, err := client.collect(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 3)

	// Find HTTP Check result
	var httpResult NagiosCheckResult
	for _, r := range results {
		if r.ServiceDescription == "HTTP Check" {
			httpResult = r
			break
		}
	}

	assert.Equal(t, "webserver01", httpResult.HostName)
	assert.Equal(t, 0, httpResult.State) // status 2 (OK bitmask) -> state 0
	assert.Equal(t, "time=0.000647s;;;0.000000;10.000000 size=3302B;;;0", httpResult.PerfData)
	assert.InDelta(t, 0.001, httpResult.ExecutionTime, 0.0001)
	assert.Equal(t, int64(1520553350), httpResult.LastCheck) // ms -> s

	// Find Disk Usage (WARNING, bitmask 4 -> state 1)
	var diskResult NagiosCheckResult
	for _, r := range results {
		if r.ServiceDescription == "Disk Usage" {
			diskResult = r
			break
		}
	}
	assert.Equal(t, 1, diskResult.State)
}

func TestAPIClient_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"format_version": 0,
			"result": {
				"type_code": 4,
				"type_text": "Option Missing",
				"message": "Option 'query' is missing"
			},
			"data": {}
		}`))
	}))
	defer server.Close()

	cfg := &APIConfig{
		ClientConfig: confighttp.ClientConfig{Endpoint: server.URL},
	}

	logger := zaptest.NewLogger(t)
	client := newAPIClient(cfg, logger)
	client.client = server.Client()

	_, err := client.collect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Option Missing")
}

func TestAPIClient_AuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Nagios Access"`)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("<html>Unauthorized</html>"))
	}))
	defer server.Close()

	cfg := &APIConfig{
		ClientConfig: confighttp.ClientConfig{Endpoint: server.URL},
		Username:     "bad",
		Password:     "creds",
	}

	logger := zaptest.NewLogger(t)
	client := newAPIClient(cfg, logger)
	client.client = server.Client()

	_, err := client.collect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestAPIStatusToState(t *testing.T) {
	tests := []struct {
		bitmask    int
		wantState  int
	}{
		{1, 3},  // Pending -> UNKNOWN
		{2, 0},  // OK
		{4, 1},  // WARNING
		{8, 3},  // UNKNOWN
		{16, 2}, // CRITICAL
	}

	for _, tt := range tests {
		state, ok := apiStatusToState[tt.bitmask]
		assert.True(t, ok)
		assert.Equal(t, tt.wantState, state)
	}
}
