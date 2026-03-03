// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package nagioscheckreceiver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/config/confighttp"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "no mode configured",
			cfg:     Config{},
			wantErr: "exactly one ingestion mode must be configured",
		},
		{
			name: "two modes configured",
			cfg: Config{
				API: &APIConfig{
					ClientConfig: confighttp.ClientConfig{Endpoint: "http://localhost"},
				},
				File: &FileConfig{
					ServicePerfdataFile: "/tmp/perfdata",
				},
			},
			wantErr: "only one ingestion mode may be configured",
		},
		{
			name: "api mode valid",
			cfg: Config{
				API: &APIConfig{
					ClientConfig: confighttp.ClientConfig{Endpoint: "http://localhost/nagios/cgi-bin/statusjson.cgi"},
					Username:     "admin",
					Password:     "secret",
				},
			},
		},
		{
			name: "api mode missing endpoint",
			cfg: Config{
				API: &APIConfig{},
			},
			wantErr: "api config: endpoint is required",
		},
		{
			name: "file mode valid default format",
			cfg: Config{
				File: &FileConfig{
					ServicePerfdataFile: "/var/nagios/service-perfdata",
					Format:              "default",
				},
			},
		},
		{
			name: "file mode valid pnp4nagios format",
			cfg: Config{
				File: &FileConfig{
					ServicePerfdataFile: "/var/nagios/service-perfdata",
					Format:              "pnp4nagios",
				},
			},
		},
		{
			name: "file mode missing service file",
			cfg: Config{
				File: &FileConfig{},
			},
			wantErr: "file config: service_perfdata_file is required",
		},
		{
			name: "file mode invalid format",
			cfg: Config{
				File: &FileConfig{
					ServicePerfdataFile: "/tmp/perfdata",
					Format:              "invalid",
				},
			},
			wantErr: "file config: unsupported format",
		},
		{
			name: "livestatus mode valid unix",
			cfg: Config{
				Livestatus: &LivestatusConfig{
					Address: "/var/run/nagios/rw/live",
					Network: "unix",
				},
			},
		},
		{
			name: "livestatus mode valid tcp",
			cfg: Config{
				Livestatus: &LivestatusConfig{
					Address: "nagios-host:6557",
					Network: "tcp",
				},
			},
		},
		{
			name: "livestatus missing address",
			cfg: Config{
				Livestatus: &LivestatusConfig{
					Network: "unix",
				},
			},
			wantErr: "livestatus config: address is required",
		},
		{
			name: "livestatus missing network",
			cfg: Config{
				Livestatus: &LivestatusConfig{
					Address: "/tmp/live",
				},
			},
			wantErr: "livestatus config: network is required",
		},
		{
			name: "livestatus invalid network",
			cfg: Config{
				Livestatus: &LivestatusConfig{
					Address: "/tmp/live",
					Network: "udp",
				},
			},
			wantErr: "livestatus config: unsupported network",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
