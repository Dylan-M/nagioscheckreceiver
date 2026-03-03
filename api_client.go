// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cenkalti/backoff/v4"
	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
)

// apiClient implements dataSource for the Nagios JSON CGI API mode.
type apiClient struct {
	cfg    *APIConfig
	logger *zap.Logger
	client *http.Client
}

func newAPIClient(cfg *APIConfig, logger *zap.Logger) *apiClient {
	return &apiClient{
		cfg:    cfg,
		logger: logger,
	}
}

func (c *apiClient) start(ctx context.Context, host component.Host) error {
	httpClient, err := c.cfg.ClientConfig.ToClient(ctx, host.GetExtensions(), component.TelemetrySettings{})
	if err != nil {
		return fmt.Errorf("creating HTTP client: %w", err)
	}
	c.client = httpClient
	return nil
}

func (c *apiClient) shutdown(_ context.Context) error {
	return nil
}

func (c *apiClient) collect(ctx context.Context) ([]NagiosCheckResult, error) {
	var results []NagiosCheckResult
	var lastErr error

	operation := func() error {
		var err error
		results, err = c.fetchServiceList(ctx)
		if err != nil {
			lastErr = err
			// Only retry on transient errors
			if isTransientError(err) {
				return err
			}
			return backoff.Permanent(err)
		}
		return nil
	}

	if c.cfg.RetryOnFailure.Enabled {
		bo := backoff.NewExponentialBackOff()
		bo.InitialInterval = c.cfg.RetryOnFailure.InitialInterval
		bo.MaxInterval = c.cfg.RetryOnFailure.MaxInterval
		bo.MaxElapsedTime = c.cfg.RetryOnFailure.MaxElapsedTime
		bo.RandomizationFactor = c.cfg.RetryOnFailure.RandomizationFactor

		err := backoff.Retry(operation, backoff.WithContext(bo, ctx))
		if err != nil {
			return nil, err
		}
	} else {
		if err := operation(); err != nil {
			return nil, lastErr
		}
	}

	return results, nil
}

func (c *apiClient) fetchServiceList(ctx context.Context) ([]NagiosCheckResult, error) {
	url := c.cfg.Endpoint + "?query=servicelist&details=true"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, string(c.cfg.Password))
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, &transientError{err: fmt.Errorf("executing request: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("authentication failed (HTTP 401)")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &transientError{err: fmt.Errorf("reading response body: %w", err)}
	}

	if resp.StatusCode >= 500 {
		return nil, &transientError{err: fmt.Errorf("server error (HTTP %d): %s", resp.StatusCode, string(body))}
	}

	return c.parseServiceListResponse(body)
}

// nagiosCGIResponse represents the top-level JSON envelope from statusjson.cgi.
type nagiosCGIResponse struct {
	FormatVersion int              `json:"format_version"`
	Result        nagiosCGIResult  `json:"result"`
	Data          *json.RawMessage `json:"data"`
}

type nagiosCGIResult struct {
	TypeCode int    `json:"type_code"`
	TypeText string `json:"type_text"`
	Message  string `json:"message"`
}

type serviceListData struct {
	ServiceList map[string]map[string]apiServiceStatus `json:"servicelist"`
}

type apiServiceStatus struct {
	HostName      string  `json:"host_name"`
	Description   string  `json:"description"`
	PluginOutput  string  `json:"plugin_output"`
	PerfData      string  `json:"perf_data"`
	Status        int     `json:"status"`
	LastCheck     int64   `json:"last_check"`
	ExecutionTime float64 `json:"execution_time"`
	Latency       float64 `json:"latency"`
}

// apiStatusToState maps Nagios CGI bitmask status values to standard state integers.
var apiStatusToState = map[int]int{
	1:  3, // Pending -> UNKNOWN
	2:  0, // OK
	4:  1, // WARNING
	8:  3, // UNKNOWN
	16: 2, // CRITICAL
}

func (c *apiClient) parseServiceListResponse(body []byte) ([]NagiosCheckResult, error) {
	var envelope nagiosCGIResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parsing JSON response: %w", err)
	}

	if envelope.Result.TypeCode != 0 {
		return nil, fmt.Errorf("nagios API error (%s): %s", envelope.Result.TypeText, envelope.Result.Message)
	}

	if envelope.Data == nil {
		return nil, fmt.Errorf("response has no data field")
	}

	var data serviceListData
	if err := json.Unmarshal(*envelope.Data, &data); err != nil {
		return nil, fmt.Errorf("parsing servicelist data: %w", err)
	}

	var results []NagiosCheckResult
	for hostName, services := range data.ServiceList {
		for _, svc := range services {
			state, ok := apiStatusToState[svc.Status]
			if !ok {
				c.logger.Warn("Unknown API status bitmask",
					zap.Int("status", svc.Status),
					zap.String("host", hostName),
					zap.String("service", svc.Description),
				)
				state = 3 // Unknown
			}

			results = append(results, NagiosCheckResult{
				HostName:           hostName,
				ServiceDescription: svc.Description,
				State:              state,
				PluginOutput:       svc.PluginOutput,
				PerfData:           svc.PerfData,
				LastCheck:          svc.LastCheck / 1000, // CGI returns milliseconds
				ExecutionTime:      svc.ExecutionTime,
				Latency:            svc.Latency,
			})
		}
	}

	return results, nil
}

// transientError wraps errors that should be retried.
type transientError struct {
	err error
}

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func isTransientError(err error) bool {
	_, ok := err.(*transientError)
	return ok
}
