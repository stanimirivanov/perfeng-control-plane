LINTER ?= golangci-lint

.PHONY: help fmt fmt-check check test race fuzz vuln validate

help:
	@echo perfeng-control-plane engineering command surface
	@echo "  make fmt        Format Go sources and imports"
	@echo "  make fmt-check  Verify formatting without changes"
	@echo "  make check      Verify linter configuration, report findings and modules"
	@echo "  make test       Run ordinary tests"
	@echo "  make race       Run the complete race-enabled test suite"
	@echo "  make fuzz       Run the bounded JSON parser fuzz target"
	@echo "  make vuln       Scan reachable vulnerabilities"
	@echo "  make validate   Run all non-mutating acceptance checks"

fmt:
	$(LINTER) fmt

fmt-check:
	$(LINTER) fmt --diff

check: fmt-check
	$(LINTER) config verify
	$(LINTER) run ./...
	go mod verify

test:
	go test -vet=off ./...

race:
	go test -vet=off -race -count=1 ./...

fuzz:
	go test -vet=off ./internal/httpapi -run=^$$ -fuzz=FuzzParseJSON -fuzztime=10s

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

validate: check race fuzz vuln
