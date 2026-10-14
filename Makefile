GOLANGCI_LINT_MODULE := github.com/golangci/golangci-lint/v2/cmd/golangci-lint
GOVULNCHECK_MODULE := golang.org/x/vuln/cmd/govulncheck
GO_LICENSES_MODULE := github.com/google/go-licenses/v2
QUALITY_TOOLS_DIRECTORY := tools/quality
QUALITY_TOOL := go tool -modfile="$(QUALITY_TOOLS_DIRECTORY)/go.mod"
GOLANGCI_LINT := $(QUALITY_TOOL) golangci-lint
GOVULNCHECK := $(QUALITY_TOOL) govulncheck
GO_LICENSES := $(QUALITY_TOOL) go-licenses
QUALITY_GOVULNCHECK := go -C $(QUALITY_TOOLS_DIRECTORY) tool govulncheck
QUALITY_GO_LICENSES := go -C $(QUALITY_TOOLS_DIRECTORY) tool go-licenses
ACTIONLINT := go -C tools/actionlint tool actionlint

TOOL_PACKAGES := \
	$(GOLANGCI_LINT_MODULE) \
	$(GOVULNCHECK_MODULE) \
	$(GO_LICENSES_MODULE)

RUNTIME_ALLOWED_LICENSES := Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MIT
DEVELOPMENT_ALLOWED_LICENSES := $(RUNTIME_ALLOWED_LICENSES),MPL-2.0

# These reviewed packages are reachable only from development tools and are not
# distributed with the control-plane binary or image.
TOOL_LICENSE_EXCEPTIONS := \
	--ignore github.com/OpenPeeDeeP/depguard/v2 \
	--ignore github.com/alecthomas/chroma/v2 \
	--ignore github.com/ashanbrown/forbidigo/v2 \
	--ignore github.com/ashanbrown/makezero/v2 \
	--ignore github.com/denis-tingaikin/go-header \
	--ignore github.com/firefart/nonamedreturns \
	--ignore github.com/golangci/gofmt \
	--ignore github.com/golangci/golangci-lint/v2 \
	--ignore github.com/ldez/structtags \
	--ignore github.com/leonklingele/grouper \
	--ignore github.com/xen0n/gosmopolitan

IMAGE ?= perfeng-control-plane:local-check
VCS_REF ?= unknown

.PHONY: help bootstrap doctor build binaries fmt fmt-check docs-check architecture-check check test verify race fuzz vuln license supply-chain validate db-validate kubernetes-validate image

help:
	@echo perfeng-control-plane engineering command surface
	@echo "  make bootstrap  Resolve all pinned repository dependencies"
	@echo "  make doctor  Report the effective local development toolchain"
	@echo "  make build  Compile all commands without repository artifacts"
	@echo "  make binaries  Build runnable commands under bin"
	@echo "  make fmt  Format Go sources and imports"
	@echo "  make fmt-check  Verify formatting without changes"
	@echo "  make docs-check  Verify documentation and contribution policy"
	@echo "  make architecture-check  Verify exhaustive production dependency policy"
	@echo "  make check  Run deterministic static and module checks"
	@echo "  make test  Run ordinary tests without cached results"
	@echo "  make verify  Run the fast network-independent development loop"
	@echo "  make race  Run the complete race-enabled test suite"
	@echo "  make fuzz  Run the bounded JSON parser fuzz target"
	@echo "  make supply-chain  Run vulnerability and license checks"
	@echo "  make validate  Run the complete non-live acceptance suite"
	@echo "  make db-validate  Run PostgreSQL integration tests (URL required)"
	@echo "  make kubernetes-validate  Run live Kubernetes API tests (kubeconfig required)"
	@echo "  make image  Build and verify IMAGE (default: perfeng-control-plane:local-check)"

bootstrap:
	go mod download
	go -C $(QUALITY_TOOLS_DIRECTORY) mod download
	go -C tools/actionlint mod download

doctor:
	@git --version
	@go version
	@go env -json GOVERSION GOOS GOARCH GOTOOLCHAIN CGO_ENABLED
	@$(MAKE) --version

build:
	go build ./cmd/...

binaries:
	go build -o bin/ ./cmd/...

fmt:
	$(GOLANGCI_LINT) fmt

fmt-check:
	$(GOLANGCI_LINT) fmt --diff

docs-check:
	go run ./tools/repo-check

architecture-check:
	go test -vet=off -count=1 ./internal/architecture

check: fmt-check docs-check architecture-check
	$(GOLANGCI_LINT) config verify
	$(GOLANGCI_LINT) run ./...
	$(ACTIONLINT)
	go mod tidy -diff
	go mod verify
	go -C $(QUALITY_TOOLS_DIRECTORY) mod tidy -diff
	go -C $(QUALITY_TOOLS_DIRECTORY) mod verify
	go -C tools/actionlint mod tidy -diff
	go -C tools/actionlint mod verify

test:
	go test -vet=off -count=1 ./...

verify: build check test

race:
	go test -vet=off -race -count=1 ./...

fuzz:
	go test -vet=off ./internal/httpapi -run=^$$ -fuzz=FuzzParseJSON -fuzztime=10s

vuln:
	$(GOVULNCHECK) ./...
	$(QUALITY_GOVULNCHECK) $(TOOL_PACKAGES)
	go -C tools/actionlint tool govulncheck github.com/rhysd/actionlint/cmd/actionlint

license:
	$(GO_LICENSES) check --include_tests ./... --allowed_licenses $(RUNTIME_ALLOWED_LICENSES)
	$(QUALITY_GO_LICENSES) check $(TOOL_PACKAGES) --allowed_licenses $(DEVELOPMENT_ALLOWED_LICENSES) $(TOOL_LICENSE_EXCEPTIONS)
	go -C tools/actionlint tool go-licenses check github.com/rhysd/actionlint/cmd/actionlint --allowed_licenses $(RUNTIME_ALLOWED_LICENSES)

supply-chain: vuln license

validate: verify race fuzz supply-chain

db-validate:
	$(if $(PERFENG_TEST_DATABASE_URL),,$(error PERFENG_TEST_DATABASE_URL must name an authorized loopback disposable PostgreSQL database))
	go test -vet=off -race -count=1 -timeout=3m -v ./internal/postgres

kubernetes-validate:
	$(if $(PERFENG_TEST_KUBECONFIG),,$(error PERFENG_TEST_KUBECONFIG must name the loopback kind-perfeng-test cluster kubeconfig))
	go test -vet=off -race -count=1 -timeout=3m -run "Test(Dispatcher|Benchmark)AgainstAPIServer" -v ./internal/kubernetes

image:
	docker build --platform linux/amd64 --build-arg VCS_REF=$(VCS_REF) -t $(IMAGE) .
	go run ./cmd/verify-image verify $(IMAGE) $(VCS_REF)
