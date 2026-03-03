// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

func TestNewFactory(t *testing.T) {
	f := NewFactory()
	assert.Equal(t, "nagioscheck", f.Type().String())
}

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig()
	require.NotNil(t, cfg)

	rcvCfg, ok := cfg.(*Config)
	require.True(t, ok)
	assert.Equal(t, defaultCollectionInterval, rcvCfg.ControllerConfig.CollectionInterval)
	assert.Nil(t, rcvCfg.API)
	assert.Nil(t, rcvCfg.File)
	assert.Nil(t, rcvCfg.Livestatus)
}

func TestCreateMetricsReceiver(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.Livestatus = &LivestatusConfig{
		Address: "/tmp/test-live",
		Network: "unix",
	}

	r, err := createMetricsReceiver(
		context.Background(),
		receivertest.NewNopSettings(component.MustNewType(typeStr)),
		cfg,
		consumertest.NewNop(),
	)
	require.NoError(t, err)
	require.NotNil(t, r)

	// Verify the receiver can be started and stopped (will fail to connect, but shouldn't panic)
	err = r.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)
	err = r.Shutdown(context.Background())
	require.NoError(t, err)
}
