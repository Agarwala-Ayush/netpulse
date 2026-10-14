# Deployment guide

NetPulse is a single static binary with no external dependencies. Pick whichever environment fits.

- [Binary](#binary)
- [Docker](#docker)
- [Docker Compose: full monitoring stack](#docker-compose-full-monitoring-stack)
- [Linux with systemd](#linux-with-systemd)
- [Kubernetes](#kubernetes)
- [Public hosting: Fly.io](#public-hosting-flyio)
- [Public hosting: Render](#public-hosting-render)
- [A $0 VPS option](#a-0-vps-option)
- [Releasing](#releasing)

## Binary

```bash
go install github.com/agarwala-ayush/netpulse/cmd/netpulse@latest
# or download a release archive from GitHub Releases
netpulse serve -c netpulse.yaml     # http://localhost:9469
```

## Docker

```bash
docker run -d --name netpulse -p 9469:9469 \
  -v "$PWD/netpulse.yaml:/etc/netpulse/netpulse.yaml:ro" \
  ghcr.io/agarwala-ayush/netpulse:latest

docker kill -s HUP netpulse   # hot-reload after editing the file
```

The image is built `FROM gcr.io/distroless/static-debian12:nonroot`: it runs as uid 65532 and contains no shell. Pass secrets used in `${VAR}` expansion with `-e API_TOKEN=…`.

## Docker Compose: full monitoring stack

```bash
make stack-up        # or: docker compose -f deploy/docker-compose.yml up -d --build
```

| Service | URL | Notes |
|---|---|---|
| NetPulse | http://localhost:9469 | web dashboard and `/metrics` |
| Prometheus | http://localhost:9090 | scrapes NetPulse every 15s; alert rules under *Alerts* |
| Grafana | http://localhost:3000 | anonymous viewer; the *NetPulse — Network Health* dashboard is the home page |

## Linux with systemd

```bash
sudo install -m 0755 bin/netpulse /usr/local/bin/netpulse
sudo install -D -m 0644 configs/netpulse.example.yaml /etc/netpulse/netpulse.yaml
sudo cp deploy/systemd/netpulse.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now netpulse

systemctl status netpulse
journalctl -u netpulse -f              # JSON logs
sudo systemctl reload netpulse         # SIGHUP → hot reload
```

The unit validates the config before starting (`ExecStartPre`) and runs fully sandboxed: `DynamicUser`, `ProtectSystem=strict`, no capabilities, restricted address families and a syscall allow-list. Secrets can go in an `EnvironmentFile=` drop-in (`systemctl edit netpulse`).

## Kubernetes

```bash
kubectl apply -f deploy/kubernetes/netpulse.yaml
kubectl -n netpulse port-forward svc/netpulse 9469:9469
```

Includes a Namespace, a ConfigMap with the config, a Deployment with liveness (`/healthz`) and readiness (`/readyz`) probes, a restricted security context (non-root, read-only root filesystem, all capabilities dropped), a Service, and a `ServiceMonitor` for the Prometheus Operator (delete it if you don't run the operator).

## Public hosting: Fly.io

Fly runs the Docker image on small always-on VMs, which suits a monitor that must keep running.

```bash
brew install flyctl            # or: curl -L https://fly.io/install.sh | sh
fly auth login
fly launch --no-deploy --copy-config --name <your-unique-name>
fly deploy
fly open                       # https://<your-unique-name>.fly.dev
```

`fly.toml` keeps one machine always running (`auto_stop_machines = "off"`), health-checks `/healthz`, and sets `PORT=9469`. To use your own targets, edit `configs/netpulse.example.yaml` (it's baked into the image) and redeploy.

## Public hosting: Render

1. Push the repository to GitHub.
2. In Render: **New → Blueprint**, then select the repository. `render.yaml` defines a Docker web service with a `/healthz` health check.
3. Render injects `$PORT`; NetPulse picks it up automatically.

The free plan sleeps after about 15 minutes without traffic, so checks pause while nobody is looking and history resets on wake-up. That's fine for a portfolio demo; use a paid instance for real 24/7 monitoring.

## A $0 VPS option

Oracle Cloud's "Always Free" ARM VMs, or any small VPS, can run the full compose stack:

```bash
# on the VM
sudo apt-get install -y docker.io docker-compose-v2 git
git clone https://github.com/agarwala-ayush/netpulse && cd netpulse
sudo docker compose -f deploy/docker-compose.yml up -d --build
```

Put Caddy or nginx in front for HTTPS, and don't expose Grafana or Prometheus publicly without authentication.

## Releasing

```bash
git tag v1.0.0 && git push origin v1.0.0
```

The `Release` workflow then:

- builds binaries for linux/darwin/windows × amd64/arm64 with GoReleaser and attaches them, with checksums, to a GitHub Release, and
- builds and pushes a multi-arch image to `ghcr.io/<owner>/netpulse:{1.0.0, 1.0, latest}`.

After the first image push, open the package on GitHub (*Packages → netpulse → Package settings*) and set visibility to **Public** so `docker pull` works without logging in.
