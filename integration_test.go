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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap/zaptest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/nagioscheckreceiver/internal/metadata"
)

// --- Helpers ---

func allMetricsEnabled() metadata.MetricsBuilderConfig {
	mbc := metadata.DefaultMetricsBuilderConfig()
	mbc.Metrics.NagiosCheckLatency.Enabled = true
	mbc.Metrics.NagiosCheckLastCheck.Enabled = true
	mbc.Metrics.NagiosPerfdataWarning.Enabled = true
	mbc.Metrics.NagiosPerfdataCritical.Enabled = true
	mbc.Metrics.NagiosPerfdataMin.Enabled = true
	mbc.Metrics.NagiosPerfdataMax.Enabled = true
	return mbc
}

func metricsByName(sm pmetric.ScopeMetrics) map[string]pmetric.Metric {
	result := make(map[string]pmetric.Metric)
	for i := 0; i < sm.Metrics().Len(); i++ {
		m := sm.Metrics().At(i)
		result[m.Name()] = m
	}
	return result
}

func resourceAttrs(rm pmetric.ResourceMetrics) map[string]string {
	result := make(map[string]string)
	rm.Resource().Attributes().Range(func(k string, v pcommon.Value) bool {
		result[k] = v.Str()
		return true
	})
	return result
}

func findResourceByHost(md pmetric.Metrics, host, service string) (pmetric.ResourceMetrics, bool) {
	for i := 0; i < md.ResourceMetrics().Len(); i++ {
		rm := md.ResourceMetrics().At(i)
		h, hOk := rm.Resource().Attributes().Get("nagios.host.name")
		s, sOk := rm.Resource().Attributes().Get("nagios.service.description")
		if hOk && sOk && h.Str() == host && s.Str() == service {
			return rm, true
		}
	}
	return pmetric.ResourceMetrics{}, false
}

// findDP finds a data point within a gauge metric that has the given nagios.perfdata.label.
func findDP(m pmetric.Metric, label string) (pmetric.NumberDataPoint, bool) {
	for i := 0; i < m.Gauge().DataPoints().Len(); i++ {
		dp := m.Gauge().DataPoints().At(i)
		l, ok := dp.Attributes().Get("nagios.perfdata.label")
		if ok && l.Str() == label {
			return dp, true
		}
	}
	return pmetric.NumberDataPoint{}, false
}

func appendToFile(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(data)
	require.NoError(t, err)
	f.Close()
}

// --- Integration: API Mode ---

