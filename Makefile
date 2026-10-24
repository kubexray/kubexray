# Tool versions are pinned here; Dependabot does not track these, bump them deliberately.
GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.9.0
GITLEAKS_VERSION      := v8.30.1
ACTIONLINT_VERSION    := v1.7.12
TYGO_VERSION          := v0.2.21

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
build: ## Build the kubexray binary with the web UI into ./bin
	$(MAKE) web
	$(MAKE) go-build

.PHONY: go-build
go-build: ## Build the binary without rebuilding the UI (no Node needed)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/kubexray ./cmd/kubexray

.PHONY: web
web: ## Build the web UI into internal/web/dist (needs Node)
	cd web && npm ci --no-fund --no-audit && npm run build

.PHONY: web-check
web-check: ## Type-check, lint and test the web UI
	cd web && npm ci --no-fund --no-audit && npm run typecheck && npm run lint && npm test

.PHONY: test-envtest
# The API server version follows client-go (v0.37.1 → 1.37.x) and the tool
# follows controller-runtime, so dependency updates carry envtest along.
ENVTEST_K8S = $(shell go list -m -f '{{.Version}}' k8s.io/client-go | sed -E 's/^v0\.([0-9]+)\..*/1.\1.x/')
ENVTEST_TOOL = $(shell go list -m -f '{{.Version}}' sigs.k8s.io/controller-runtime)
test-envtest: ## Run the watcher against a real API server (downloads etcd and kube-apiserver)
	KUBEBUILDER_ASSETS="$$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_TOOL) use $(ENVTEST_K8S) -p path)" \
		go test -tags envtest -run AgainstAnAPIServer -count=1 ./internal/cluster/

.PHONY: e2e-ui
e2e-ui: ## Run the UI end to end in a browser (Playwright, on the public player)
	cd web && npm ci --no-fund --no-audit && npx playwright install chromium && npm run e2e

.PHONY: gen
gen: ## Generate the UI's TypeScript types from Go
	go run github.com/gzuidhof/tygo@$(TYGO_VERSION) generate

.PHONY: gen-check
gen-check: gen ## Fail if the generated TypeScript types are out of date
	git diff --exit-code -- web/src/gen || (echo "Generated types are stale: run make gen and commit." && exit 1)

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

.PHONY: perf
perf: ## Open the UI on a simulated 1,000-pod cluster with constant churn
	$(MAKE) web
	go run ./hack/loadgen

.PHONY: bench
bench: ## Run the Go benchmarks (1,000-pod cluster)
	go test -run '^$$' -bench . -benchmem ./internal/...

DEMO_KUBECONFIG := .demo/kubeconfig

.PHONY: demo
demo: build ## Run the demo: a kind cluster with the shop app, and kubexray on it
	scripts/demo.sh up
	./bin/kubexray --kubeconfig $(DEMO_KUBECONFIG)

.PHONY: break
break: ## Break the demo (SCENARIO=config, bad-image, node, oom, bad-probe, unschedulable, bad-config, scale-zero or quota)
	scripts/demo.sh break

.PHONY: fix
fix: ## Undo make break
	scripts/demo.sh fix

.PHONY: e2e
e2e: go-build ## Run every break-it scenario on the demo cluster and check kubexray's answers
	scripts/demo.sh up
	scripts/e2e.sh

.PHONY: e2e-helm
e2e-helm: ## Install the Helm chart on the demo cluster and check auth, RBAC and persistence
	scripts/demo.sh up
	scripts/e2e-helm.sh

.PHONY: demo-down
demo-down: ## Delete the demo cluster
	scripts/demo.sh down

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin coverage.out
