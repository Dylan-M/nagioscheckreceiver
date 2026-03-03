// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.uber.org/zap/zaptest"
)

func TestFileTailer_DefaultFormat(t *testing.T) {
	dir := t.TempDir()
	perfFile := filepath.Join(dir, "service-perfdata")

	cfg := &FileConfig{
		ServicePerfdataFile: perfFile,
		Format:              "default",
	}

	logger := zaptest.NewLogger(t)
	tailer := newFileTailer(cfg, logger)

	// Create file with content before starting
	content := "[SERVICEPERFDATA]\t1520553350\twebserver01\tHTTP Check\tOK\tHTTP OK\ttime=0.001s;;;0;10\n"
	require.NoError(t, os.WriteFile(perfFile, []byte(content), 0644))

	err := tailer.start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)
	defer tailer.shutdown(context.Background())

	// First collect should return nothing (we seeked to end)
	results, err := tailer.collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, results)

	// Write new data
	f, err := os.OpenFile(perfFile, os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	_, err = f.WriteString("[SERVICEPERFDATA]\t1520553400\tdb01\tMySQL\tOK\tMySQL OK\tuptime=12345s\n")
	require.NoError(t, err)
	f.Close()

	// Second collect should return the new line
	results, err = tailer.collect(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "db01", results[0].HostName)
	assert.Equal(t, "MySQL", results[0].ServiceDescription)
	assert.Equal(t, 0, results[0].State)
	assert.Equal(t, "uptime=12345s", results[0].PerfData)
}

func TestFileTailer_PNP4NagiosFormat(t *testing.T) {
	dir := t.TempDir()
	perfFile := filepath.Join(dir, "service-perfdata")

	cfg := &FileConfig{
		ServicePerfdataFile: perfFile,
		Format:              "pnp4nagios",
	}

	logger := zaptest.NewLogger(t)
	tailer := newFileTailer(cfg, logger)

	// Start with no file
	err := tailer.start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)
	defer tailer.shutdown(context.Background())

	// Create file with PNP4Nagios format data
	line := "HOSTNAME::webserver01\tSERVICEDESC::HTTP Check\tSERVICESTATE::OK\tSERVICEOUTPUT::HTTP OK\tSERVICEPERFDATA::time=0.001s;;;0;10\tSERVICECHECKCOMMAND::check_http!-p 80\n"
	require.NoError(t, os.WriteFile(perfFile, []byte(line), 0644))

	// Collect should detect new file and read it
	results, err := tailer.collect(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "webserver01", results[0].HostName)
	assert.Equal(t, "HTTP Check", results[0].ServiceDescription)
	assert.Equal(t, "check_http", results[0].CheckCommand)
	assert.Equal(t, 0, results[0].State)
}

func TestFileTailer_HostPerfdataDefault(t *testing.T) {
	dir := t.TempDir()
	svcFile := filepath.Join(dir, "service-perfdata")
	hostFile := filepath.Join(dir, "host-perfdata")

	cfg := &FileConfig{
		ServicePerfdataFile: svcFile,
		HostPerfdataFile:    hostFile,
		Format:              "default",
	}

	logger := zaptest.NewLogger(t)
	tailer := newFileTailer(cfg, logger)

	// Start with no files
	err := tailer.start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)
	defer tailer.shutdown(context.Background())

	// Create service and host files
	svcLine := "[SERVICEPERFDATA]\t1\thost1\tsvc1\tOK\tout1\tperf1=1\n"
	hostLine := "[HOSTPERFDATA]\t1\thost1\tUP\tPING OK\trta=0.5ms;100;500\n"
	require.NoError(t, os.WriteFile(svcFile, []byte(svcLine), 0644))
	require.NoError(t, os.WriteFile(hostFile, []byte(hostLine), 0644))

	results, err := tailer.collect(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 2)

	// Find service result
	var svcResult, hostResult NagiosCheckResult
	for _, r := range results {
		if r.ServiceDescription == "svc1" {
			svcResult = r
		}
		if r.ServiceDescription == "Host Check" {
			hostResult = r
		}
	}

	assert.Equal(t, "host1", svcResult.HostName)
	assert.Equal(t, "perf1=1", svcResult.PerfData)

	assert.Equal(t, "host1", hostResult.HostName)
	assert.Equal(t, "Host Check", hostResult.ServiceDescription)
	assert.Equal(t, 0, hostResult.State) // UP -> 0
	assert.Equal(t, "rta=0.5ms;100;500", hostResult.PerfData)
}

func TestFileTailer_HostPerfdataPNP4Nagios(t *testing.T) {
	dir := t.TempDir()
	svcFile := filepath.Join(dir, "service-perfdata")
	hostFile := filepath.Join(dir, "host-perfdata")

	cfg := &FileConfig{
		ServicePerfdataFile: svcFile,
		HostPerfdataFile:    hostFile,
		Format:              "pnp4nagios",
	}

	logger := zaptest.NewLogger(t)
	tailer := newFileTailer(cfg, logger)

	err := tailer.start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)
	defer tailer.shutdown(context.Background())

	hostLine := "HOSTNAME::router01\tHOSTSTATE::UP\tHOSTOUTPUT::PING OK\tHOSTPERFDATA::rta=0.5ms;100;500\tHOSTCHECKCOMMAND::check_ping!100,20%!500,60%\n"
	require.NoError(t, os.WriteFile(hostFile, []byte(hostLine), 0644))
	// Create empty service file so tailer doesn't error
	require.NoError(t, os.WriteFile(svcFile, []byte(""), 0644))

	results, err := tailer.collect(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "router01", results[0].HostName)
	assert.Equal(t, "Host Check", results[0].ServiceDescription)
	assert.Equal(t, 0, results[0].State)
	assert.Equal(t, "check_ping", results[0].CheckCommand)
	assert.Equal(t, "rta=0.5ms;100;500", results[0].PerfData)
}

