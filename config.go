// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"errors"
	"fmt"

	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/configopaque"
	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/scraper/scraperhelper"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/nagioscheckreceiver/internal/metadata"
)

// Config defines configuration for the Nagios check receiver.
type Config struct {
	scraperhelper.ControllerConfig `mapstructure:",squash"`

	metadata.MetricsBuilderConfig `mapstructure:",squash"`

	// Exactly one of API, File, or Livestatus must be set.
	API        *APIConfig        `mapstructure:"api"`
	File       *FileConfig       `mapstructure:"file"`
	Livestatus *LivestatusConfig `mapstructure:"livestatus"`
}

// APIConfig defines configuration for the Nagios JSON CGI API mode.
type APIConfig struct {
	confighttp.ClientConfig `mapstructure:",squash"`

	// Username for HTTP Basic Auth.
	Username string `mapstructure:"username"`

	// Password for HTTP Basic Auth.
	Password configopaque.String `mapstructure:"password"`

	// RetryOnFailure defines retry settings for transient failures.
	RetryOnFailure configretry.BackOffConfig `mapstructure:"retry_on_failure"`
}

// FileConfig defines configuration for the perfdata file tailing mode.
type FileConfig struct {
	// ServicePerfdataFile is the path to the Nagios service perfdata file.
	ServicePerfdataFile string `mapstructure:"service_perfdata_file"`

	// HostPerfdataFile is the path to the Nagios host perfdata file (optional).
	HostPerfdataFile string `mapstructure:"host_perfdata_file"`

	// Format is the perfdata file format: "default" or "pnp4nagios".
	Format string `mapstructure:"format"`
}

// LivestatusConfig defines configuration for the MK Livestatus socket mode.
type LivestatusConfig struct {
	// Address is the socket path (Unix) or host:port (TCP).
	Address string `mapstructure:"address"`

	// Network is "unix" or "tcp".
	Network string `mapstructure:"network"`
}

// Validate checks that the configuration is valid.
func (cfg *Config) Validate() error {
	modeCount := 0
	if cfg.API != nil {
		modeCount++
	}
	if cfg.File != nil {
		modeCount++
	}
	if cfg.Livestatus != nil {
		modeCount++
	}

	if modeCount == 0 {
		return errors.New("exactly one ingestion mode must be configured: api, file, or livestatus")
	}
	if modeCount > 1 {
		return errors.New("only one ingestion mode may be configured at a time: api, file, or livestatus")
	}

	if cfg.API != nil {
		if err := validateAPIConfig(cfg.API); err != nil {
			return fmt.Errorf("api config: %w", err)
		}
	}

	if cfg.File != nil {
		if err := validateFileConfig(cfg.File); err != nil {
			return fmt.Errorf("file config: %w", err)
		}
	}

	if cfg.Livestatus != nil {
		if err := validateLivestatusConfig(cfg.Livestatus); err != nil {
			return fmt.Errorf("livestatus config: %w", err)
		}
	}

	return nil
}

func validateAPIConfig(cfg *APIConfig) error {
	if cfg.Endpoint == "" {
		return errors.New("endpoint is required")
	}
	return nil
}

func validateFileConfig(cfg *FileConfig) error {
	if cfg.ServicePerfdataFile == "" {
		return errors.New("service_perfdata_file is required")
	}
	switch cfg.Format {
	case "", "default", "pnp4nagios":
		// valid
	default:
		return fmt.Errorf("unsupported format %q: must be \"default\" or \"pnp4nagios\"", cfg.Format)
	}
	return nil
}

func validateLivestatusConfig(cfg *LivestatusConfig) error {
	if cfg.Address == "" {
		return errors.New("address is required")
	}
	switch cfg.Network {
	case "unix", "tcp":
		// valid
	case "":
		return errors.New("network is required: \"unix\" or \"tcp\"")
	default:
		return fmt.Errorf("unsupported network %q: must be \"unix\" or \"tcp\"", cfg.Network)
	}
	return nil
}
