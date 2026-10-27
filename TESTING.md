# Testing

## Levels

| Level | Where | Command |
|-------|-------|---------|
| Unit and property tests, race detector | any OS | `make test` |
| SocketCAN, netlink, kernel ISO-TP on vcan | Linux with `vcan0` (MTU ≥ 72) | `make vcan-setup test-vcan`, `make test-admin` (root) |
| Coverage gate (90 %) | Linux with vcan0 | `make coverage-check` |
| Fuzzing | any OS | `make fuzz FUZZTIME=30s` |
| Golden code generation | any OS | `make generate-check` |
| Interoperability with independent stacks | Linux, Docker, vcan0 | `make interop` (see [INTEROPERABILITY.md](INTEROPERABILITY.md)) |
| SocketCAN tests under another kernel (microVM) | Linux with KVM, virtme-ng | `make microvm KERNEL=v6.12`; CI: the Kernels workflow (Ubuntu generic, mainline 6.1, 6.6, 6.12, 6.18). See docs/MICROVM.md |
| Everything above except vcan/interop | any OS | `make check` |

Tests that need vcan **skip** without it, so a plain `go test ./...` passes
on macOS and Windows. `CANBUS_REQUIRE_VCAN=1` turns those skips into
failures, and CI sets it: a skipped SocketCAN test is never a pass.
Kernel ISO-TP tests also need the `can-isotp` module; GitHub's hosted
runner kernels (Azure) do not ship it, so there they skip with a CI
warning, and `CANBUS_REQUIRE_ISOTP=1` makes them required (set it where
the module exists, as in the Lima VM used for development: Ubuntu, kernel
7.0 generic).

## Coverage policy

`make coverage` measures all library packages together (`-coverpkg`), so
code exercised through another package counts. Excluded, and why:

| Excluded | Reason |
|----------|--------|
| `examples/` | Programs, run by `make examples` |
| `internal/cmd/gengolden` | Build tool, its output is checked by `make generate-check` |
| `internal/golden/` | Generated code; verified bit-for-bit against the dynamic codec by `codegen`'s differential test |
| `internal/leak` | Test helper |

Coverage is only meaningful on Linux with vcan0 (otherwise the SocketCAN
code is not run). CI measures it in the `vcan` job and uploads that to
Codecov.

## What the tests check

- **Frames, DLC, filters**: tables of valid/invalid frames; the software
  filter against the kernel's expression (fuzzed) and against the kernel
  itself on vcan (`TestKernelFilterSemantics`).
- **Bus**: fan-out, filters, the three overflow policies, close and
  cancellation semantics, echo, error-state tracking, metrics, interface
  down/up, transport failures and back-off; a stress test with concurrent
  senders, receivers and subscription churn checking per-sender order and
  loss (`TestBusStress`); goroutine leak checks (`internal/leak`).
- **virtual**: SocketCAN delivery rules, FD ports, filters and error masks,
  queue overflow accounting, deterministic seeded faults, delays keeping
  order, close and down semantics, raw injection of invalid frames.
- **socketcan** (vcan): every classical length, every CAN FD length with
  BRS/ESI, remote frames, DLC 9..15, standard and extended IDs, loopback
  and receive-own, kernel filters (join, invert, remote-only), "any",
  cancellation during blocked reads, Close during reads, kernel overflow
  accounting with exact `received + dropped = sent`, zero allocations
  (`BenchmarkSendReceive`).
- **link**: netlink encoding/decoding (fuzzed), interface listing, and as
  root: vcan/vxcan create/delete, MTU, up/down, rejection of bit timing on
  vcan, a blocked receive ending with `ErrInterfaceDown` when the
  interface goes down.
- **isotp**: every addressing mode × sizes 1..10000 both ways, exact wire
  bytes, STmin timing, WAIT/WFTmax, OVERFLOW, N_Bs, N_Cr, wrong sequence,
  interruption, sequence wrap, stray/malformed frames, concurrent senders,
  a fuzzed receiver; kernel ↔ userspace on vcan in 8 modes up to 8000
  bytes, kernel error mapping.
- **capture**: every format round trip, candump and pcap/pcapng variants
  (byte orders, resolutions, old FD captures), replay timing, filters,
  limits, rate, cancellation, routing; fuzzed parsers.
- **candb/dbc/kcd**: a fixture with every supported construct and
  hand-computed bytes, 64-bit exactness, floats, simple and extended
  multiplexing, half-to-even rounding, strict and lax ranges, diagnostics
  for every unsupported construct, error recovery, write → parse
  round trip, CRLF; randomized raw and physical round trips; fuzzed
  parsers (a parsed database must write, re-parse and decode).
- **codegen**: generated code against the dynamic codec on 2000 random
  payloads per message (fields, physical values and encoded bytes),
  generated API errors, identifier collisions, determinism.

## Chaos and fault injection

| Fault | Emulated by | Test |
|-------|-------------|------|
| Dropped / duplicated / delayed / corrupted frames | `virtual.Faults` | `TestFaultsAreDeterministic`, `TestDelayPreservesOrder`, examples/virtual-bus |
| Receiver overflow | small queues; `SO_RCVBUF` on vcan | `TestBusOverflowPolicies`, `TestOverflowAccounting` |
| Interface closure / down | `virtual.Bus.SetUp`, `link.SetDown` | `TestInterfaceDownAndUp`, `TestAdmin` |
| Cancellation under load | contexts in blocked calls | `TestCancelAndClose`, replay tests |
| Invalid sequence numbers, malformed DLC/payload | `virtual.InjectRaw`, raw frames | `TestReceptionErrors`, `FuzzReceiver` |
| Abrupt peer termination | closing ports/connections mid-transfer | `TestFlowControlTimeoutAndClose` |
| Controller error states | `virtual.InjectError` | `TestMetricsAndErrorState` |

Faults below the frame level need hardware: bit errors, stuffing and CRC
errors, arbitration loss, missing acknowledgement, error-passive and
bus-off transitions of a real controller, bit-timing mismatches. The
virtual bus can only *report* them (as error frames); it does not produce
them. vcan delivers every frame and never raises error frames.
