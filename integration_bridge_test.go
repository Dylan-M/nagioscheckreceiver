// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap/zaptest"
)

// wantTS is the timestamp every data point should carry: the check's last_check
// (1520553350 s), not the scrape wall-clock.
var wantTS = pcommon.NewTimestampFromTime(time.Unix(1520553350, 0))

// API mode stamps data points at last_check (last_check is reported in ms by the CGI).
func TestIntegration_TimestampIsLastCheck_API(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(fullAPIResponse))
	}))
	defer server.Close()

	cfg := &Config{MetricsBuilderConfig: allMetricsEnabled(), API: &APIConfig{ClientConfig: confighttp.ClientConfig{Endpoint: server.URL}}}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	s.source = &apiClient{cfg: cfg.API, logger: zaptest.NewLogger(t), client: server.Client()}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	rm, found := findResourceByHost(md, "webserver01", "HTTP Check")
	require.True(t, found)
	mn := metricsByName(rm.ScopeMetrics().At(0))
	assert.Equal(t, wantTS, mn["nagios.check.state"].Gauge().DataPoints().At(0).Timestamp())
	dp, ok := findDP(mn["nagios.perfdata.value"], "time")
	require.True(t, ok)
	assert.Equal(t, wantTS, dp.Timestamp())
}

// Livestatus mode stamps data points at last_check (reported in seconds).
func TestIntegration_TimestampIsLastCheck_Livestatus(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "live")
	listener, err := net.Listen("unix", socketPath)
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
		fmt.Fprint(conn, "webserver01\tHTTP Check\tcheck_http\t0\ttime=0.001s;;;0;10\tHTTP OK\t1520553350\t0.001\t0.05\n")
	}()

	cfg := &Config{MetricsBuilderConfig: allMetricsEnabled(), Livestatus: &LivestatusConfig{Address: socketPath, Network: "unix"}}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	s.source = newLivestatusClient(cfg.Livestatus, zaptest.NewLogger(t))

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	rm, found := findResourceByHost(md, "webserver01", "HTTP Check")
	require.True(t, found)
	mn := metricsByName(rm.ScopeMetrics().At(0))
	assert.Equal(t, wantTS, mn["nagios.check.state"].Gauge().DataPoints().At(0).Timestamp())
	dp, ok := findDP(mn["nagios.perfdata.value"], "time")
	require.True(t, ok)
	assert.Equal(t, wantTS, dp.Timestamp())
}

// File mode stamps data points at the TIMET field from each perfdata line.
func TestIntegration_TimestampIsLastCheck_File(t *testing.T) {
	dir := t.TempDir()
	svcFile := filepath.Join(dir, "service-perfdata")
	cfg := &Config{MetricsBuilderConfig: allMetricsEnabled(), File: &FileConfig{ServicePerfdataFile: svcFile, Format: "pnp4nagios"}}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	tailer := newFileTailer(cfg.File, zaptest.NewLogger(t))
	require.NoError(t, os.WriteFile(svcFile, []byte(""), 0644))
	require.NoError(t, tailer.start(context.Background(), nil))
	s.source = tailer
	defer s.shutdown(context.Background())

	appendToFile(t, svcFile, "DATATYPE::SERVICEPERFDATA\tTIMET::1520553350\tHOSTNAME::webserver01\tSERVICEDESC::HTTP Check\tSERVICESTATE::OK\tSERVICEPERFDATA::time=0.001s;;;0;10\n")

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	rm, found := findResourceByHost(md, "webserver01", "HTTP Check")
	require.True(t, found)
	mn := metricsByName(rm.ScopeMetrics().At(0))
	assert.Equal(t, wantTS, mn["nagios.check.state"].Gauge().DataPoints().At(0).Timestamp())
	dp, ok := findDP(mn["nagios.perfdata.value"], "time")
	require.True(t, ok)
	assert.Equal(t, wantTS, dp.Timestamp())
}

// A second scrape that sees the same last_check must not re-emit (emit-on-change).
func TestIntegration_EmitOnChange_API(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(fullAPIResponse))
	}))
	defer server.Close()

	cfg := &Config{MetricsBuilderConfig: allMetricsEnabled(), API: &APIConfig{ClientConfig: confighttp.ClientConfig{Endpoint: server.URL}}}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	s.source = &apiClient{cfg: cfg.API, logger: zaptest.NewLogger(t), client: server.Client()}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, md.ResourceMetrics().Len(), "first scrape emits all checks")

	md, err = s.scrape(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, md.ResourceMetrics().Len(), "identical last_check on next scrape must not re-emit")
}
