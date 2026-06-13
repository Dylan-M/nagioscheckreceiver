// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap/zaptest"
)

// File mode, "default" (tab-delimited [SERVICEPERFDATA]/[HOSTPERFDATA]) format.
func TestIntegration_FileMode_DefaultFormat(t *testing.T) {
	dir := t.TempDir()
	svcFile := filepath.Join(dir, "service-perfdata")
	hostFile := filepath.Join(dir, "host-perfdata")

	cfg := &Config{
		MetricsBuilderConfig: allMetricsEnabled(),
		File:                 &FileConfig{ServicePerfdataFile: svcFile, HostPerfdataFile: hostFile, Format: "default"},
	}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	tailer := newFileTailer(cfg.File, zaptest.NewLogger(t))
	require.NoError(t, os.WriteFile(svcFile, []byte(""), 0644))
	require.NoError(t, os.WriteFile(hostFile, []byte(""), 0644))
	require.NoError(t, tailer.start(context.Background(), nil))
	s.source = tailer
	defer s.shutdown(context.Background())

	appendToFile(t, svcFile, "[SERVICEPERFDATA]\t1520553350\twebserver01\tHTTP Check\tOK\tHTTP OK\ttime=0.001s;;;0;10 size=3302B;;;0\n")
	appendToFile(t, svcFile, "[SERVICEPERFDATA]\t1520553350\tdbserver01\tMySQL\tCRITICAL\tMySQL DOWN\ttime=5.0s;3;5\n")
	appendToFile(t, hostFile, "[HOSTPERFDATA]\t1520553350\twebserver01\tUP\tPING OK\trta=0.456ms;100;500;0;1000 pl=0%;20;60;0;100\n")

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, md.ResourceMetrics().Len())

	rm, found := findResourceByHost(md, "webserver01", "HTTP Check")
	require.True(t, found)
	assert.Equal(t, "file", resourceAttrs(rm)["nagios.source"])
	mn := metricsByName(rm.ScopeMetrics().At(0))
	assert.Equal(t, int64(0), mn["nagios.check.state"].Gauge().DataPoints().At(0).IntValue())
	assert.Equal(t, 2, mn["nagios.perfdata.value"].Gauge().DataPoints().Len())
	sizeDp, ok := findDP(mn["nagios.perfdata.value"], "size")
	require.True(t, ok)
	assert.InDelta(t, 3302.0, sizeDp.DoubleValue(), 0.1)
	u, _ := sizeDp.Attributes().Get("nagios.perfdata.unit")
	assert.Equal(t, "By", u.Str())

	rm, found = findResourceByHost(md, "dbserver01", "MySQL")
	require.True(t, found)
	dp := metricsByName(rm.ScopeMetrics().At(0))["nagios.check.state"].Gauge().DataPoints().At(0)
	assert.Equal(t, int64(2), dp.IntValue())
	sa, _ := dp.Attributes().Get("nagios.state")
	assert.Equal(t, "critical", sa.Str())

	rm, found = findResourceByHost(md, "webserver01", "Host Check")
	require.True(t, found)
	mn = metricsByName(rm.ScopeMetrics().At(0))
	assert.Equal(t, int64(0), mn["nagios.check.state"].Gauge().DataPoints().At(0).IntValue())
	rtaDp, ok := findDP(mn["nagios.perfdata.value"], "rta")
	require.True(t, ok)
	assert.InDelta(t, 0.456, rtaDp.DoubleValue(), 0.001)
}

// Livestatus mode over TCP (network: tcp) rather than a unix socket.
func TestIntegration_LivestatusMode_TCP(t *testing.T) {
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
		fmt.Fprint(conn, "webserver01\tHTTP Check\tcheck_http\t0\ttime=0.001s;;;0;10\tHTTP OK\t1520553350\t0.001\t0.05\n")
	}()

	cfg := &Config{
		MetricsBuilderConfig: allMetricsEnabled(),
		Livestatus:           &LivestatusConfig{Address: listener.Addr().String(), Network: "tcp"},
	}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	s.source = newLivestatusClient(cfg.Livestatus, zaptest.NewLogger(t))

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, md.ResourceMetrics().Len())
	rm, found := findResourceByHost(md, "webserver01", "HTTP Check")
	require.True(t, found)
	assert.Equal(t, "livestatus", resourceAttrs(rm)["nagios.source"])
	assert.Equal(t, int64(0), metricsByName(rm.ScopeMetrics().At(0))["nagios.check.state"].Gauge().DataPoints().At(0).IntValue())
}
