# AGENTS.md — go-canbus

## What this is

Pure Go CAN library: classical CAN, CAN FD and CAN XL frames, SocketCAN, slcan, a virtual bus, ISO-TP, capture and replay, message databases and a code generator. No cgo. Profile: **protocol-library**. No ISO or CiA conformance claim and no certification (LIMITATIONS.md).

## Organisation standards

This repository follows **otfabric/engineering policy 1.0**, profile **protocol-library** (`.otfabric/policy.yaml`, mode `baseline`). Where this file and the organisation standards disagree, this file wins here — say so in your report.

## Where the truth lives

| Question | Source |
|---|---|
| Requirements | REQUIREMENTS.md |
| What is supported and verified | INTEROPERABILITY.md, LIMITATIONS.md, ROADMAP.md |
| How to test | TESTING.md, docs/MICROVM.md |
| Errors | ERRORS.md |
| Release history | RELEASE.md (`## vX.Y.Z` … `---`) |

## Hard rules

1. Never lower `COVER_MIN` (90), add lint suppressions, or skip or delete tests to make CI pass.
2. Never invent protocol semantics: ISO 11898-1 and ISO 15765-2 are not public, DBC has no public specification; name the public source or implementation.
3. A compatibility claim needs an automated test against an independent implementation. Do not write skipped or simulated results as passes.
4. No cgo and no third-party runtime dependencies beyond those in go.mod.
5. The coverage gate is meaningful only on Linux with vcan: it runs in CI's vcan job, not in the shared go-ci job, which has no CAN interface.
6. A generator change needs `make generate` and a reviewed diff of `internal/golden`.

## Commands

| When you change… | Run |
|---|---|
| anything | `make check` |
| SocketCAN, netlink, kernel ISO-TP | `make vcan-setup test-vcan`; `make test-admin` (sudo). CI sets `CANBUS_REQUIRE_VCAN`, `CANBUS_REQUIRE_ISOTP`, `CANBUS_REQUIRE_XL`, `CANBUS_REQUIRE_TSHARK` |
| the end-to-end paths | `make e2e chaos` |
| a parser | `make fuzz` (a new parser needs a fuzz target) |
| interop-relevant behaviour | `make interop-images interop` (Linux, Docker, vcan0) |

## Definition of Done

`make check` green, and `make test-vcan` where SocketCAN is touched; tests for new behaviour including error paths; ARCHITECTURE.md, REQUIREMENTS.md and the docs in step with the code; RELEASE.md section for user-visible changes; failing or skipped steps reported.

## Git and releases

Branches and PRs. Do not push to `main`, tag or run the Release workflow unless the maintainer asks in the session.
