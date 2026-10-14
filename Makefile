# NetPulse — developer tasks. Run `make help` for a list.

BINARY   := netpulse
PKG      := github.com/agarwala-ayush/netpulse
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
            -X $(PKG)/internal/version.Version=$(VERSION) \
            -X $(PKG)/internal/version.Commit=$(COMMIT) \
            -X $(PKG)/internal/version.Date=$(DATE)
IMAGE    ?= ghcr.io/agarwala-ayush/netpulse

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the binary into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/netpulse

.PHONY: install
install: ## Install into $GOPATH/bin
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/netpulse

.PHONY: run
run: build ## Run a quick check against a few public hosts
	./bin/$(BINARY) check github.com cloudflare.com google.com

.PHONY: serve
serve: build ## Run the exporter + dashboard with the example config
	./bin/$(BINARY) serve -c configs/netpulse.example.yaml

.PHONY: test
test: ## Run unit tests with the race detector
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Run tests and open an HTML coverage report
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	go tool cover -html=coverage.out -o coverage.html

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Format code
	gofmt -s -w .

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: tidy
tidy: ## Tidy go.mod
	go mod tidy

.PHONY: docker
docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

.PHONY: stack-up
stack-up: ## Start NetPulse + Prometheus + Grafana with docker compose
	docker compose -f deploy/docker-compose.yml up -d --build
	@echo "Dashboard  http://localhost:9469"
	@echo "Prometheus http://localhost:9090"
	@echo "Grafana    http://localhost:3000 (anonymous viewer enabled)"

.PHONY: stack-down
stack-down: ## Stop the docker compose stack
	docker compose -f deploy/docker-compose.yml down

.PHONY: release-snapshot
release-snapshot: ## Build release artifacts locally with GoReleaser (no publish)
	goreleaser release --snapshot --clean

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist coverage.out coverage.html
