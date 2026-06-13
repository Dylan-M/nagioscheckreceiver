// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap/zaptest"
)

func basicAuthServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "nagiosadmin" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(fullAPIResponse))
	}))
}

// API mode sends HTTP basic auth and succeeds with correct credentials.
func TestIntegration_APIMode_BasicAuth(t *testing.T) {
	server := basicAuthServer()
	defer server.Close()

	cfg := &Config{MetricsBuilderConfig: allMetricsEnabled(), API: &APIConfig{
		ClientConfig: confighttp.ClientConfig{Endpoint: server.URL},
		Username:     "nagiosadmin",
		Password:     "secret",
	}}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	s.source = &apiClient{cfg: cfg.API, logger: zaptest.NewLogger(t), client: server.Client()}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, md.ResourceMetrics().Len())
}

// API mode surfaces the 401 path as an error when credentials are wrong.
func TestIntegration_APIMode_Unauthorized(t *testing.T) {
	server := basicAuthServer()
	defer server.Close()

	cfg := &Config{MetricsBuilderConfig: allMetricsEnabled(), API: &APIConfig{
		ClientConfig: confighttp.ClientConfig{Endpoint: server.URL},
		Username:     "nagiosadmin",
		Password:     "wrong",
	}}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	s.source = &apiClient{cfg: cfg.API, logger: zaptest.NewLogger(t), client: server.Client()}

	_, err := s.scrape(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

// API mode over TLS with insecure_skip_verify, building the client via the real start() path.
func TestIntegration_APIMode_TLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(fullAPIResponse))
	}))
	defer server.Close()

	cfg := &Config{MetricsBuilderConfig: allMetricsEnabled(), API: &APIConfig{
		ClientConfig: confighttp.ClientConfig{Endpoint: server.URL},
	}}
	cfg.API.ClientConfig.TLS.InsecureSkipVerify = true

	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	require.NoError(t, s.start(context.Background(), componenttest.NewNopHost()))
	defer s.shutdown(context.Background())

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, md.ResourceMetrics().Len())
}

// API mode retries transient (5xx) failures when retry_on_failure is enabled.
func TestIntegration_APIMode_Retry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(fullAPIResponse))
	}))
	defer server.Close()

	cfg := &Config{MetricsBuilderConfig: allMetricsEnabled(), API: &APIConfig{
		ClientConfig: confighttp.ClientConfig{Endpoint: server.URL},
		RetryOnFailure: configretry.BackOffConfig{
			Enabled:         true,
			InitialInterval: 5 * time.Millisecond,
			MaxInterval:     20 * time.Millisecond,
			MaxElapsedTime:  5 * time.Second,
			Multiplier:      1.5,
		},
	}}
	params := receivertest.NewNopSettings(component.MustNewType(typeStr))
	s := newNagiosScraper(params, cfg)
	s.source = &apiClient{cfg: cfg.API, logger: zaptest.NewLogger(t), client: server.Client()}

	md, err := s.scrape(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, md.ResourceMetrics().Len())
	assert.GreaterOrEqual(t, calls.Load(), int32(3), "should have retried past the 503 responses")
}
