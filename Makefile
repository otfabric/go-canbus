# HELP
# This will output the help for each task
# thanks to https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html

.DEFAULT_GOAL := help

.PHONY: chaos e2e microvm interop-images help all build examples fmt fmt-check lint lint-ci vet vuln test test-vcan test-admin coverage coverage-check cover bench fuzz check clean generate generate-check coverpkg-check cross interop vcan-setup coverage-pkg-check

help: ## This help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# The parent otfabric/go.work does not list this module.
export GOWORK := off

# Format with the gofmt of the Go version in go.mod, as CI does.
GO_MOD_VERSION := $(shell sed -nE 's/^go ([0-9]+\.[0-9]+).*/\1/p' go.mod | head -n1)
GOFMT_CI = $(shell GOTOOLCHAIN=go$(GO_MOD_VERSION).0 go env GOROOT)/bin/gofmt

BIN_DIR := bin
TEST_PKGS := $(shell go list ./... | grep -v '/examples')
# Packages whose coverage counts. Excluded, by policy (TESTING.md): the
# examples, the generator of the golden files (internal/cmd), the
# generated golden code (internal/golden, verified by the
# differential test) and the leak-check test helper.
COVER_PKGS := $(shell go list ./... | grep -v '/examples' | grep -v '/internal/cmd' | grep -v '/internal/golden' | grep -v '/internal/leak' | grep -v '/e2e' | paste -sd, -)
COVER_MIN := 90
# Per-package floor (TEST-012), checked by coverage-pkg-check. Measured with
# the coverage of the CI job that has the prerequisites; see TESTING.md.
COVER_PKG_MIN := 100

# vcan interface the SocketCAN and kernel ISO-TP tests use (Linux).
CANBUS_TEST_IFACE ?= vcan0
export CANBUS_TEST_IFACE

all: build ## Build the examples

