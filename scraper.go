// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/multierr"
	"go.uber.org/zap"
)

// dataSource is the interface all ingestion modes implement.
type dataSource interface {
	start(ctx context.Context, host component.Host) error
	collect(ctx context.Context) ([]NagiosCheckResult, error)
	shutdown(ctx context.Context) error
}

// NagiosCheckResult represents a single Nagios check result from any ingestion mode.
type NagiosCheckResult struct {
	HostName           string
	ServiceDescription string
	CheckCommand       string // May be empty if not available
	State              int    // 0=OK, 1=WARNING, 2=CRITICAL, 3=UNKNOWN
	PluginOutput       string
	PerfData           string
	LastCheck          int64   // Unix timestamp in seconds
	ExecutionTime      float64 // Seconds
	Latency            float64 // Seconds
}

// stateNames maps state integers to human-readable names.
var stateNames = map[int]string{
	0: "ok",
	1: "warning",
	2: "critical",
	3: "unknown",
}

type nagiosScraper struct {
	cfg    *Config
	logger *zap.Logger
	source dataSource
}

func newNagiosScraper(params receiver.Settings, cfg *Config) *nagiosScraper {
	return &nagiosScraper{
		cfg:    cfg,
		logger: params.Logger,
	}
}

func (s *nagiosScraper) start(ctx context.Context, host component.Host) error {
	var err error
	s.source, err = s.createDataSource()
	if err != nil {
		return fmt.Errorf("creating data source: %w", err)
	}
	return s.source.start(ctx, host)
}

func (s *nagiosScraper) createDataSource() (dataSource, error) {
	switch {
	case s.cfg.API != nil:
		return newAPIClient(s.cfg.API, s.logger), nil
	case s.cfg.File != nil:
		return newFileTailer(s.cfg.File, s.logger), nil
	case s.cfg.Livestatus != nil:
		return newLivestatusClient(s.cfg.Livestatus, s.logger), nil
	default:
		return nil, fmt.Errorf("no ingestion mode configured")
	}
}

func (s *nagiosScraper) shutdown(ctx context.Context) error {
	if s.source != nil {
		return s.source.shutdown(ctx)
	}
	return nil
}

