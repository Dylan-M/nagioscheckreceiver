// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/nagioscheckreceiver/internal/metadata"
)

type mockDataSource struct {
	results []NagiosCheckResult
	err     error
}

func (m *mockDataSource) start(_ context.Context, _ component.Host) error { return nil }
func (m *mockDataSource) shutdown(_ context.Context) error                { return nil }
func (m *mockDataSource) collect(_ context.Context) ([]NagiosCheckResult, error) {
	return m.results, m.err
}

func TestScraper_Scrape(t *testing.T) {
	cfg := &Config{
		API:                  &APIConfig{},
		MetricsBuilderConfig: metadata.DefaultMetricsBuilderConfig(),
	}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)

	s.source = &mockDataSource{
		results: []NagiosCheckResult{
			{
				HostName:           "webserver01",
				ServiceDescription: "HTTP Check",
				State:              0,
				PluginOutput:       "HTTP OK: HTTP/1.1 200 OK",
				PerfData:           "time=0.000647s;;;0.000000;10.000000 size=3302B;;;0",
				ExecutionTime:      0.001,
				Latency:            0.05,
			},
		},
	}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, md.ResourceMetrics().Len())

	rm := md.ResourceMetrics().At(0)
	attrs := rm.Resource().Attributes()

	hostVal, ok := attrs.Get("nagios.host.name")
	require.True(t, ok)
	assert.Equal(t, "webserver01", hostVal.Str())

	svcVal, ok := attrs.Get("nagios.service.description")
	require.True(t, ok)
	assert.Equal(t, "HTTP Check", svcVal.Str())

	sourceVal, ok := attrs.Get("nagios.source")
	require.True(t, ok)
	assert.Equal(t, "api", sourceVal.Str())

	// MetricsBuilder emits enabled metrics: check.state, check.execution_time, perfdata.value (x2)
	sm := rm.ScopeMetrics().At(0)
	assert.GreaterOrEqual(t, sm.Metrics().Len(), 3)

	// Find and verify state metric
	found := false
	for i := 0; i < sm.Metrics().Len(); i++ {
		m := sm.Metrics().At(i)
		if m.Name() == "nagios.check.state" {
			found = true
			assert.Equal(t, int64(0), m.Gauge().DataPoints().At(0).IntValue())
			stateAttr, ok := m.Gauge().DataPoints().At(0).Attributes().Get("nagios.state")
			require.True(t, ok)
			assert.Equal(t, "ok", stateAttr.Str())
		}
	}
	assert.True(t, found, "nagios.check.state metric not found")
}

func TestScraper_ScrapeEmptyResults(t *testing.T) {
	cfg := &Config{
		API:                  &APIConfig{},
		MetricsBuilderConfig: metadata.DefaultMetricsBuilderConfig(),
	}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)

	s.source = &mockDataSource{results: nil}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, md.ResourceMetrics().Len())
}

func TestScraper_CheckCommandAttribute(t *testing.T) {
	cfg := &Config{
		Livestatus:           &LivestatusConfig{Address: "/tmp/live", Network: "unix"},
		MetricsBuilderConfig: metadata.DefaultMetricsBuilderConfig(),
	}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)

	s.source = &mockDataSource{
		results: []NagiosCheckResult{
			{
				HostName:           "db01",
				ServiceDescription: "MySQL",
				CheckCommand:       "check_mysql",
				State:              0,
				PerfData:           "uptime=12345s",
				ExecutionTime:      0.5,
			},
		},
	}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)

	rm := md.ResourceMetrics().At(0)
	cmdVal, ok := rm.Resource().Attributes().Get("nagios.check.command")
	require.True(t, ok)
	assert.Equal(t, "check_mysql", cmdVal.Str())

	sourceVal, ok := rm.Resource().Attributes().Get("nagios.source")
	require.True(t, ok)
	assert.Equal(t, "livestatus", sourceVal.Str())
}

func TestScraper_WarningState(t *testing.T) {
	cfg := &Config{
		File:                 &FileConfig{ServicePerfdataFile: "/tmp/perf", Format: "default"},
		MetricsBuilderConfig: metadata.DefaultMetricsBuilderConfig(),
	}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)

	s.source = &mockDataSource{
		results: []NagiosCheckResult{
			{
				HostName:           "disk01",
				ServiceDescription: "Disk Usage",
				State:              1,
				PerfData:           "/=85%;80;90;0;100",
				ExecutionTime:      0.01,
			},
		},
	}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)

	sm := md.ResourceMetrics().At(0).ScopeMetrics().At(0)
	for i := 0; i < sm.Metrics().Len(); i++ {
		m := sm.Metrics().At(i)
		if m.Name() == "nagios.check.state" {
			assert.Equal(t, int64(1), m.Gauge().DataPoints().At(0).IntValue())
			stateAttr, ok := m.Gauge().DataPoints().At(0).Attributes().Get("nagios.state")
			require.True(t, ok)
			assert.Equal(t, "warning", stateAttr.Str())
		}
	}
}

func TestScraper_LatencyAndLastCheckEmitted(t *testing.T) {
	// Enable the disabled-by-default metrics
	mbc := metadata.DefaultMetricsBuilderConfig()
	mbc.Metrics.NagiosCheckLatency.Enabled = true
	mbc.Metrics.NagiosCheckLastCheck.Enabled = true

	cfg := &Config{
		API:                  &APIConfig{},
		MetricsBuilderConfig: mbc,
	}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)

	s.source = &mockDataSource{
		results: []NagiosCheckResult{
			{
				HostName:           "host1",
				ServiceDescription: "svc1",
				State:              0,
				ExecutionTime:      0.1,
				Latency:            0.05,
				LastCheck:          1520553350,
			},
		},
	}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)

	sm := md.ResourceMetrics().At(0).ScopeMetrics().At(0)

	foundLatency := false
	foundLastCheck := false
	for i := 0; i < sm.Metrics().Len(); i++ {
		m := sm.Metrics().At(i)
		switch m.Name() {
		case "nagios.check.latency":
			foundLatency = true
			assert.InDelta(t, 0.05, m.Gauge().DataPoints().At(0).DoubleValue(), 0.001)
		case "nagios.check.last_check":
			foundLastCheck = true
			assert.Equal(t, int64(1520553350), m.Gauge().DataPoints().At(0).IntValue())
		}
	}
	assert.True(t, foundLatency, "nagios.check.latency metric should be emitted when enabled")
	assert.True(t, foundLastCheck, "nagios.check.last_check metric should be emitted when enabled")
}

func TestScraper_DisabledMetricsNotEmitted(t *testing.T) {
	// Default config: latency and last_check are disabled
	cfg := &Config{
		API:                  &APIConfig{},
		MetricsBuilderConfig: metadata.DefaultMetricsBuilderConfig(),
	}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)

	s.source = &mockDataSource{
		results: []NagiosCheckResult{
			{
				HostName:           "host1",
				ServiceDescription: "svc1",
				State:              0,
				ExecutionTime:      0.1,
				Latency:            0.05,
				LastCheck:          1520553350,
			},
		},
	}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)

	sm := md.ResourceMetrics().At(0).ScopeMetrics().At(0)

	for i := 0; i < sm.Metrics().Len(); i++ {
		m := sm.Metrics().At(i)
		assert.NotEqual(t, "nagios.check.latency", m.Name(), "latency should not be emitted when disabled")
		assert.NotEqual(t, "nagios.check.last_check", m.Name(), "last_check should not be emitted when disabled")
	}
}