build: ## Build the examples into ./bin
	@mkdir -p $(BIN_DIR)
	@for dir in examples/*/; do \
		name="$$(basename "$$dir")"; \
		go build -o "$(BIN_DIR)/example-$$name" "./$$dir"; \
	done

RUN_EXAMPLES := virtual-bus dbc-decode dbc-generate isotp-exchange capture-replay
examples: ## Run the self-contained examples (no hardware; each must exit successfully)
	@for e in $(RUN_EXAMPLES); do \
		echo "Running example $$e"; \
		go run ./examples/$$e >/dev/null || { echo "example $$e failed"; exit 1; }; \
	done

fmt: ## Format with the gofmt of the go.mod Go version
	@$(GOFMT_CI) -w .

fmt-check: ## Fail if gofmt (go.mod version or local) would change a file
	@ci="$$($(GOFMT_CI) -l .)"; loc="$$(gofmt -l .)"; \
	if [ -n "$$ci$$loc" ]; then echo "Not formatted:"; echo "$$ci $$loc"; exit 1; fi

lint: ## Run staticcheck (all platforms of the SocketCAN code)
	@staticcheck ./...
	@GOOS=linux staticcheck ./...

lint-ci: ## Run golangci-lint
	@golangci-lint run ./...

vet: ## Run go vet for this platform, Linux (amd64, arm) and Windows
	@go vet ./...
	@GOOS=linux go vet ./...
	@GOOS=linux GOARCH=arm go vet ./...
	@GOOS=windows go vet ./...

vuln: ## Run govulncheck
	@govulncheck ./...

test: ## Run the tests with the race detector (SocketCAN tests skip without vcan0)
	@go test -count=1 -race $(TEST_PKGS)

# Packages share vcan0/vcan1: on Linux they run one at a time (-p 1), or
# one package's traffic disturbs another's (a chaos bridge forwards it all).
test-vcan: ## Run the tests on Linux, failing instead of skipping when vcan0 is missing
	@CANBUS_REQUIRE_VCAN=1 go test -p 1 -count=1 -race $(TEST_PKGS)

test-admin: ## Run the netlink administration and tcpdump capture tests (Linux, needs sudo)
	@go test -c -o $(BIN_DIR)/link.test ./socketcan/link
	@sudo CANBUS_TEST_ADMIN=1 $(BIN_DIR)/link.test -test.v -test.run TestAdmin
	@go test -c -o $(BIN_DIR)/slcan.test ./slcan
	@cd slcan && sudo CANBUS_TEST_ADMIN=1 $(CURDIR)/$(BIN_DIR)/slcan.test -test.v -test.run TestKernelSlcan
	@go test -c -o $(BIN_DIR)/e2e.test ./e2e
	@cd e2e && sudo CANBUS_TEST_ADMIN=1 CANBUS_REQUIRE_XL=$(CANBUS_REQUIRE_XL) CANBUS_REQUIRE_TSHARK=$(CANBUS_REQUIRE_TSHARK) $(CURDIR)/$(BIN_DIR)/e2e.test -test.v -test.run TestTcpdumpCapturesXL

vcan-setup: ## Create vcan0 and vcan1 (CAN FD MTU) and vcanxl0 (CAN XL MTU, Linux ≥ 6.2); load the CAN modules (Linux, sudo)
	@sudo modprobe vcan; sudo modprobe can-isotp || true
	@for i in vcan0 vcan1; do ip link show $$i >/dev/null 2>&1 || sudo ip link add $$i type vcan; sudo ip link set $$i mtu 72 up; done
	@ip link show vcanxl0 >/dev/null 2>&1 || sudo ip link add vcanxl0 type vcan; sudo ip link set vcanxl0 mtu 2060 up || echo "no CAN XL (Linux < 6.2): CAN XL tests skip"

KERNEL ?= v6.12
microvm: ## Run the SocketCAN tests in a microVM with a mainline kernel (Linux with KVM, virtme-ng; KERNEL=v6.12)
	@scripts/mainline-kernel.sh $(KERNEL) $(BIN_DIR)/kernel-$(KERNEL)
	@sudo depmod -b $(BIN_DIR)/kernel-$(KERNEL)/root $$(ls $(BIN_DIR)/kernel-$(KERNEL)/root/lib/modules)
	@scripts/microvm-test.sh $(BIN_DIR)/kernel-$(KERNEL)/root/boot/vmlinuz-$$(ls $(BIN_DIR)/kernel-$(KERNEL)/root/lib/modules) $(CURDIR)/$(BIN_DIR)/kernel-$(KERNEL)/root

CHAOS_ITERATIONS ?= 200
chaos: ## Run the chaos-monkey suites with CHAOS_ITERATIONS seeds each (Linux with vcan0/vcan1 adds the kernel ISO-TP one)
	@CANBUS_CHAOS_ITERATIONS=$(CHAOS_ITERATIONS) go test -count=1 -timeout=60m -run Monkey -v ./chaos | grep -E '^(---|ok|FAIL|PASS)'

e2e: ## Run the end-to-end suite (on Linux with vcan0 also over SocketCAN and the kernel)
	@go test -count=1 -race ./e2e

coverage: ## Run the tests with coverage across the library packages (writes coverage.out)
	@go test -p 1 -count=1 -race -covermode=atomic -coverpkg=$(COVER_PKGS) -coverprofile=coverage.out $(TEST_PKGS)
	@go tool cover -func=coverage.out | tail -1

coverage-check: coverage ## Fail when total coverage is below COVER_MIN (meaningful on Linux with vcan0)
	@total=$$(go tool cover -func=coverage.out | tail -1 | awk '{print $$3}' | tr -d %); \
	echo "total $$total% (minimum $(COVER_MIN)%)"; \
	awk -v t=$$total -v m=$(COVER_MIN) 'BEGIN { exit (t+0 < m+0) }'

coverage-pkg-check: coverage ## Fail when a package is below COVER_PKG_MIN (reads coverage.out)
	@awk -v m="$(COVER_PKG_MIN)" 'NR > 1 { n[$$1] = $$2; if ($$3 > 0) h[$$1] = 1 } \
	END { for (k in n) { p = k; sub(/\/[^\/]*$$/, "", p); s[p] += n[k]; if (k in h) c[p] += n[k] } \
	for (p in s) { v = 100 * c[p] / s[p]; printf "%s %.1f%%\n", p, v; if (v < m) { printf "  below COVER_PKG_MIN: %s: %.1f%% < %s%%\n", p, v, m; bad = 1 } } exit bad }' coverage.out

cover: coverage ## Open the coverage report in a browser
	@go tool cover -html=coverage.out

bench: ## Run the benchmarks
	@go test -count=1 -run='^$$' -bench=. -benchmem ./ ./candb/bits ./socketcan ./capture

FUZZTIME ?= 30s
fuzz: ## Run every fuzz target for FUZZTIME (default 30s)
	@go test -run='^$$' -fuzz='^FuzzFilter$$' -fuzztime=$(FUZZTIME) .
	@go test -run='^$$' -fuzz='^FuzzParseXLFrame$$' -fuzztime=$(FUZZTIME) .
	@go test -run='^$$' -fuzz='^FuzzUnmarshal$$' -fuzztime=$(FUZZTIME) ./socketcan
	@go test -run='^$$' -fuzz='^FuzzParseLink$$' -fuzztime=$(FUZZTIME) ./socketcan/link
	@go test -run='^$$' -fuzz='^FuzzParseFrame$$' -fuzztime=$(FUZZTIME) ./capture
	@go test -run='^$$' -fuzz='^FuzzReaders$$' -fuzztime=$(FUZZTIME) ./capture
	@go test -run='^$$' -fuzz='^FuzzParseFrame$$' -fuzztime=$(FUZZTIME) ./slcan
	@go test -run='^$$' -fuzz='^FuzzParseStatus$$' -fuzztime=$(FUZZTIME) ./slcan
	@go test -run='^$$' -fuzz='^FuzzParse$$' -fuzztime=$(FUZZTIME) ./dbc
	@go test -run='^$$' -fuzz='^FuzzParse$$' -fuzztime=$(FUZZTIME) ./kcd
	@go test -run='^$$' -fuzz='^FuzzParse$$' -fuzztime=$(FUZZTIME) ./arxml
	@go test -run='^$$' -fuzz='^FuzzReceiver$$' -fuzztime=$(FUZZTIME) ./isotp
	@go test -run='^$$' -fuzz='^FuzzCodec$$' -fuzztime=$(FUZZTIME) ./candb

generate: ## Regenerate the code generator's golden output
	@go generate ./codegen

generate-check: ## Fail when the golden output differs from what the generator produces
	@go run ./internal/cmd/gengolden -check

coverpkg-check: ## Fail when CI's coverpkg (.github/workflows/ci.yml) is not COVER_PKGS
	@grep -qF -- "$(COVER_PKGS)" .github/workflows/ci.yml || { echo "coverpkg in .github/workflows/ci.yml must be: $(COVER_PKGS)"; exit 1; }

cross: ## Build every package for the release platforms
	@for t in linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
		echo "build $$t"; GOOS=$${t%/*} GOARCH=$${t#*/} go build ./... || exit 1; done

