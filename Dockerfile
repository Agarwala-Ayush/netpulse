# syntax=docker/dockerfile:1

# ---- build stage -----------------------------------------------------------
# Full Go toolchain; nothing from this stage ships except the binary.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src

# Download modules first so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev COMMIT=none
# CGO_ENABLED=0 → a fully static binary that runs on an empty base image.
# Go cross-compiles natively, so multi-arch images need no emulation.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w \
        -X github.com/agarwala-ayush/netpulse/internal/version.Version=${VERSION} \
        -X github.com/agarwala-ayush/netpulse/internal/version.Commit=${COMMIT} \
        -X github.com/agarwala-ayush/netpulse/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -o /out/netpulse ./cmd/netpulse

# ---- runtime stage ---------------------------------------------------------
# distroless/static: CA certificates, tzdata and /etc/passwd only. No shell,
# no package manager → small image (~17 MB: a 15 MB static binary + ~2 MB base) and minimal attack surface.
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="netpulse" \
      org.opencontainers.image.description="Concurrent DNS/HTTP/TLS/latency health checker with Prometheus metrics" \
      org.opencontainers.image.source="https://github.com/agarwala-ayush/netpulse" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build /out/netpulse /usr/local/bin/netpulse
COPY configs/netpulse.example.yaml /etc/netpulse/netpulse.yaml

# Runs as uid 65532 ("nonroot"); no extra capabilities needed because the
# latency check uses TCP connects instead of raw ICMP sockets.
USER nonroot:nonroot
EXPOSE 9469
ENTRYPOINT ["/usr/local/bin/netpulse"]
CMD ["serve", "--config", "/etc/netpulse/netpulse.yaml", "--log-format", "json"]
