.PHONY: help build build-embed test test-race vet lint run-dev run-embed clean docker compose-up compose-down smoke cross-compile check-size release-snapshot verify-release

GOFLAGS ?=
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
TAG     := $(shell git describe --tags --always --dirty 2>/dev/null || echo v0.0.0-dev)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X github.com/railbase/railbase/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/railbase/railbase/internal/buildinfo.Tag=$(TAG) \
	-X github.com/railbase/railbase/internal/buildinfo.Date=$(DATE)

DEV_DSN ?= postgres://railbase:railbase@localhost:54329/railbase?sslmode=disable

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

# Native target detection — uses `go env` so the right output file gets
# the right name on any host (darwin/arm64 dev box, linux/amd64 CI, etc.).
HOST_OS   := $(shell go env GOOS)
HOST_ARCH := $(shell go env GOARCH)
HOST_EXT  := $(if $(filter windows,$(HOST_OS)),.exe,)

build: ## Build production binary (no embedded postgres). Writes bin/dist/railbase_<host>_<arch> + updates the bin/railbase symlink.
	@mkdir -p bin/dist
	go build $(GOFLAGS) -trimpath -ldflags="$(LDFLAGS)" \
	  -o bin/dist/railbase_$(HOST_OS)_$(HOST_ARCH)$(HOST_EXT) ./cmd/railbase
	@ln -sf dist/railbase_$(HOST_OS)_$(HOST_ARCH)$(HOST_EXT) bin/railbase
	@echo "→ bin/railbase → dist/railbase_$(HOST_OS)_$(HOST_ARCH)$(HOST_EXT)"

build-embed: ## Build dev binary with embedded postgres support (-tags embed_pg). Separate filename so it doesn't shadow the release symlink.
	@mkdir -p bin
	go build $(GOFLAGS) -tags embed_pg -trimpath -ldflags="$(LDFLAGS)" \
	  -o bin/railbase-embed$(HOST_EXT) ./cmd/railbase
	@echo "→ bin/railbase-embed$(HOST_EXT) (dev binary, includes embedded postgres)"

test: ## Run unit tests
	go test ./...

test-race: ## Run unit tests with race detector
	go test -race -count=1 ./...

vet: ## go vet
	go vet ./...

lint: ## golangci-lint (if installed)
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed; skipping"

smoke: ## Run the v0.9 / docs/17 #2 5-minute smoke gate
	bash scripts/smoke-5min.sh

cross-compile: ## v1 SHIP gate — build all 6 target binaries under bin/dist/
	@mkdir -p bin/dist
	@for goos in linux darwin windows; do \
	  for goarch in amd64 arm64; do \
	    ext=""; [ "$$goos" = "windows" ] && ext=".exe"; \
	    out="bin/dist/railbase_$${goos}_$${goarch}$${ext}"; \
	    echo "→ $$out"; \
	    CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch \
	      go build $(GOFLAGS) -trimpath -ldflags="$(LDFLAGS)" \
	        -o "$$out" ./cmd/railbase || exit $$?; \
	  done; \
	done
	@ln -sf dist/railbase_$(HOST_OS)_$(HOST_ARCH)$(HOST_EXT) bin/railbase
	@echo "→ bin/railbase → dist/railbase_$(HOST_OS)_$(HOST_ARCH)$(HOST_EXT)"

check-size: ## docs/17 #1 — fail if any binary in bin/dist/ > 30 MB
	bash scripts/check-binary-size.sh bin/dist/

release-snapshot: ## Local goreleaser dry-run (no publish). Needs goreleaser installed.
	@command -v goreleaser >/dev/null 2>&1 || { echo "goreleaser not installed; see https://goreleaser.com/install/"; exit 1; }
	goreleaser release --snapshot --clean --skip=publish,sign,announce,validate

verify-release: ## Pre-tag verification: vet + test-race + cross-compile + size-budget. Run before `git tag vX.Y.Z`.
	@echo "→ go vet"
	@$(MAKE) -s vet
	@echo "→ go test -race"
	@$(MAKE) -s test-race
	@echo "→ cross-compile (6 targets)"
	@$(MAKE) -s cross-compile
	@echo "→ binary size budget (docs/17 #1, ≤30 MB)"
	@$(MAKE) -s check-size
	@echo
	@echo "✓ pre-release gates green — safe to tag."

run-dev: build ## Run against local Postgres at $(DEV_DSN)
	RAILBASE_DSN=$(DEV_DSN) \
	RAILBASE_LOG_LEVEL=debug \
	RAILBASE_LOG_FORMAT=text \
	./bin/railbase serve

run-embed: build-embed ## Run with embedded postgres (downloads PG on first run)
	RAILBASE_EMBED_POSTGRES=true \
	RAILBASE_LOG_LEVEL=debug \
	RAILBASE_LOG_FORMAT=text \
	./bin/railbase-embed serve

compose-up: ## docker-compose up
	docker compose up -d --build

compose-down: ## docker-compose down
	docker compose down

docker: ## Build production docker image
	docker build \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg TAG=$(TAG) \
		--build-arg DATE=$(DATE) \
		-t railbase:$(TAG) -t railbase:latest .

clean:
	rm -rf bin/
