// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap/zaptest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/nagioscheckreceiver/internal/metadata"
)

func TestParseLivestatusLine_TooFewFields(t *testing.T) {
	_, err := parseLivestatusLine("only\ttwo\tfields")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "9 tab-delimited fields")
}

func TestParseLivestatusLine_BadState(t *testing.T) {
	_, err := parseLivestatusLine("h\ts\tcmd\tNOTANINT\tperf\tout\t123\t0.1\t0.2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing state")
}

func TestLivestatusClient_ConnectionError(t *testing.T) {
	c := newLivestatusClient(&LivestatusConfig{Address: "/nonexistent/socket", Network: "unix"}, zaptest.NewLogger(t))
	_, err := c.collect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connecting to livestatus")
}

func TestScraper_CreateDataSource_NoModeErrors(t *testing.T) {
	s := &nagiosScraper{cfg: &Config{}}
	_, err := s.createDataSource()
	require.Error(t, err)
}

func TestScraper_ShutdownNilSourceIsNoop(t *testing.T) {
	s := &nagiosScraper{}
	require.NoError(t, s.shutdown(context.Background()))
}

func TestScraper_ScrapePropagatesCollectError(t *testing.T) {
	cfg := &Config{API: &APIConfig{}, MetricsBuilderConfig: metadata.DefaultMetricsBuilderConfig()}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	s.source = &mockDataSource{err: errors.New("boom")}

	_, err := s.scrape(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collecting check results")
}
