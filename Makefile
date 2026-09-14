SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

BACKEND  := backend
BIN      := bin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
VL_IMAGE := victoriametrics/victoria-logs:v1.52.0
VL_TEST_CONTAINER := syslogc-test-victorialogs
TEST_VICTORIALOGS_URL ?= http://127.0.0.1:19428
FUZZTIME ?= 30s

.PHONY: help
help: ## Show targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build syslogc and loggen into ./bin
	cd $(BACKEND) && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN)/syslogc ./cmd/syslogc
	cd $(BACKEND) && CGO_ENABLED=0 go build -trimpath -o ../$(BIN)/loggen ./cmd/loggen

.PHONY: test
test: ## Unit tests with the race detector
	cd $(BACKEND) && go test -race ./...

.PHONY: lint
lint: ## golangci-lint (includes import-boundary rules)
	cd $(BACKEND) && golangci-lint run ./...

.PHONY: fmt
fmt: ## Format Go code
	cd $(BACKEND) && golangci-lint fmt ./...

.PHONY: fuzz
fuzz: ## Run every fuzz target for FUZZTIME each
	cd $(BACKEND) && for pkg in $$(go list ./...); do \
	  for target in $$(go test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
	    echo "== $$pkg $$target"; go test $$pkg -run '^$$' -fuzz "^$$target$$" -fuzztime $(FUZZTIME) || exit 1; \
	  done; \
	done

.PHONY: bench
bench: ## Micro-benchmarks for parsers, encoder and generator
	cd $(BACKEND) && go test -run '^$$' -bench . -benchmem ./internal/parser/... ./internal/storage/... ./internal/loggen/...

.PHONY: vl-up
vl-up: ## Start a throwaway VictoriaLogs for integration tests (port 19428)
	@docker inspect $(VL_TEST_CONTAINER) >/dev/null 2>&1 || \
	  docker run -d --rm --name $(VL_TEST_CONTAINER) -p 127.0.0.1:19428:9428 $(VL_IMAGE) -retentionPeriod=30d >/dev/null
	@for i in $$(seq 1 30); do curl -fsS $(TEST_VICTORIALOGS_URL)/health >/dev/null 2>&1 && exit 0; sleep 1; done; \
	  echo "VictoriaLogs did not become healthy" >&2; exit 1

.PHONY: vl-down
vl-down: ## Stop the integration-test VictoriaLogs
	-docker stop $(VL_TEST_CONTAINER) >/dev/null 2>&1

.PHONY: integration
integration: vl-up ## Integration tests against a real VictoriaLogs
	cd $(BACKEND) && TEST_VICTORIALOGS_URL=$(TEST_VICTORIALOGS_URL) go test -tags integration -race -count=1 ./tests/integration/

.PHONY: docker
docker: ## Build the syslogc and loggen images
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --target syslogc -t ghcr.io/freezxp/syslogc:$(VERSION) .
	docker build --target loggen -t ghcr.io/freezxp/syslogc-loggen:$(VERSION) .

.PHONY: up
up: ## docker compose up -d --build
	docker compose up -d --build

.PHONY: down
down: ## docker compose down
	docker compose down

.PHONY: e2e
e2e: ## Phase 1 end-to-end smoke test against the compose stack
	$(BACKEND)/tests/e2e/phase1-smoke.sh

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN)
