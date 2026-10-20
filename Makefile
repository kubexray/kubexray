# Tool versions are pinned here; Dependabot does not track these, bump them deliberately.
GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.9.0
GITLEAKS_VERSION      := v8.30.1
ACTIONLINT_VERSION    := v1.7.12

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w \
	-X github.com/kubexray/kubexray/internal/version.Version=$(VERSION) \
	-X github.com/kubexray/kubexray/internal/version.Commit=$(COMMIT)

IMAGE ?= ghcr.io/kubexray/kubexray:$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the kubexray binary into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/kubexray ./cmd/kubexray

.PHONY: test
test: ## Run tests with the race detector
	go test -race -coverprofile=coverage.out ./...

.PHONY: lint
lint: ## Run golangci-lint
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

.PHONY: fmt
fmt: ## Format code (gofumpt + goimports)
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) fmt

.PHONY: vuln
vuln: ## Scan dependencies and stdlib for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: secrets
secrets: ## Scan git history for leaked secrets
	go run github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION) git --redact --no-banner

.PHONY: actionlint
actionlint: ## Lint GitHub Actions workflows
	go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

.PHONY: check
check: lint test vuln secrets actionlint ## Run every check CI runs

.PHONY: docker
docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE) .

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin coverage.out
