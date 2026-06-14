# Nagios Check Receiver

| Status        |               |
|---------------|---------------|
| Stability     | [development] |
| Distributions | []            |

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
| `host.name`                  | The monitored host name (OTel semantic convention)       |
| `nagios.service.description` | The service check name                                   |
| `nagios.check.command`       | Base check command (Livestatus/PNP4Nagios modes only)   |
| `nagios.source`              | Ingestion mode: `"api"`, `"file"`, or `"livestatus"`   |

## Reshaping output: folding sensors into metric names

By design this receiver emits a small, generic set of metric names and carries the
sensor identity in attributes: a single `nagios.perfdata.value` metric holds one
data point per perfdata label (`load1`, `load5`, `load15`, …), distinguished by the
`nagios.perfdata.label` data-point attribute and the `nagios.service.description`
resource attribute. This is the OpenTelemetry-idiomatic shape (dimensions live in
attributes, not in the name) and is lossless for a migration bridge.

If the target system instead wants **one metric name per sensor** (e.g.
`Current Load.load1.nagios.perfdata.value`), you can reshape downstream in the
collector pipeline. Note this requires *splitting* the metric by a data-point
attribute, which the `transform` processor cannot do on its own — it edits in place.
Put the `groupbyattrs` processor first to fan each label into its own resource
(promoting `nagios.perfdata.label` to a resource attribute), then let `transform`
fold the now-resource-level `service` + `label` into the name and drop them:

```yaml
processors:
  # 1. Split: fan each perfdata label into its own resource, promoting the
  #    label to a resource attribute so it becomes available to transform.
  groupbyattrs:
    keys: [nagios.perfdata.label]
  # 2. Fold service + label into the metric name and drop the folded attributes.
  transform:
    error_mode: ignore
    metric_statements:
      - context: metric
        statements:
          - set(name, Concat([resource.attributes["nagios.service.description"], resource.attributes["nagios.perfdata.label"], name], ".")) where resource.attributes["nagios.perfdata.label"] != nil
      - context: resource
        statements:
          # drop only the attributes that were folded into the name (perfdata metrics only);
          # host.name is emitted by the receiver and kept as the resource identifier.
          - delete_key(attributes, "nagios.service.description") where attributes["nagios.perfdata.label"] != nil
          - delete_key(attributes, "nagios.perfdata.label")

service:
  pipelines:
    metrics:
      receivers: [nagioscheck]
      processors: [groupbyattrs, transform]   # order matters: split before fold
      exporters: [...]
```

Produces, per sensor:

```
Current Load.load1.nagios.perfdata.value   {host.name=localhost, nagios.check.command=check_local_load, nagios.source=livestatus}
Disk Root./.nagios.perfdata.value          {host.name=nagios-db, ...}
HTTP 8080.time.nagios.perfdata.value       {host.name=nagios-web, ...}
```

Notes:
- The `where … label != nil` guard leaves non-perfdata metrics (`nagios.check.state`,
  `nagios.check.execution_time`, …) untouched — they keep their generic names and
  their `nagios.service.description` attribute, since they are per-check, not per-sensor.
- Service descriptions containing spaces appear verbatim in the name
  (`Current Load.load1…`). If your backend dislikes spaces, add a
  `replace_pattern(name, " ", "_")` statement.

## Security

This receiver enforces a strict read-only posture. There is no `exec.Command` or process spawning anywhere in the codebase. It does not execute Nagios plugins.
