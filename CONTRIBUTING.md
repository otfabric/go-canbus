# Contributing to go-canbus

Thank you for your interest in contributing.

## Getting started

```sh
git clone git@github.com:<your-username>/go-canbus.git
cd go-canbus
git checkout -b feature/my-change
```

## Prerequisites

- Go 1.23+ (see [go.mod](go.mod)); the code must build and pass on Go 1.23
- [staticcheck](https://staticcheck.dev/), [golangci-lint](https://golangci-lint.run/), [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)
- For the SocketCAN tests: Linux with the `vcan` (and `can-isotp`) modules: `make vcan-setup`. On macOS, a Linux VM (for example [Lima](https://lima-vm.io/) with Ubuntu) works; Docker Desktop's kernel has no CAN support.
- Docker, for the interop suite only

## Build and test

```sh
make check          # generated code, format, lint, vet, vuln, race tests, coverage, examples, cross builds
make test-vcan      # Linux: SocketCAN tests must run, not skip
make test-admin     # Linux, sudo: netlink administration
make fuzz           # every fuzz target, FUZZTIME=30s each
make interop        # Linux + Docker + vcan0: independent implementations
```

## Rules

- No cgo and no third-party runtime dependencies.
- SPDX header `// SPDX-License-Identifier: MIT` on every Go file.
- Every behaviour change comes with a test; parsers of untrusted input get
  a fuzz target.
- A compatibility claim needs an automated test against an independent
  implementation (see INTEROPERABILITY.md). Do not write skipped or
  simulated results as passes.
- Keep ARCHITECTURE.md, REQUIREMENTS.md and the docs in step with the code.
- Changes to the code generator need `make generate` and a reviewed diff
  of `internal/golden`.

## Commit and pull request

Small, focused commits with a message that says what and why. Open a pull
request against `main`; CI must be green, including the vcan jobs.