# Reference implementations: otfabric/canbus-interop v0.1.0, pinned by digest
# in interop/harness_test.go (see INTEROPERABILITY.md). interop-images
# builds canbus-interop's source at INTEROP_REF as :dev images, to test
# unreleased adapters (CANBUS_INTEROP_<ADAPTER>_IMAGE=...:dev).
INTEROP_REF ?= main
interop-images: ## Build the canbus-interop images from source as :dev (needs Docker)
	@rm -rf $(BIN_DIR)/canbus-interop && mkdir -p $(BIN_DIR)
	@git clone -q --depth 1 --branch $(INTEROP_REF) https://github.com/otfabric/canbus-interop.git $(BIN_DIR)/canbus-interop
	@$(MAKE) -C $(BIN_DIR)/canbus-interop images REGISTRY=ghcr.io/otfabric VERSION=dev

interop: ## Run the interop suite against the canbus-interop images (Linux, Docker, vcan0; writes bin/interop-report.json)
	@mkdir -p $(BIN_DIR)
	@CANBUS_REQUIRE_VCAN=1 CANBUS_REQUIRE_INTEROP=1 CANBUS_INTEROP_REPORT=$(CURDIR)/$(BIN_DIR)/interop-report.json \
		go test -tags=interop -count=1 -timeout=60m ./interop/...

check: generate-check fmt-check lint lint-ci vet vuln test coverage-check coverage-pkg-check examples cross ## Everything CI checks except the vcan and interop jobs

clean: ## Remove binaries and coverage files
	@rm -rf $(BIN_DIR) coverage.out coverage.html