func TestFileTailer_Rotation(t *testing.T) {
	dir := t.TempDir()
	perfFile := filepath.Join(dir, "service-perfdata")

	cfg := &FileConfig{
		ServicePerfdataFile: perfFile,
		Format:              "default",
	}

	logger := zaptest.NewLogger(t)
	tailer := newFileTailer(cfg, logger)

	// Create initial file
	require.NoError(t, os.WriteFile(perfFile, []byte(""), 0644))

	err := tailer.start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)
	defer tailer.shutdown(context.Background())

	// Write data to original file
	f, err := os.OpenFile(perfFile, os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	_, err = f.WriteString("[SERVICEPERFDATA]\t1\thost1\tsvc1\tOK\tout1\tperf1=1\n")
	require.NoError(t, err)
	f.Close()

	// Simulate rotation: rename old file and create new one
	os.Rename(perfFile, perfFile+".old")
	require.NoError(t, os.WriteFile(perfFile, []byte("[SERVICEPERFDATA]\t2\thost2\tsvc2\tWARNING\tout2\tperf2=2\n"), 0644))

	results, err := tailer.collect(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(results), 1)

	foundHost1 := false
	foundHost2 := false
	for _, r := range results {
		if r.HostName == "host1" {
			foundHost1 = true
		}
		if r.HostName == "host2" {
			foundHost2 = true
		}
	}
	assert.True(t, foundHost1 || foundHost2, "should have read data from at least one file")
}

func TestFileTailer_FileNotExist(t *testing.T) {
	cfg := &FileConfig{
		ServicePerfdataFile: "/nonexistent/path/perfdata",
		Format:              "default",
	}

	logger := zaptest.NewLogger(t)
	tailer := newFileTailer(cfg, logger)

	err := tailer.start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)

	results, err := tailer.collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestParseDefaultLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    NagiosCheckResult
		wantErr bool
	}{
		{
			name: "valid line",
			line: "[SERVICEPERFDATA]\t1520553350\twebserver01\tHTTP Check\tOK\tHTTP OK\ttime=0.001s",
			want: NagiosCheckResult{
				HostName:           "webserver01",
				ServiceDescription: "HTTP Check",
				State:              0,
				PluginOutput:       "HTTP OK",
				PerfData:           "time=0.001s",
			},
		},
		{
			name:    "wrong prefix",
			line:    "[HOSTPERFDATA]\t1\thost\tUP\tout\tperf",
			wantErr: true,
		},
		{
			name:    "too few fields",
			line:    "[SERVICEPERFDATA]\t1\thost",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDefaultLine(tt.line)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseDefaultHostLine(t *testing.T) {
	line := "[HOSTPERFDATA]\t1520553350\trouter01\tUP\tPING OK - Packet loss = 0%\trta=0.456ms;100;500;0;1000"
	result, err := parseDefaultHostLine(line)
	require.NoError(t, err)
	assert.Equal(t, "router01", result.HostName)
	assert.Equal(t, "Host Check", result.ServiceDescription)
	assert.Equal(t, 0, result.State)
	assert.Equal(t, "rta=0.456ms;100;500;0;1000", result.PerfData)
}

func TestParsePNP4NagiosLine(t *testing.T) {
	line := "HOSTNAME::db01\tSERVICEDESC::MySQL\tSERVICESTATE::CRITICAL\tSERVICEOUTPUT::MySQL DOWN\tSERVICEPERFDATA::time=5s;3;5\tSERVICECHECKCOMMAND::check_mysql!-H db01"

	result, err := parsePNP4NagiosLine(line)
	require.NoError(t, err)
	assert.Equal(t, "db01", result.HostName)
	assert.Equal(t, "MySQL", result.ServiceDescription)
	assert.Equal(t, 2, result.State)
	assert.Equal(t, "check_mysql", result.CheckCommand)
	assert.Equal(t, "time=5s;3;5", result.PerfData)
}

func TestParsePNP4NagiosHostLine(t *testing.T) {
	line := "HOSTNAME::router01\tHOSTSTATE::DOWN\tHOSTOUTPUT::PING CRITICAL\tHOSTPERFDATA::rta=500ms;100;500\tHOSTCHECKCOMMAND::check_ping"

	result, err := parsePNP4NagiosHostLine(line)
	require.NoError(t, err)
	assert.Equal(t, "router01", result.HostName)
	assert.Equal(t, "Host Check", result.ServiceDescription)
	assert.Equal(t, 2, result.State) // DOWN -> CRITICAL
	assert.Equal(t, "check_ping", result.CheckCommand)
	assert.Equal(t, "rta=500ms;100;500", result.PerfData)
}

func TestParseNagiosState(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"OK", 0},
		{"UP", 0},
		{"0", 0},
		{"WARNING", 1},
		{"1", 1},
		{"CRITICAL", 2},
		{"DOWN", 2},
		{"2", 2},
		{"UNKNOWN", 3},
		{"UNREACHABLE", 3},
		{"3", 3},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, parseNagiosState(tt.input))
		})
	}
}
