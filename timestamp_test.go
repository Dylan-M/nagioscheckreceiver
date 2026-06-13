// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/nagioscheckreceiver/internal/metadata"
)

// Datapoints must be stamped at the check's real time (last_check), not scrape time.
func TestScraper_DataPointTimestampIsLastCheck(t *testing.T) {
	cfg := &Config{API: &APIConfig{}, MetricsBuilderConfig: metadata.DefaultMetricsBuilderConfig()}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)

	const lastCheck int64 = 1520553350
	s.source = &mockDataSource{results: []NagiosCheckResult{{
		HostName:           "host1",
		ServiceDescription: "svc1",
		State:              0,
		PerfData:           "time=0.5s;;;0;10",
		ExecutionTime:      0.1,
		LastCheck:          lastCheck,
	}}}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	want := pcommon.NewTimestampFromTime(time.Unix(lastCheck, 0))

	sm := md.ResourceMetrics().At(0).ScopeMetrics().At(0)
	require.Greater(t, sm.Metrics().Len(), 0)
	for i := 0; i < sm.Metrics().Len(); i++ {
		m := sm.Metrics().At(i)
		dps := m.Gauge().DataPoints()
		for j := 0; j < dps.Len(); j++ {
			assert.Equalf(t, want, dps.At(j).Timestamp(),
				"metric %s datapoint must be stamped at last_check, not scrape time", m.Name())
		}
	}
}

// When last_check is unknown (0), datapoints fall back to scrape time.
func TestScraper_TimestampFallsBackToNowWhenNoLastCheck(t *testing.T) {
	cfg := &Config{API: &APIConfig{}, MetricsBuilderConfig: metadata.DefaultMetricsBuilderConfig()}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)

	before := pcommon.NewTimestampFromTime(time.Now())
	s.source = &mockDataSource{results: []NagiosCheckResult{{
		HostName: "h", ServiceDescription: "s", State: 0, ExecutionTime: 0.1, // LastCheck == 0
	}}}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	after := pcommon.NewTimestampFromTime(time.Now())

	dp := md.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics().At(0).Gauge().DataPoints().At(0)
	assert.GreaterOrEqual(t, uint64(dp.Timestamp()), uint64(before))
	assert.LessOrEqual(t, uint64(dp.Timestamp()), uint64(after))
}

// File parsers must populate LastCheck from the TIMET field so file mode can stamp at real time.
func TestParseDefaultLine_PopulatesLastCheck(t *testing.T) {
	r, err := parseDefaultLine("[SERVICEPERFDATA]\t1520553350\thost1\tsvc1\tOK\tplugin out\tx=1;2;3")
	require.NoError(t, err)
	assert.Equal(t, int64(1520553350), r.LastCheck)
}

func TestParseDefaultHostLine_PopulatesLastCheck(t *testing.T) {
	r, err := parseDefaultHostLine("[HOSTPERFDATA]\t1520553350\thost1\tUP\tplugin out\trta=0.1")
	require.NoError(t, err)
	assert.Equal(t, int64(1520553350), r.LastCheck)
}

func TestParsePNP4NagiosLine_PopulatesLastCheck(t *testing.T) {
	r, err := parsePNP4NagiosLine("DATATYPE::SERVICEPERFDATA\tTIMET::1520553350\tHOSTNAME::host1\tSERVICEDESC::svc1\tSERVICESTATE::OK\tSERVICEPERFDATA::x=1")
	require.NoError(t, err)
	assert.Equal(t, int64(1520553350), r.LastCheck)
}

func TestParsePNP4NagiosHostLine_PopulatesLastCheck(t *testing.T) {
	r, err := parsePNP4NagiosHostLine("DATATYPE::HOSTPERFDATA\tTIMET::1520553350\tHOSTNAME::host1\tHOSTSTATE::UP\tHOSTPERFDATA::rta=0.1")
	require.NoError(t, err)
	assert.Equal(t, int64(1520553350), r.LastCheck)
}