func (s *nagiosScraper) scrape(ctx context.Context) (pmetric.Metrics, error) {
	results, err := s.source.collect(ctx)
	if err != nil {
		return pmetric.NewMetrics(), fmt.Errorf("collecting check results: %w", err)
	}

	if len(results) == 0 {
		return pmetric.NewMetrics(), nil
	}

	md := pmetric.NewMetrics()
	var errs error

	sourceName := s.sourceName()

	for _, result := range results {
		rm := md.ResourceMetrics().AppendEmpty()
		res := rm.Resource()

		res.Attributes().PutStr("nagios.host.name", result.HostName)
		res.Attributes().PutStr("nagios.service.description", result.ServiceDescription)
		if result.CheckCommand != "" {
			res.Attributes().PutStr("nagios.check.command", result.CheckCommand)
		}
		res.Attributes().PutStr("nagios.source", sourceName)

		sm := rm.ScopeMetrics().AppendEmpty()
		sm.Scope().SetName("github.com/open-telemetry/opentelemetry-collector-contrib/receiver/nagioscheckreceiver")
		sm.Scope().SetVersion("0.1.0")

		now := pcommon.NewTimestampFromTime(time.Now())

		// nagios.check.state
		stateName, ok := stateNames[result.State]
		if !ok {
			stateName = "unknown"
		}
		stateMetric := sm.Metrics().AppendEmpty()
		stateMetric.SetName("nagios.check.state")
		stateMetric.SetDescription("Nagios check state as an integer: 0=OK, 1=WARNING, 2=CRITICAL, 3=UNKNOWN.")
		stateMetric.SetUnit("1")
		stateGauge := stateMetric.SetEmptyGauge()
		dp := stateGauge.DataPoints().AppendEmpty()
		dp.SetTimestamp(now)
		dp.SetIntValue(int64(result.State))
		dp.Attributes().PutStr("nagios.state", stateName)

		// nagios.check.execution_time
		execMetric := sm.Metrics().AppendEmpty()
		execMetric.SetName("nagios.check.execution_time")
		execMetric.SetDescription("Check execution duration in seconds.")
		execMetric.SetUnit("s")
		execGauge := execMetric.SetEmptyGauge()
		execDp := execGauge.DataPoints().AppendEmpty()
		execDp.SetTimestamp(now)
		execDp.SetDoubleValue(result.ExecutionTime)

		// Parse and emit perfdata metrics
		if result.PerfData != "" {
			perfMetrics, parseErr := ParsePerfdata(result.PerfData)
			if parseErr != nil {
				s.logger.Warn("Failed to parse perfdata",
					zap.String("host", result.HostName),
					zap.String("service", result.ServiceDescription),
					zap.Error(parseErr),
				)
				errs = multierr.Append(errs, parseErr)
			}

			for _, pm := range perfMetrics {
				perfValueMetric := sm.Metrics().AppendEmpty()
				perfValueMetric.SetName("nagios.perfdata.value")
				perfValueMetric.SetDescription("The performance data metric value from Nagios plugin output.")
				perfValueMetric.SetUnit("1")
				perfGauge := perfValueMetric.SetEmptyGauge()
				perfDp := perfGauge.DataPoints().AppendEmpty()
				perfDp.SetTimestamp(now)
				perfDp.SetDoubleValue(pm.Value)
				perfDp.Attributes().PutStr("nagios.perfdata.label", pm.Label)
				perfDp.Attributes().PutStr("nagios.perfdata.unit", pm.Unit)

				if pm.Warning != nil {
					warnMetric := sm.Metrics().AppendEmpty()
					warnMetric.SetName("nagios.perfdata.warning")
					warnMetric.SetDescription("Warning threshold upper bound from performance data.")
					warnMetric.SetUnit("1")
					warnGauge := warnMetric.SetEmptyGauge()
					warnDp := warnGauge.DataPoints().AppendEmpty()
					warnDp.SetTimestamp(now)
					warnDp.SetDoubleValue(*pm.Warning)
					warnDp.Attributes().PutStr("nagios.perfdata.label", pm.Label)
					warnDp.Attributes().PutStr("nagios.perfdata.unit", pm.Unit)
				}

				if pm.Critical != nil {
					critMetric := sm.Metrics().AppendEmpty()
					critMetric.SetName("nagios.perfdata.critical")
					critMetric.SetDescription("Critical threshold upper bound from performance data.")
					critMetric.SetUnit("1")
					critGauge := critMetric.SetEmptyGauge()
					critDp := critGauge.DataPoints().AppendEmpty()
					critDp.SetTimestamp(now)
					critDp.SetDoubleValue(*pm.Critical)
					critDp.Attributes().PutStr("nagios.perfdata.label", pm.Label)
					critDp.Attributes().PutStr("nagios.perfdata.unit", pm.Unit)
				}

				if pm.Min != nil {
					minMetric := sm.Metrics().AppendEmpty()
					minMetric.SetName("nagios.perfdata.min")
					minMetric.SetDescription("Minimum possible value from performance data.")
					minMetric.SetUnit("1")
					minGauge := minMetric.SetEmptyGauge()
					minDp := minGauge.DataPoints().AppendEmpty()
					minDp.SetTimestamp(now)
					minDp.SetDoubleValue(*pm.Min)
					minDp.Attributes().PutStr("nagios.perfdata.label", pm.Label)
					minDp.Attributes().PutStr("nagios.perfdata.unit", pm.Unit)
				}

				if pm.Max != nil {
					maxMetric := sm.Metrics().AppendEmpty()
					maxMetric.SetName("nagios.perfdata.max")
					maxMetric.SetDescription("Maximum possible value from performance data.")
					maxMetric.SetUnit("1")
					maxGauge := maxMetric.SetEmptyGauge()
					maxDp := maxGauge.DataPoints().AppendEmpty()
					maxDp.SetTimestamp(now)
					maxDp.SetDoubleValue(*pm.Max)
					maxDp.Attributes().PutStr("nagios.perfdata.label", pm.Label)
					maxDp.Attributes().PutStr("nagios.perfdata.unit", pm.Unit)
				}
			}
		}
	}

	return md, errs
}

func (s *nagiosScraper) sourceName() string {
	switch {
	case s.cfg.API != nil:
		return "api"
	case s.cfg.File != nil:
		return "file"
	case s.cfg.Livestatus != nil:
		return "livestatus"
	default:
		return "unknown"
	}
}
