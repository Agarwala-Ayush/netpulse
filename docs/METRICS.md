# Metrics reference

`netpulse serve` exposes metrics at `/metrics` in the Prometheus text format, or OpenMetrics when the scraper asks for it.

Common labels:

| Label | Values |
|---|---|
| `target` | target `name` from config |
| `group` | target `group` from config (may be empty) |
| `check` | `dns`, `http`, `tls`, `tcp` |

All label values come from configuration or small fixed sets, so the number of series is bounded by *targets × checks*.

## Check results

| Metric | Type | Labels | Description |
|---|---|---|---|
| `netpulse_check_up` | gauge | target, group, check | `1` if the last run did not fail (ok or warn), else `0`. |
| `netpulse_check_status` | gauge | + `status` (ok, warn, fail) | State set: the current status is `1`, the others `0`. |
| `netpulse_check_duration_seconds` | histogram | target, group, check | Latency distribution. Buckets: 1ms … 10s. |
| `netpulse_check_latency_seconds` | gauge | target, group, check | Latency of the most recent run. |
| `netpulse_check_phase_duration_seconds` | gauge | + `phase` | Phase durations of the last run (HTTP: `dns`, `connect`, `tls`, `server`, `transfer`; TLS: `connect`, `handshake`; DNS: one per record type; TCP: `resolve`). |
| `netpulse_check_runs_total` | counter | + `status` | Runs by result. |
| `netpulse_check_last_run_timestamp_seconds` | gauge | target, group, check | When the check last completed. |

## Protocol details

| Metric | Type | Labels | Description |
|---|---|---|---|
| `netpulse_http_status_code` | gauge | target, group | Status code of the last response. |
| `netpulse_tls_cert_expiry_timestamp_seconds` | gauge | target, group | Unix time when the earliest-expiring cert in the presented chain expires. |
| `netpulse_tls_cert_verified` | gauge | target, group | `1` if the chain verified for the expected hostname. |
| `netpulse_dns_records` | gauge | target, group, `type` | Number of records returned per type. |
| `netpulse_tcp_rtt_seconds` | gauge | target, group, `stat` (min, avg, max, jitter) | Connect RTT statistics. |
| `netpulse_tcp_loss_ratio` | gauge | target, group | Failed connect attempts / attempts (0–1). |

## Process

| Metric | Description |
|---|---|
| `netpulse_targets` | Number of configured jobs. |
| `netpulse_config_reloads_total{result}` | Reload attempts (success / failure). |
| `netpulse_config_last_reload_successful` | `0` after a rejected reload. |
| `netpulse_config_last_reload_success_timestamp_seconds` | Last good load. |
| `netpulse_build_info{version,commit,goversion}` | Always `1`. |
| `go_*`, `process_*` | Go runtime and process stats (memory, goroutines, GC, FDs, CPU). |

## Useful PromQL

```promql
# Everything currently failing
netpulse_check_up == 0

# Count of checks by status
sum by (status) (netpulse_check_status)

# Days until each certificate expires, soonest first
sort((netpulse_tls_cert_expiry_timestamp_seconds - time()) / 86400)

# p95 / p99 HTTP latency per target over 10 minutes
histogram_quantile(0.95, sum by (le, target) (rate(netpulse_check_duration_seconds_bucket{check="http"}[10m])))
histogram_quantile(0.99, sum by (le, target) (rate(netpulse_check_duration_seconds_bucket{check="http"}[10m])))

# Availability (SLI) over the last 30 days
sum by (target, check) (increase(netpulse_check_runs_total{status!="fail"}[30d]))
  / sum by (target, check) (increase(netpulse_check_runs_total[30d]))

# Which phase dominates a slow site?
topk(1, netpulse_check_phase_duration_seconds{check="http", target="github"})

# Flapping detector: status changes in the last hour
changes(netpulse_check_up[1h]) > 4

# Detect a wedged exporter (no results for 5 minutes)
time() - netpulse_check_last_run_timestamp_seconds > 300
```

## Alerting

[`deploy/prometheus/alerts.yml`](../deploy/prometheus/alerts.yml) ships these rules:

| Alert | Fires when |
|---|---|
| `NetPulseCheckFailing` | a check fails for 2 minutes |
| `NetPulseTLSCertExpiringSoon` / `…Critical` | < 14 days / < 3 days to expiry |
| `NetPulseHighHTTPLatency` | p95 HTTP latency > 2s for 10 minutes |
| `NetPulseTCPPacketLoss` | > 20% average connect loss over 10 minutes |
| `NetPulseStale` | no results for a check in 5 minutes |
| `NetPulseDown` | Prometheus can't scrape NetPulse |
| `NetPulseConfigReloadFailed` | the last reload was rejected |

Rules exclude `group="canary"`, the intentionally broken demo targets.
