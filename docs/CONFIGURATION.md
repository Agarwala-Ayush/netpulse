# Configuration reference

NetPulse reads a YAML file (`--config`/`-c`). Without `-c` and without hosts on the command line it looks for `./netpulse.yaml`, `./netpulse.yml`, then `/etc/netpulse/netpulse.yaml`.

```bash
netpulse init                 # write an annotated ./netpulse.yaml
netpulse validate -c file     # check it and list what will run
```

## Processing rules

1. **Environment expansion** — `${VAR}` and `$VAR` are replaced from the environment before parsing. Use this for secrets: `Authorization: "Bearer ${API_TOKEN}"`.
2. **Strict keys** — unknown keys are an error, so a typo can never silently disable an assertion.
3. **Presence enables a check** — `tls: {}` enables TLS with defaults; omit the key to disable it.
4. **All errors at once** — validation reports every problem with its location, e.g. `targets[2] (github): http.url: must be an absolute http(s) URL`.

## Top level

| Key | Type | Default | Description |
|---|---|---|---|
| `interval` | duration | `30s` | How often each check runs (min `1s`). |
| `timeout` | duration | `10s` (capped at `interval`) | Deadline for a single check run. |
| `concurrency` | int | `16` | Max checks executing at once (1–4096). |
| `server.listen` | string | `:9469` | Address for `serve`. Overridden by `$PORT`, then by `--listen`. |
| `server.title` | string | `NetPulse` | Dashboard title. |
| `server.history` | int | `120` | Results kept per check for dashboard sparklines. |
| `targets` | list | — | At least one target is required. |

Durations use Go syntax: `500ms`, `5s`, `1m30s`.

## Target

| Key | Type | Default | Description |
|---|---|---|---|
| `name` | string | `host` | Unique ID used in metrics and UIs. Letters, digits, `.`, `_`, `-`. |
| `host` | string | — | Bare hostname or IP, with no scheme or path. |
| `group` | string | — | Free-form label (exported as the `group` metric label, filterable in the dashboard). |
| `interval` | duration | global | Per-target override. |
| `timeout` | duration | global (capped at the target interval) | Per-target override; must not exceed `interval`. |
| `dns` / `http` / `tls` / `tcp` | object | absent | Enable and configure each check. |

## `dns`

| Key | Default | Description |
|---|---|---|
| `server` | first `nameserver` in `/etc/resolv.conf`, else `1.1.1.1:53` | Resolver `ip[:port]` (port defaults to 53). |
| `types` | `[A, AAAA]` | Record types to query: A, AAAA, CNAME, MX, TXT, NS, CAA, SOA, SRV… |
| `expect` | — | Values that must all appear in the answers (e.g. IPs, CNAME targets). |
| `latency_warn` | `300ms` | Warn if total resolution time exceeds this. |

Behaviour: fails on any non-NOERROR RCODE (NXDOMAIN, SERVFAIL…), on timeouts after 3 UDP attempts, or when *no* records come back for any type. NODATA for a single type (e.g. no AAAA) is fine. If `host` is an IP address, a PTR lookup is done instead.

## `http`

| Key | Default | Description |
|---|---|---|
| `url` | `https://<host>/` | Absolute http(s) URL. |
| `method` | `GET` | GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS. |
| `headers` | — | Map of request headers (`Host` overrides the virtual host). |
| `body` | — | Request body. |
| `expect_status` | any 2xx/3xx | List of acceptable status codes. |
| `body_contains` | — | Substring that must appear in the first 1 MiB of the body. |
| `follow_redirects` | `true` | Follow up to 10 redirects; `false` evaluates the 3xx itself. |
| `insecure_skip_verify` | `false` | Skip certificate verification (use a `tls` check to inspect certs instead). |
| `ca_file` | system roots | PEM bundle of CAs to trust (private PKI). |
| `latency_warn` | `1500ms` | Warn above this total request time. |

Each run uses a fresh connection (keep-alives disabled) so the DNS/connect/TLS phases are always measured.

## `tls`

| Key | Default | Description |
|---|---|---|
| `port` | `443` | Port to connect to (e.g. `853` for DNS-over-TLS, `993` for IMAPS, `6443` for Kubernetes). |
| `server_name` | `host` | SNI name and the hostname the certificate must match. |
| `warn_days` | `30` | Warn when the earliest-expiring cert in the chain expires within this many days. |
| `crit_days` | `7` | Fail within this many days. Must be `<= warn_days`. |
| `ca_file` | system roots | PEM bundle of CAs to trust. |
| `latency_warn` | `1s` | Warn above this connect + handshake time. |

Fails on: handshake errors, expiry, untrusted chain (self-signed or missing intermediate), hostname mismatch, or `crit_days`. Warns on `warn_days`, TLS < 1.2, or slow handshakes.

## `tcp`

| Key | Default | Description |
|---|---|---|
| `port` | `443` | Port to connect to. |
| `count` | `3` | Connection attempts per run (1–100). |
| `probe_interval` | `100ms` | Pause between attempts. |
| `latency_warn` | `250ms` | Warn when the average RTT exceeds this. |

Fails when every attempt fails; warns on any loss or a high average.

## Command-line mode

`check`, `watch` and `serve` also accept hosts instead of a file:

| Flag | Description |
|---|---|
| `--checks dns,http,tls,tcp` | Which checks to enable (default: all that apply). |
| `--dns-server ip:port` | Resolver for DNS checks. |
| `--interval 10s` | Interval (`watch` defaults to 10s, `serve` to 30s). |
| `-t, --timeout 5s` | Per-check timeout; also overrides config files. |
| `--concurrency N` | (`check`) override the concurrency. |

## Examples

**Internal service with a private CA and an auth token**

```yaml
targets:
  - name: billing-api
    host: billing.internal.example
    group: payments
    http:
      url: https://billing.internal.example/healthz
      headers: { Authorization: "Bearer ${BILLING_TOKEN}" }
      ca_file: /etc/ssl/internal-ca.pem
      expect_status: [200]
      body_contains: '"db":"up"'
    tls:
      ca_file: /etc/ssl/internal-ca.pem
      warn_days: 21
```

**Make sure DNS hasn't been tampered with**

```yaml
  - name: apex-record
    host: example.com
    dns:
      server: 8.8.8.8:53
      types: [A]
      expect: ["93.184.215.14"]
```

**Database / cache reachability (TCP only)**

```yaml
  - name: postgres
    host: db.internal
    interval: 15s
    tcp: { port: 5432, count: 5, latency_warn: 20ms }
```