func TestIntegration_APIMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fullAPIResponse))
	}))
	defer server.Close()

	cfg := &Config{
		MetricsBuilderConfig: allMetricsEnabled(),
		API: &APIConfig{
			ClientConfig: confighttp.ClientConfig{Endpoint: server.URL},
			Username:     "nagiosadmin",
			Password:     "secret",
		},
	}

	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	s.source = &apiClient{
		cfg:    cfg.API,
		logger: zaptest.NewLogger(t),
		client: server.Client(),
	}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, md.ResourceMetrics().Len())

	// --- webserver01 / HTTP Check ---
	rm, found := findResourceByHost(md, "webserver01", "HTTP Check")
	require.True(t, found)

	attrs := resourceAttrs(rm)
	assert.Equal(t, "api", attrs["nagios.source"])
	assert.Empty(t, attrs["nagios.check.command"])

	mn := metricsByName(rm.ScopeMetrics().At(0))

	// check.state = 0 (OK)
	require.Contains(t, mn, "nagios.check.state")
	dp := mn["nagios.check.state"].Gauge().DataPoints().At(0)
	assert.Equal(t, int64(0), dp.IntValue())
	sa, _ := dp.Attributes().Get("nagios.state")
	assert.Equal(t, "ok", sa.Str())

	// execution_time
	require.Contains(t, mn, "nagios.check.execution_time")
	assert.InDelta(t, 0.001, mn["nagios.check.execution_time"].Gauge().DataPoints().At(0).DoubleValue(), 0.0001)

	// latency
	require.Contains(t, mn, "nagios.check.latency")
	assert.InDelta(t, 0.05, mn["nagios.check.latency"].Gauge().DataPoints().At(0).DoubleValue(), 0.001)

	// last_check (ms -> s conversion)
	require.Contains(t, mn, "nagios.check.last_check")
	assert.Equal(t, int64(1520553350), mn["nagios.check.last_check"].Gauge().DataPoints().At(0).IntValue())

	// perfdata: 2 data points on one metric
	require.Contains(t, mn, "nagios.perfdata.value")
	assert.Equal(t, 2, mn["nagios.perfdata.value"].Gauge().DataPoints().Len())

	timeDp, found := findDP(mn["nagios.perfdata.value"], "time")
	require.True(t, found)
	assert.InDelta(t, 0.000647, timeDp.DoubleValue(), 0.000001)
	u, _ := timeDp.Attributes().Get("nagios.perfdata.unit")
	assert.Equal(t, "s", u.Str())

	sizeDp, found := findDP(mn["nagios.perfdata.value"], "size")
	require.True(t, found)
	assert.InDelta(t, 3302.0, sizeDp.DoubleValue(), 0.1)
	u, _ = sizeDp.Attributes().Get("nagios.perfdata.unit")
	assert.Equal(t, "By", u.Str())

	// perfdata min/max for "time"
	require.Contains(t, mn, "nagios.perfdata.min")
	timeMin, found := findDP(mn["nagios.perfdata.min"], "time")
	require.True(t, found)
	assert.InDelta(t, 0.0, timeMin.DoubleValue(), 0.0001)

	require.Contains(t, mn, "nagios.perfdata.max")
	timeMax, found := findDP(mn["nagios.perfdata.max"], "time")
	require.True(t, found)
	assert.InDelta(t, 10.0, timeMax.DoubleValue(), 0.0001)

	// --- webserver01 / Disk Usage (WARNING) ---
	rm, found = findResourceByHost(md, "webserver01", "Disk Usage")
	require.True(t, found)
	mn = metricsByName(rm.ScopeMetrics().At(0))

	dp = mn["nagios.check.state"].Gauge().DataPoints().At(0)
	assert.Equal(t, int64(1), dp.IntValue())
	sa, _ = dp.Attributes().Get("nagios.state")
	assert.Equal(t, "warning", sa.Str())

	// /=6789MB;7000;7500;0;8000
	diskDp, found := findDP(mn["nagios.perfdata.value"], "/")
	require.True(t, found)
	assert.InDelta(t, 6789.0, diskDp.DoubleValue(), 0.1)
	u, _ = diskDp.Attributes().Get("nagios.perfdata.unit")
	assert.Equal(t, "MBy", u.Str())

	warnDp, found := findDP(mn["nagios.perfdata.warning"], "/")
	require.True(t, found)
	assert.InDelta(t, 7000.0, warnDp.DoubleValue(), 0.1)

	critDp, found := findDP(mn["nagios.perfdata.critical"], "/")
	require.True(t, found)
	assert.InDelta(t, 7500.0, critDp.DoubleValue(), 0.1)

	// --- dbserver01 / MySQL ---
	rm, found = findResourceByHost(md, "dbserver01", "MySQL")
	require.True(t, found)
	mn = metricsByName(rm.ScopeMetrics().At(0))

	dp = mn["nagios.check.state"].Gauge().DataPoints().At(0)
	assert.Equal(t, int64(0), dp.IntValue())

	mysqlDp, found := findDP(mn["nagios.perfdata.value"], "time")
	require.True(t, found)
	assert.InDelta(t, 0.001234, mysqlDp.DoubleValue(), 0.000001)

	mysqlWarn, found := findDP(mn["nagios.perfdata.warning"], "time")
	require.True(t, found)
	assert.InDelta(t, 3.0, mysqlWarn.DoubleValue(), 0.1)

	mysqlCrit, found := findDP(mn["nagios.perfdata.critical"], "time")
	require.True(t, found)
	assert.InDelta(t, 5.0, mysqlCrit.DoubleValue(), 0.1)
}

// --- Integration: File Mode (PNP4Nagios) ---

