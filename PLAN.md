# Plan

Executable plan for the three repositories. Each milestone ends in a
reviewable, tested commit (PR to `main`). Status lives in
[PROGRESS.md](PROGRESS.md).

| # | Milestone | Repo | Done when |
|---|-----------|------|-----------|
| M0 | Conventions study, design docs | go-canbus | ARCHITECTURE/REQUIREMENTS/PLAN committed |
| M1 | Frame, DLC, filters, errors, Bus, virtual bus | go-canbus | unit + race + fuzz tests, benchmarks |
| M2 | SocketCAN raw backend, netlink admin | go-canbus | vcan integration tests in Lima VM and CI (`vcan` job, failing instead of skipping when `CANBUS_REQUIRE_VCAN=1`) |
| M3 | Capture (candump, JSONL, pcap) and replay | go-canbus | round-trip + fuzz tests |
| M4 | candb, DBC parser/writer, KCD, codec | go-canbus | fixtures, property + fuzz tests |
| M5 | Code generator | go-canbus | golden files, generate-check, generated code compiles and round-trips |
| M6 | ISO-TP userspace + kernel adapter | go-canbus | virtual-bus tests, kernel vs userspace on vcan |
| M7 | Interop container contract, adapters (can-utils, python-can, socketcan-rs, scapy, python-can-isotp, cantools, canmatrix), self-test | canbus-interop | images build natively on amd64/arm64, reference-to-reference self-test on vcan |
| M8 | go-canbus interop suite against the published images | go-canbus | `make interop` green in CI on vcan |
| M9 | canbusctl | canbusctl | all command groups, tests, cross-builds |
| M10 | Docs, CI, release prep, otfabric.io integration proposal | all | docs complete, CI green, release notes ready |

Test environments:

* macOS dev host: unit tests, virtual bus, codecs, DBC oracles (Docker).
* Lima VM `canvm` (Ubuntu, kernel 7.0, `vcan`, `vxcan`, `can-isotp`,
  `can-gw`): SocketCAN, kernel ISO-TP and interop suites.
* GitHub Actions `ubuntu-24.04` / `ubuntu-24.04-arm`: load
  `linux-modules-extra-$(uname -r)` and the CAN modules.
