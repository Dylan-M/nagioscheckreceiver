// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
)

// livestatusClient implements dataSource for the MK Livestatus socket mode.
type livestatusClient struct {
	cfg    *LivestatusConfig
	logger *zap.Logger
}

func newLivestatusClient(cfg *LivestatusConfig, logger *zap.Logger) *livestatusClient {
	return &livestatusClient{
		cfg:    cfg,
		logger: logger,
	}
}

func (c *livestatusClient) start(_ context.Context, _ component.Host) error {
	return nil
}

func (c *livestatusClient) shutdown(_ context.Context) error {
	return nil
}

// livestatusQuery is the LQL query to fetch service check results.
// Uses custom separators to avoid semicolon collision with perfdata format:
// Separators: 10 9 44 124
//   10 = newline (row separator)
//   9  = tab (column separator)
//   44 = comma (list separator)
//   124 = pipe (host list separator)
const livestatusQuery = `GET services
Columns: host_name description check_command state perf_data plugin_output last_check execution_time latency
Separators: 10 9 44 124
OutputFormat: csv

`

func (c *livestatusClient) collect(ctx context.Context) ([]NagiosCheckResult, error) {
	// Open a new connection per scrape (Livestatus connections are short-lived)
	var d net.Dialer
	conn, err := d.DialContext(ctx, c.cfg.Network, c.cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("connecting to livestatus at %s/%s: %w", c.cfg.Network, c.cfg.Address, err)
	}
	defer conn.Close()

	// Send query
	_, err = conn.Write([]byte(livestatusQuery))
	if err != nil {
		return nil, fmt.Errorf("sending livestatus query: %w", err)
	}

	// Close write side to signal end of query
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		uc.CloseWrite()
	}

	// Read response
	var results []NagiosCheckResult
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		result, err := parseLivestatusLine(line)
		if err != nil {
			c.logger.Warn("Failed to parse livestatus line", zap.Error(err))
			continue
		}
		results = append(results, result)
	}

	if err := scanner.Err(); err != nil {
		return results, fmt.Errorf("reading livestatus response: %w", err)
	}

	return results, nil
}

// parseLivestatusLine parses a tab-delimited Livestatus response line.
// Column order matches the query: host_name, description, check_command, state,
// perf_data, plugin_output, last_check, execution_time, latency
func parseLivestatusLine(line string) (NagiosCheckResult, error) {
	fields := strings.Split(line, "\t")
	if len(fields) < 9 {
		return NagiosCheckResult{}, fmt.Errorf("expected 9 tab-delimited fields, got %d", len(fields))
	}

	state, err := strconv.Atoi(fields[3])
	if err != nil {
		return NagiosCheckResult{}, fmt.Errorf("parsing state %q: %w", fields[3], err)
	}

	lastCheck, _ := strconv.ParseInt(fields[6], 10, 64)
	execTime, _ := strconv.ParseFloat(fields[7], 64)
	latency, _ := strconv.ParseFloat(fields[8], 64)

	// Extract base command name (strip arguments after '!')
	checkCommand := fields[2]
	if idx := strings.Index(checkCommand, "!"); idx >= 0 {
		checkCommand = checkCommand[:idx]
	}

	return NagiosCheckResult{
		HostName:           fields[0],
		ServiceDescription: fields[1],
		CheckCommand:       checkCommand,
		State:              state,
		PerfData:           fields[4],
		PluginOutput:       fields[5],
		LastCheck:          lastCheck,
		ExecutionTime:      execTime,
		Latency:            latency,
	}, nil
}