func TestIntegration_FileMode_PNP4Nagios(t *testing.T) {
	dir := t.TempDir()
	svcFile := filepath.Join(dir, "service-perfdata")
	hostFile := filepath.Join(dir, "host-perfdata")

	cfg := &Config{
		MetricsBuilderConfig: allMetricsEnabled(),
		File: &FileConfig{
			ServicePerfdataFile: svcFile,
			HostPerfdataFile:    hostFile,
			Format:              "pnp4nagios",
		},
	}

	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)

	logger := zaptest.NewLogger(t)
	tailer := newFileTailer(cfg.File, logger)

	require.NoError(t, os.WriteFile(svcFile, []byte(""), 0644))
	require.NoError(t, os.WriteFile(hostFile, []byte(""), 0644))
	require.NoError(t, tailer.start(context.Background(), nil))
	s.source = tailer
	defer s.shutdown(context.Background())

	svcData := "HOSTNAME::webserver01\tSERVICEDESC::HTTP Check\tSERVICESTATE::OK\tSERVICEOUTPUT::HTTP OK\tSERVICEPERFDATA::time=0.001s;;;0;10 size=3302B;;;0\tSERVICECHECKCOMMAND::check_http!-p 80\n"
	svcData += "HOSTNAME::dbserver01\tSERVICEDESC::MySQL\tSERVICESTATE::CRITICAL\tSERVICEOUTPUT::MySQL DOWN\tSERVICEPERFDATA::time=5.0s;3;5\tSERVICECHECKCOMMAND::check_mysql!-H db01\n"
	hostData := "HOSTNAME::webserver01\tHOSTSTATE::UP\tHOSTOUTPUT::PING OK\tHOSTPERFDATA::rta=0.456ms;100;500;0;1000 pl=0%;20;60;0;100\tHOSTCHECKCOMMAND::check_ping!100,20%!500,60%\n"

	appendToFile(t, svcFile, svcData)
	appendToFile(t, hostFile, hostData)

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, md.ResourceMetrics().Len())

	// --- webserver01 / HTTP Check ---
	rm, found := findResourceByHost(md, "webserver01", "HTTP Check")
	require.True(t, found)

	attrs := resourceAttrs(rm)
	assert.Equal(t, "file", attrs["nagios.source"])
	assert.Equal(t, "check_http", attrs["nagios.check.command"])

	mn := metricsByName(rm.ScopeMetrics().At(0))
	dp := mn["nagios.check.state"].Gauge().DataPoints().At(0)
	assert.Equal(t, int64(0), dp.IntValue())
	assert.Equal(t, 2, mn["nagios.perfdata.value"].Gauge().DataPoints().Len())

	// --- dbserver01 / MySQL (CRITICAL) ---
	rm, found = findResourceByHost(md, "dbserver01", "MySQL")
	require.True(t, found)
	assert.Equal(t, "check_mysql", resourceAttrs(rm)["nagios.check.command"])

	mn = metricsByName(rm.ScopeMetrics().At(0))
	dp = mn["nagios.check.state"].Gauge().DataPoints().At(0)
	assert.Equal(t, int64(2), dp.IntValue())
	sa, _ := dp.Attributes().Get("nagios.state")
	assert.Equal(t, "critical", sa.Str())

	// --- webserver01 / Host Check ---
	rm, found = findResourceByHost(md, "webserver01", "Host Check")
	require.True(t, found)

	attrs = resourceAttrs(rm)
	assert.Equal(t, "file", attrs["nagios.source"])
	assert.Equal(t, "check_ping", attrs["nagios.check.command"])

	mn = metricsByName(rm.ScopeMetrics().At(0))
	dp = mn["nagios.check.state"].Gauge().DataPoints().At(0)
	assert.Equal(t, int64(0), dp.IntValue())

	assert.Equal(t, 2, mn["nagios.perfdata.value"].Gauge().DataPoints().Len())

	rtaDp, found := findDP(mn["nagios.perfdata.value"], "rta")
	require.True(t, found)
	assert.InDelta(t, 0.456, rtaDp.DoubleValue(), 0.001)
	u, _ := rtaDp.Attributes().Get("nagios.perfdata.unit")
	assert.Equal(t, "ms", u.Str())

	plDp, found := findDP(mn["nagios.perfdata.value"], "pl")
	require.True(t, found)
	assert.InDelta(t, 0.0, plDp.DoubleValue(), 0.001)
	u, _ = plDp.Attributes().Get("nagios.perfdata.unit")
	assert.Equal(t, "%", u.Str())
}

// --- Integration: Livestatus Mode ---

