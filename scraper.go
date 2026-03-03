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

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/nagioscheckreceiver/internal/metadata"
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

// stateToAttribute maps state integers to the generated attribute enum.
var stateToAttribute = map[int]metadata.AttributeNagiosState{
	0: metadata.AttributeNagiosStateOk,
	1: metadata.AttributeNagiosStateWarning,
	2: metadata.AttributeNagiosStateCritical,
	3: metadata.AttributeNagiosStateUnknown,
}

type nagiosScraper struct {
	cfg    *Config
	logger *zap.Logger
	mb     *metadata.MetricsBuilder
	source dataSource
}

func newNagiosScraper(params receiver.Settings, cfg *Config) *nagiosScraper {
	return &nagiosScraper{
		cfg:    cfg,
		logger: params.Logger,
		mb:     metadata.NewMetricsBuilder(cfg.MetricsBuilderConfig, params),
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

	var errs error
	sourceName := s.sourceName()
	now := pcommon.NewTimestampFromTime(time.Now())

	for _, result := range results {
		// Record static check metrics
		stateAttr, ok := stateToAttribute[result.State]
		if !ok {
			stateAttr = metadata.AttributeNagiosStateUnknown
		}
		s.mb.RecordNagiosCheckStateDataPoint(now, int64(result.State), stateAttr)
		s.mb.RecordNagiosCheckExecutionTimeDataPoint(now, result.ExecutionTime)
		s.mb.RecordNagiosCheckLatencyDataPoint(now, result.Latency)

		if result.LastCheck > 0 {
			s.mb.RecordNagiosCheckLastCheckDataPoint(now, result.LastCheck)
		}

		// Parse and record perfdata metrics
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
				s.mb.RecordNagiosPerfdataValueDataPoint(now, pm.Value, pm.Label, pm.Unit)

				if pm.Warning != nil {
					s.mb.RecordNagiosPerfdataWarningDataPoint(now, *pm.Warning, pm.Label, pm.Unit)
				}
				if pm.Critical != nil {
					s.mb.RecordNagiosPerfdataCriticalDataPoint(now, *pm.Critical, pm.Label, pm.Unit)
				}
				if pm.Min != nil {
					s.mb.RecordNagiosPerfdataMinDataPoint(now, *pm.Min, pm.Label, pm.Unit)
				}
				if pm.Max != nil {
					s.mb.RecordNagiosPerfdataMaxDataPoint(now, *pm.Max, pm.Label, pm.Unit)
				}
			}
		}

		// Build resource and emit for this host/service
		rb := s.mb.NewResourceBuilder()
		rb.SetNagiosHostName(result.HostName)
		rb.SetNagiosServiceDescription(result.ServiceDescription)
		rb.SetNagiosSource(sourceName)
		if result.CheckCommand != "" {
			rb.SetNagiosCheckCommand(result.CheckCommand)
		}

		s.mb.EmitForResource(metadata.WithResource(rb.Emit()))
	}

	return s.mb.Emit(), errs
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
