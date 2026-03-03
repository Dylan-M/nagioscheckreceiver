// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/scraper"
	"go.opentelemetry.io/collector/scraper/scraperhelper"
)

const (
	typeStr   = "nagioscheck"
	stability = component.StabilityLevelDevelopment

	defaultCollectionInterval = 30 * time.Second
)

// NewFactory creates a new receiver factory for the Nagios check receiver.
func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		component.MustNewType(typeStr),
		createDefaultConfig,
		receiver.WithMetrics(createMetricsReceiver, stability),
	)
}

func createDefaultConfig() component.Config {
	cfg := scraperhelper.NewDefaultControllerConfig()
	cfg.CollectionInterval = defaultCollectionInterval

	return &Config{
		ControllerConfig: cfg,
	}
}

func createMetricsReceiver(
	ctx context.Context,
	params receiver.Settings,
	baseCfg component.Config,
	consumer consumer.Metrics,
) (receiver.Metrics, error) {
	cfg := baseCfg.(*Config)

	ns := newNagiosScraper(params, cfg)

	s, err := scraper.NewMetrics(
		ns.scrape,
		scraper.WithStart(ns.start),
		scraper.WithShutdown(ns.shutdown),
	)
	if err != nil {
		return nil, err
	}

	return scraperhelper.NewMetricsController(
		&cfg.ControllerConfig,
		params,
		consumer,
		scraperhelper.AddMetricsScraper(component.MustNewType(typeStr), s),
	)
}
