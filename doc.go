// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:generate mdatagen metadata.yaml

// Package nagioscheckreceiver implements an OpenTelemetry Collector receiver
// that ingests Nagios check results and performance data via three mutually
// exclusive modes: JSON CGI API, perfdata file tailing, or Livestatus socket.
package nagioscheckreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/nagioscheckreceiver"
