# Single-instance Nagios test environment

A repeatable end-to-end test for `nagioscheckreceiver`. Everything lives on **one
host**, so it runs in CI with no inter-node networking: a single box runs Nagios
Core 4 exposing all three ingestion surfaces the receiver supports, plus local
workloads so the data is real and varied.

| Mode | Surface on the box |
|------|--------------------|
| **API** | Apache + `statusjson.cgi` (basic auth) at `http://127.0.0.1/cgi-bin/nagios4/statusjson.cgi` |
| **File** | perfdata files `/var/lib/nagios4/{service,host}-perfdata` (default tab format) |
| **Livestatus** | MK Livestatus unix socket `/var/lib/nagios4/rw/live` |

The `nagios-testenv` host is monitored with stock local plugins (load, users,
disk, procs, swap, ping, http) at a 1-minute cadence. A `stress-ng` workload
drives load/procs/swap to swing `OK ↔ WARNING ↔ CRITICAL` so the series move.

## Files

- `provision.sh` — stands up the whole stack on a Debian/Ubuntu host (idempotent, run as root).
- `run-e2e.sh` — builds a collector embedding the receiver (via `ocb`) and asserts all three modes carry metrics.
- `configs/{api,file,livestatus}.yaml` — one collector config per mode (receiver + file exporter).
- `../.github/workflows/e2e.yml` — runs the above on every PR/push on `ubuntu-latest`.

## Running locally

On a throwaway Debian 12 / Ubuntu 24.04 box (a VM, not your workstation, it
installs and starts system services):

```bash
sudo bash testenv/provision.sh
sudo REPO_ROOT="$PWD" bash testenv/run-e2e.sh   # REPO_ROOT = receiver module root
```

`run-e2e.sh` prints `PASS[api]`, `PASS[livestatus]`, `PASS[file]`, and finally
`E2E RESULT: PASS`. For each mode it asserts metrics reach a file exporter,
carry the `host.name` resource attribute, and report the expected
`nagios.source`.

## MK Livestatus build note

There is no maintained Nagios-4 Livestatus package, so `provision.sh` builds the
[`ageric/livestatus`](https://github.com/ageric/livestatus) fork from source
against the matching Nagios Core source headers. On modern toolchains (gcc 13/14
on Ubuntu 24.04) it needs three compatibility flags that older toolchains
(Debian 12 / gcc 12) did not:

- `-I…/nagios` (+ source root) so `<nagios/objects.h>` and `"lib/libnagios.h"` resolve,
- `-include pthread.h` for the `pthread_mutex_*` declarations newer glibc no longer pulls in transitively,
- `-fcommon` to merge tentative definitions (gcc 10+ defaults to `-fno-common`, which otherwise trips a `g_mainthread_id` multiple-definition link error).

These are applied in the livestatus build step of `provision.sh`.

## Why single-instance

The original manual test bed was a 4-VM fleet (one Nagios server + three NRPE
workload hosts). CI runners can't network separate hosts together, so the fleet
is collapsed onto one box: the workloads run locally and Nagios monitors
localhost. The receiver sees the same three surfaces it would in a distributed
deployment.
