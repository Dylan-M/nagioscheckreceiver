# Nagios Check Receiver

| Status        |               |
|---------------|---------------|
| Stability     | [development] |
| Distributions | [contrib]     |

Receives Nagios check results and performance data, converting them into OpenTelemetry metrics. Supports three mutually exclusive ingestion modes:

| Mode          | Connection                    | Requires                               |
|---------------|-------------------------------|----------------------------------------|
| **API**       | HTTP(S) to Nagios JSON CGI    | Nagios Core 4.0.7+ with CGI enabled   |
| **File**      | Read local perfdata files     | Collector co-located with Nagios       |
| **Livestatus**| Unix socket or TCP            | MK Livestatus addon installed          |

## Configuration

Exactly one ingestion mode must be configured. Configuring zero or more than one is a validation error.

### API Mode

```yaml
receivers:
  nagioscheck:
    collection_interval: 30s
    api:
      endpoint: "https://nagios.example.com/nagios/cgi-bin/statusjson.cgi"
      username: "nagiosadmin"
      password: "${env:NAGIOS_PASSWORD}"
      tls:
        insecure_skip_verify: true
      retry_on_failure:
        enabled: true
        initial_interval: 5s
        max_interval: 30s
        max_elapsed_time: 120s
```

### File Mode

```yaml
receivers:
  nagioscheck:
    collection_interval: 15s
    file:
      service_perfdata_file: "/var/nagios/service-perfdata"
      host_perfdata_file: "/var/nagios/host-perfdata"
      format: "pnp4nagios"  # or "default"
```

### Livestatus Mode

```yaml
receivers:
  nagioscheck:
    collection_interval: 30s
    livestatus:
      address: "/var/run/nagios/rw/live"
      network: "unix"  # or "tcp" with address like "nagios-host:6557"
```

## Metrics

### Static Metrics

| Metric                         | Type         | Unit | Default | Description                                    |
|--------------------------------|--------------|------|---------|------------------------------------------------|
| `nagios.check.state`           | Gauge (int)  | `1`  | Enabled | 0=OK, 1=WARNING, 2=CRITICAL, 3=UNKNOWN       |
| `nagios.check.execution_time`  | Gauge (dbl)  | `s`  | Enabled | Check execution duration                       |
| `nagios.check.latency`         | Gauge (dbl)  | `s`  | Disabled| Scheduling latency                             |
| `nagios.check.last_check`      | Gauge (int)  | `s`  | Disabled| Unix timestamp of last check                   |

### Dynamic Perfdata Metrics

| Metric                    | Type         | Unit | Default | Description                        |
|---------------------------|--------------|------|---------|------------------------------------|
| `nagios.perfdata.value`   | Gauge (dbl)  | `1`  | Enabled | Metric value with label/unit attrs |
| `nagios.perfdata.warning`  | Gauge (dbl)  | `1`  | Disabled| Warning threshold upper bound      |
| `nagios.perfdata.critical` | Gauge (dbl)  | `1`  | Disabled| Critical threshold upper bound     |
| `nagios.perfdata.min`     | Gauge (dbl)  | `1`  | Disabled| Minimum possible value             |
| `nagios.perfdata.max`     | Gauge (dbl)  | `1`  | Disabled| Maximum possible value             |

### Resource Attributes

| Attribute                    | Description                                              |
|------------------------------|----------------------------------------------------------|
| `nagios.host.name`           | The Nagios host name                                     |
| `nagios.service.description` | The service check name                                   |
| `nagios.check.command`       | Base check command (Livestatus/PNP4Nagios modes only)   |
| `nagios.source`              | Ingestion mode: `"api"`, `"file"`, or `"livestatus"`   |

## Security

This receiver enforces a strict read-only posture. There is no `exec.Command` or process spawning anywhere in the codebase. It does not execute Nagios plugins.
