# Changelog

All notable changes are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [1.0.0] - 2026-10-14

### Added
- DNS checker (miekg/dns): any record type, custom resolver, RCODE/TTL reporting,
  UDP retries, TCP fallback on truncation, `expect` assertions, PTR for IPs.
- HTTP checker: httptrace phase breakdown (dns/connect/tls/server/transfer),
  status and body assertions, headers, methods, redirect control, 1 MiB body cap.
- TLS checker: chain inspection with independent trust/hostname verification,
  earliest-expiry-in-chain, warn/crit thresholds, legacy-protocol warning, custom CA.
- TCP latency checker: min/avg/max/jitter/loss, SYN-retransmission detection.
- `check` command with table/JSON/Markdown output and Nagios exit codes.
- `watch` live terminal dashboard (Bubble Tea).
- `serve` exporter: Prometheus metrics, embedded web dashboard, JSON API,
  `/healthz` + `/readyz`, SIGHUP hot reload, graceful shutdown.
- `init`, `validate` and `version` commands.
- Docker image, compose stack with Prometheus + Grafana, alert rules, systemd
  unit, Kubernetes manifests, Fly.io and Render configs, CI and release pipelines.