func TestIntegration_LivestatusMode(t *testing.T) {
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

		lines := "webserver01\tHTTP Check\tcheck_http!-p 80\t0\ttime=0.001s;;;0;10 size=3302B;;;0\tHTTP OK\t1520553350\t0.001\t0.05\n"
		lines += "webserver01\tDisk Usage\tcheck_disk!-w 20%\t1\t/=6789MB;7000;7500;0;8000\tDISK WARNING\t1520553300\t0.01\t0.02\n"
		lines += "dbserver01\tMySQL\tcheck_mysql\t2\ttime=5.0s;3;5\tMySQL CRITICAL\t1520553400\t5.0\t0.01\n"
		fmt.Fprint(conn, lines)
	}()

	cfg := &Config{
		MetricsBuilderConfig: allMetricsEnabled(),
		Livestatus:           &LivestatusConfig{Address: socketPath, Network: "unix"},
	}

	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	s.source = newLivestatusClient(cfg.Livestatus, zaptest.NewLogger(t))

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, md.ResourceMetrics().Len())

	// --- webserver01 / HTTP Check ---
	rm, found := findResourceByHost(md, "webserver01", "HTTP Check")
	require.True(t, found)

	attrs := resourceAttrs(rm)
	assert.Equal(t, "livestatus", attrs["nagios.source"])
	assert.Equal(t, "check_http", attrs["nagios.check.command"])

	mn := metricsByName(rm.ScopeMetrics().At(0))

	dp := mn["nagios.check.state"].Gauge().DataPoints().At(0)
	assert.Equal(t, int64(0), dp.IntValue())

	require.Contains(t, mn, "nagios.check.latency")
	assert.InDelta(t, 0.05, mn["nagios.check.latency"].Gauge().DataPoints().At(0).DoubleValue(), 0.001)

	require.Contains(t, mn, "nagios.check.last_check")
	assert.Equal(t, int64(1520553350), mn["nagios.check.last_check"].Gauge().DataPoints().At(0).IntValue())

	assert.Equal(t, 2, mn["nagios.perfdata.value"].Gauge().DataPoints().Len())

	// --- webserver01 / Disk Usage (WARNING) ---
	rm, found = findResourceByHost(md, "webserver01", "Disk Usage")
	require.True(t, found)
	assert.Equal(t, "check_disk", resourceAttrs(rm)["nagios.check.command"])

	mn = metricsByName(rm.ScopeMetrics().At(0))
	dp = mn["nagios.check.state"].Gauge().DataPoints().At(0)
	assert.Equal(t, int64(1), dp.IntValue())
	sa, _ := dp.Attributes().Get("nagios.state")
	assert.Equal(t, "warning", sa.Str())

	// --- dbserver01 / MySQL (CRITICAL) ---
	rm, found = findResourceByHost(md, "dbserver01", "MySQL")
	require.True(t, found)

	mn = metricsByName(rm.ScopeMetrics().At(0))
	dp = mn["nagios.check.state"].Gauge().DataPoints().At(0)
	assert.Equal(t, int64(2), dp.IntValue())
	sa, _ = dp.Attributes().Get("nagios.state")
	assert.Equal(t, "critical", sa.Str())

	assert.InDelta(t, 5.0, mn["nagios.check.execution_time"].Gauge().DataPoints().At(0).DoubleValue(), 0.1)
}

// --- Integration: Default metrics only (disabled metrics omitted) ---

func TestIntegration_DefaultMetricsOnly(t *testing.T) {
	cfg := &Config{
		MetricsBuilderConfig: metadata.DefaultMetricsBuilderConfig(),
		API:                  &APIConfig{},
	}

	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)

	s.source = &mockDataSource{
		results: []NagiosCheckResult{
			{
				HostName:           "host1",
				ServiceDescription: "svc1",
				State:              0,
				PerfData:           "val=42;80;90;0;100",
				ExecutionTime:      0.1,
				Latency:            0.05,
				LastCheck:          1520553350,
			},
		},
	}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)

	mn := metricsByName(md.ResourceMetrics().At(0).ScopeMetrics().At(0))

	// Enabled by default
	assert.Contains(t, mn, "nagios.check.state")
	assert.Contains(t, mn, "nagios.check.execution_time")
	assert.Contains(t, mn, "nagios.perfdata.value")

	// Disabled by default
	assert.NotContains(t, mn, "nagios.check.latency")
	assert.NotContains(t, mn, "nagios.check.last_check")
	assert.NotContains(t, mn, "nagios.perfdata.warning")
	assert.NotContains(t, mn, "nagios.perfdata.critical")
	assert.NotContains(t, mn, "nagios.perfdata.min")
	assert.NotContains(t, mn, "nagios.perfdata.max")
}

// --- Test data ---

const fullAPIResponse = `{
  "format_version": 0,
  "result": {
    "query_time": 1520553414000,
    "cgi": "statusjson.cgi",
    "user": "nagiosadmin",
    "query": "servicelist",
    "query_status": "Released",
    "program_start": 1520355947000,
    "last_data_update": 1520553407000,
    "type_code": 0,
    "type_text": "Success",
    "message": ""
  },
  "data": {
    "selectors": {"details": true},
    "servicelist": {
      "webserver01": {
        "HTTP Check": {
          "host_name": "webserver01",
          "description": "HTTP Check",
          "plugin_output": "HTTP OK: HTTP/1.1 200 OK - 3302 bytes in 0.001 second response time",
          "long_plugin_output": "",
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
          "long_plugin_output": "",
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
          "long_plugin_output": "",
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
