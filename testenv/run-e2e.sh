#!/usr/bin/env bash
#
# Build a collector that embeds nagioscheckreceiver (via the OpenTelemetry
# Collector Builder, ocb) and validate all three ingestion modes end-to-end
# against the local single-instance Nagios env stood up by provision.sh.
#
# Run as root (needs to read the livestatus socket, mode 0660 nagios:nagios).
# Assumes provision.sh has completed on the same host.
#
# Env knobs (all optional, sane defaults below):
#   REPO_ROOT          path to the receiver module (default: parent of testenv/)
#   OTELCOL_VERSION    collector release line (default: 0.147.0, matches go.mod)
#   GO_VERSION         Go to fetch if absent (default: 1.25.0)
set -euxo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="${REPO_ROOT:-$(cd "${HERE}/.." && pwd)}"
BUILD_DIR="${BUILD_DIR:-${HERE}/_build}"
OTELCOL_VERSION="${OTELCOL_VERSION:-0.147.0}"
GO_VERSION="${GO_VERSION:-1.25.0}"

# Env consumed by the collector configs in testenv/configs/.
export NAGIOS_API_ENDPOINT="${NAGIOS_API_ENDPOINT:-http://127.0.0.1/cgi-bin/nagios4/statusjson.cgi}"
export NAGIOS_ADMIN_USER="${NAGIOS_ADMIN_USER:-nagiosadmin}"
export NAGIOS_ADMIN_PASS="${NAGIOS_ADMIN_PASS:-nagiosci}"
export NAGIOS_SERVICE_PERFDATA="${NAGIOS_SERVICE_PERFDATA:-/var/lib/nagios4/service-perfdata}"
export NAGIOS_HOST_PERFDATA="${NAGIOS_HOST_PERFDATA:-/var/lib/nagios4/host-perfdata}"
export NAGIOS_LIVESTATUS_SOCKET="${NAGIOS_LIVESTATUS_SOCKET:-/var/lib/nagios4/rw/live}"

# --- ensure Go (CI runners already have it; a bare box may not) ---
if ! command -v go >/dev/null 2>&1; then
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz
  export PATH="/usr/local/go/bin:${PATH}"
fi
export PATH="$(go env GOPATH)/bin:${PATH}"

# --- build a collector embedding the local receiver ---
go install "go.opentelemetry.io/collector/cmd/builder@v${OTELCOL_VERSION}"
mkdir -p "${BUILD_DIR}"
cat > "${BUILD_DIR}/builder.yaml" <<EOF
dist:
  name: nagioscheck-e2e-collector
  description: e2e collector embedding nagioscheckreceiver
  output_path: ${BUILD_DIR}
  otelcol_version: ${OTELCOL_VERSION}
receivers:
  - gomod: github.com/open-telemetry/opentelemetry-collector-contrib/receiver/nagioscheckreceiver v${OTELCOL_VERSION}
exporters:
  - gomod: go.opentelemetry.io/collector/exporter/debugexporter v${OTELCOL_VERSION}
  - gomod: github.com/open-telemetry/opentelemetry-collector-contrib/exporter/fileexporter v${OTELCOL_VERSION}
replaces:
  - github.com/open-telemetry/opentelemetry-collector-contrib/receiver/nagioscheckreceiver => ${REPO_ROOT}
EOF
builder --config "${BUILD_DIR}/builder.yaml"
BIN="${BUILD_DIR}/nagioscheck-e2e-collector"

# --- run one mode and assert metrics actually flow through the real receiver ---
run_mode() {
  local mode="$1" cfg="$2" timeout="$3"
  local out="/tmp/e2e-${mode}-metrics.json"
  local log="/tmp/e2e-${mode}-collector.log"
  rm -f "$out"
  export E2E_OUT="$out"
  echo "### MODE: ${mode} (up to ${timeout}s) ###"
  "$BIN" --config "$cfg" >"$log" 2>&1 &
  local pid=$! ok=0 i
  for ((i=0; i<timeout; i++)); do
    if [ -s "$out" ] && grep -q 'nagios.check.state' "$out"; then ok=1; break; fi
    sleep 1
  done
  kill "$pid" >/dev/null 2>&1 || true
  wait "$pid" 2>/dev/null || true

  if [ "$ok" -ne 1 ]; then
    echo "FAIL[${mode}]: no nagios.check.state metric reached ${out} within ${timeout}s"
    tail -25 "$log" || true
    return 1
  fi
  grep -q '"host.name"' "$out"   || { echo "FAIL[${mode}]: host.name resource attribute missing"; return 1; }
  grep -q "\"${mode}\"" "$out"   || { echo "FAIL[${mode}]: nagios.source did not report '${mode}'"; return 1; }
  echo "PASS[${mode}]"
}

# API and Livestatus report current state on the first scrape; File mode tails
# from end-of-file, so it waits for Nagios to append the next check cycle.
run_mode api        "${HERE}/configs/api.yaml"        60
run_mode livestatus "${HERE}/configs/livestatus.yaml" 60
run_mode file       "${HERE}/configs/file.yaml"       150

echo "E2E RESULT: PASS"
