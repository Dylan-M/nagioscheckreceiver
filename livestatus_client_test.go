// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestLivestatusClient_Collect(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "live")

	// Start a mock Unix socket server
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read the query (we don't validate it in this test)
		buf := make([]byte, 4096)
		conn.Read(buf)

		// Send mock response: tab-separated fields matching our query columns
		// host_name, description, check_command, state, perf_data, plugin_output, last_check, execution_time, latency
		response := "webserver01\tHTTP Check\tcheck_http!-p 80\t0\ttime=0.001s;;;0;10 size=3302B;;;0\tHTTP OK\t1520553350\t0.001\t0.05\n"
		response += "dbserver01\tMySQL\tcheck_mysql\t2\ttime=5.0s;3;5\tMySQL CRITICAL\t1520553400\t5.0\t0.01\n"
		fmt.Fprint(conn, response)
	}()

	cfg := &LivestatusConfig{
		Address: socketPath,
		Network: "unix",
	}

	logger := zaptest.NewLogger(t)
	client := newLivestatusClient(cfg, logger)

	err = client.start(context.Background(), nil)
	require.NoError(t, err)

	results, err := client.collect(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 2)

	// Verify first result
	assert.Equal(t, "webserver01", results[0].HostName)
	assert.Equal(t, "HTTP Check", results[0].ServiceDescription)
	assert.Equal(t, "check_http", results[0].CheckCommand) // Arguments stripped
	assert.Equal(t, 0, results[0].State)
	assert.Equal(t, "time=0.001s;;;0;10 size=3302B;;;0", results[0].PerfData)
	assert.InDelta(t, 0.001, results[0].ExecutionTime, 0.0001)

	// Verify second result (CRITICAL)
	assert.Equal(t, "dbserver01", results[1].HostName)
	assert.Equal(t, "check_mysql", results[1].CheckCommand)
	assert.Equal(t, 2, results[1].State)
}

func TestLivestatusClient_ConnectionRefused(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "nonexistent")

	cfg := &LivestatusConfig{
		Address: socketPath,
		Network: "unix",
	}

	logger := zaptest.NewLogger(t)
	client := newLivestatusClient(cfg, logger)

	_, err := client.collect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connecting to livestatus")
}

func TestParseLivestatusLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    NagiosCheckResult
		wantErr bool
	}{
		{
			name: "valid OK line",
			line: "web01\tHTTP\tcheck_http!-p 443\t0\ttime=0.1s\tHTTP OK\t1520553350\t0.1\t0.05",
			want: NagiosCheckResult{
				HostName:           "web01",
				ServiceDescription: "HTTP",
				CheckCommand:       "check_http",
				State:              0,
				PerfData:           "time=0.1s",
				PluginOutput:       "HTTP OK",
				LastCheck:          1520553350,
				ExecutionTime:      0.1,
				Latency:            0.05,
			},
		},
		{
			name: "CRITICAL state",
			line: "db01\tMySQL\tcheck_mysql\t2\ttime=5s\tMySQL DOWN\t1520553400\t5.0\t0.01",
			want: NagiosCheckResult{
				HostName:           "db01",
				ServiceDescription: "MySQL",
				CheckCommand:       "check_mysql",
				State:              2,
				PerfData:           "time=5s",
				PluginOutput:       "MySQL DOWN",
				LastCheck:          1520553400,
				ExecutionTime:      5.0,
				Latency:            0.01,
			},
		},
		{
			name:    "too few fields",
			line:    "host\tsvc\tcmd",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLivestatusLine(tt.line)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLivestatusClient_TCP(t *testing.T) {
	// Start a mock TCP server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 4096)
		conn.Read(buf)

		response := "host01\tPing\tcheck_ping\t0\trta=0.5ms;100;500\tPING OK\t1520553350\t0.001\t0.01\n"
		fmt.Fprint(conn, response)
	}()

	cfg := &LivestatusConfig{
		Address: listener.Addr().String(),
		Network: "tcp",
	}

	logger := zaptest.NewLogger(t)
	client := newLivestatusClient(cfg, logger)

	results, err := client.collect(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "host01", results[0].HostName)
	assert.Equal(t, "check_ping", results[0].CheckCommand)
}
