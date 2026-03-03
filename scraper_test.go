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
		API: &APIConfig{},
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

	// Should have check.state + execution_time + 2 perfdata.value metrics + 2 min metrics
	sm := rm.ScopeMetrics().At(0)
	assert.GreaterOrEqual(t, sm.Metrics().Len(), 4)

	// Verify state metric
	stateMetric := sm.Metrics().At(0)
	assert.Equal(t, "nagios.check.state", stateMetric.Name())
	assert.Equal(t, int64(0), stateMetric.Gauge().DataPoints().At(0).IntValue())

	stateAttr, ok := stateMetric.Gauge().DataPoints().At(0).Attributes().Get("nagios.state")
	require.True(t, ok)
	assert.Equal(t, "ok", stateAttr.Str())

	// Verify execution time metric
	execMetric := sm.Metrics().At(1)
	assert.Equal(t, "nagios.check.execution_time", execMetric.Name())
	assert.InDelta(t, 0.001, execMetric.Gauge().DataPoints().At(0).DoubleValue(), 0.0001)
}

func TestScraper_ScrapeEmptyResults(t *testing.T) {
	cfg := &Config{
		API: &APIConfig{},
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
		Livestatus: &LivestatusConfig{Address: "/tmp/live", Network: "unix"},
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
		File: &FileConfig{ServicePerfdataFile: "/tmp/perf", Format: "default"},
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
	stateMetric := sm.Metrics().At(0)
	assert.Equal(t, int64(1), stateMetric.Gauge().DataPoints().At(0).IntValue())

	stateAttr, ok := stateMetric.Gauge().DataPoints().At(0).Attributes().Get("nagios.state")
	require.True(t, ok)
	assert.Equal(t, "warning", stateAttr.Str())
}
