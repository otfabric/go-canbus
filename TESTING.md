# Testing

## Levels

| Level | Where | Command |
|-------|-------|---------|
| Unit and property tests, race detector | any OS | `make test` |
| SocketCAN, netlink, kernel ISO-TP on vcan | Linux with `vcan0` (MTU ≥ 72) | `make vcan-setup test-vcan`, `make test-admin` (root) |
| slcan against the Linux kernel's slcan driver (a pty with the N_SLCAN line discipline) | Linux, root, `slcan` module | `make test-admin`; the microVMs require it (`CANBUS_REQUIRE_SLCAN=1`). slcan over real ptys with go-serial: `make test` (Linux, macOS) |
| CAN XL on vcan: send, receive, mixed CAN/CAN FD/CAN XL order, echo, VCID pass-through and filter, kernel CAN_RAW_FILTER decisions against `FilterSet.MatchXL`, a `canbus.Bus` on CAN XL sockets | Linux ≥ 6.2 with `vcanxl0` (MTU 2060; VCIDs need ≥ 6.9) | `make vcan-setup test-vcan`; CI and the microVMs on kernels ≥ 6.2 require it (`CANBUS_REQUIRE_XL=1`) |
| pcap/pcapng against Wireshark: every frame kind written by this package dissected by tshark into the same frame; the captures in `capture/testdata` (tcpdump and dumpcap on Linux 7.0) read alike by tshark and this package | any OS with tshark | `make test`; CI installs tshark and requires it (`CANBUS_REQUIRE_TSHARK=1`) |
| pcap against the kernel and libpcap: tcpdump captures CAN, CAN FD and CAN XL frames sent on `vcanxl0`; this package and tshark read the capture | Linux ≥ 6.2, root, tcpdump | `make test-admin`; required in CI |
| Coverage gate (90 %) | Linux with vcan0 | `make coverage-check` |
| Per-package floor (80 %) | Linux with vcan0 | `make coverage-pkg-check` |
| Fuzzing | any OS | `make fuzz FUZZTIME=30s` |
| Golden code generation | any OS | `make generate-check` |
| Interoperability with independent stacks | Linux, Docker, vcan0 | `make interop` (see [INTEROPERABILITY.md](INTEROPERABILITY.md)) |
| SocketCAN tests under another kernel (microVM) | Linux with KVM, virtme-ng | `make microvm KERNEL=v6.12`; CI: the Kernels workflow (Ubuntu generic, mainline 6.1, 6.6, 6.12, 6.18). See docs/MICROVM.md |
| End-to-end scenarios | any OS; on Linux with vcan0 also SocketCAN, kernel ISO-TP, processes | `make e2e` |
| Chaos monkey (random fault schedules, invariants) | any OS; Linux with vcan0 + vcan1 adds kernel ISO-TP through the chaos bridge | `make chaos CHAOS_ITERATIONS=200`; CI: 12/6/10/8 seeds on every pull request, Nightly chaos workflow 500 seeds on amd64 and arm64 |
| Everything above except vcan/interop | any OS | `make check` |

Without CAN hardware, slcan is tested as it runs: `Open` drives the slave
of a pseudo-terminal through go-serial while a Conn on the master plays the
adapter, and with root the kernel's own slcan driver is attached to a pty
and exchanges frames with a SocketCAN socket on the slcanN interface it
creates. python-can's slcan interface is the second, userspace oracle
(interop, over TCP).

Tests that need vcan **skip** without it, so a plain `go test ./...` passes
on macOS and Windows. `CANBUS_REQUIRE_VCAN=1` turns those skips into
failures, and CI sets it: a skipped SocketCAN test is never a pass.
Kernel ISO-TP tests also need the `can-isotp` module; `CANBUS_REQUIRE_ISOTP=1`
makes them required. GitHub's hosted runner kernels (Azure) do not ship the
module, so CI's vcan job builds it from the kernel's source
(`scripts/ci-can-modules.sh`) and sets the variable unconditionally: a module
that cannot be provided fails the job. Elsewhere set it where the module
exists, as in the Lima VM used for development (Ubuntu, kernel 7.0 generic).

## Coverage policy

`make coverage` measures all library packages together (`-coverpkg`), so
code exercised through another package counts. Excluded, and why:

| Excluded | Reason |
|----------|--------|
| `examples/` | Programs, run by `make examples` |
| `internal/cmd/gengolden` | Build tool, its output is checked by `make generate-check` |
| `internal/golden/` | Generated code; verified bit-for-bit against the dynamic codec by `codegen`'s differential test |
| `internal/leakcheck` | Test helper |

Coverage is only meaningful on Linux with vcan0 (otherwise the SocketCAN
code is not run). CI measures it in the `vcan` job and uploads that to
Codecov.

`make coverage-pkg-check` holds every package to `COVER_PKG_MIN` (80 %) and
prints the figures. The floor is below the 90 % total because of
`socketcan/link` (81.1 %) and `socketcan` (83.7 %): their remaining code is
netlink administration, which needs root (`make test-admin`, not part of
the coverage run), and kernel error paths vcan cannot produce.

## What the tests check

- **Frames, DLC, filters**: tables of valid/invalid frames; the software
  filter against the kernel's expression (fuzzed) and against the kernel
  itself on vcan (`TestKernelFilterSemantics`).
- **Bus**: fan-out, filters, the three overflow policies, close and
  cancellation semantics, echo, error-state tracking, metrics, interface
  down/up, transport failures and back-off; a stress test with concurrent
  senders, receivers and subscription churn checking per-sender order and
  loss (`TestBusStress`); goroutine leak checks (`internal/leakcheck`).
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

## Recorded vehicle traffic

[testdata/captures](testdata/captures/README.md) holds CAN logs recorded by
others on twelve real vehicles (evidence class B: origin documented by the
publisher, not independently verified), byte for byte as published: ten
passenger cars at their OBD-II port (CANdid), the ten CAN domains of a 2020
electric vehicle with two workshop testers and CaringCaribou on its
diagnostic bus (CarDS), and a Renault Clio (TU Eindhoven). 16 logs, 2.7
million frames, 131 MB, checked by SHA256SUMS and `manifest.json`. They run
in the ordinary `go test ./...`, offline:

- `capture`: every line decodes to the counts in the manifest; Wireshark's
  own candump reader decodes it to the same frames (`CANBUS_REQUIRE_TSHARK`
  in CI); written back it is the original byte for byte; converted to every
  format and back nothing is lost but what the format cannot carry; the
  CANdid logs' out-of-order first line through BLF and replay; cut and
  corrupted lines; real lines seed `FuzzReaders`.
- `isotp`: on the diagnostic bus of the CarDS recordings, a listen-only
  connection reassembles exactly the messages Wireshark's ISO 15765
  dissector finds (`*.isotp.json`); a connection set up as each of the 21
  recorded testers and ECUs sends their frames byte for byte (requests,
  flow control, single, first and consecutive frames) and receives the
  recorded peer's messages. Recorded frames seed `FuzzReceiver`.

A `-race` build (`make test`, `make coverage`) checks the first 20,000
lines of each log: the codecs run in one goroutine and the whole corpus
under the race detector takes longer than a test binary's 10 minutes. The
whole corpus runs in the tests without `-race`: `make test-captures` (in
CI's vcan jobs, with Wireshark required) and CI's Test jobs on Linux, macOS
and Windows.

`make captures-extended` fetches the three recordings too large to commit
(about 1.1 GB to download: CarDS fast driving and CaringCaribou, TU/e Opel
Astra) and checks 14.7 million more frames: each recording decodes and is
written back with the SHA-256 the manifest records (the TU/e logs lack a
final newline, which the comparison ignores). `CANBUS_CAPTURES` names
another cache directory; `CANBUS_REQUIRE_CAPTURES=1` fails instead of
skipping. docs/CAPTURE-COVERAGE.md is the matrix.

## End-to-end

`e2e/` combines the packages as an application does: three ECUs built from
generated code send classical, multiplexed and CAN FD messages; a gateway
decodes them with the DBC and records into candump, JSON Lines, pcap, ASC
and BLF at once; ISO-TP diagnostics (1000-byte responses) run alongside;
every recording is replayed onto a new bus and decoded to the same signals.
It runs on the virtual bus everywhere and on vcan with the kernel's ISO-TP
as the server on Linux. Further: two processes (examples/vcan-pingpong) on
vcan; Go generated from DBC, KCD and ARXML compiled in a separate module and
round-tripped.

## Chaos monkey

`chaos/` runs random, seeded fault schedules (`CANBUS_CHAOS_ITERATIONS`
seeds) with invariants:

| Suite | Faults | Invariant |
|-------|--------|-----------|
| `TestMonkeyISOTP` | drop, duplicate, delay (what a bus can do to a sender's frames) | a received message is byte-identical to one that was sent; every call returns by its deadline; transfers succeed again once the faults stop |
| `TestMonkeyKernelISOTP` (Linux) | same, through `chaos.Bridge` between vcan0 (kernel ISO-TP) and vcan1 (go-canbus) | same, both directions |
| `TestMonkeyCorruption` | all, with corruption and reordering | no panic, no hang: every call returns by its deadline |
| `TestMonkeyBus` | bus faults plus subscription churn, interface down/up, error and invalid frames, close under traffic | no deadlock or leak; each sender's frames arrive in order |
| `TestMonkeyReplay` | all, cancelled at random | Replay returns promptly, never sends more than it read |

Finding: a consecutive frame of a failed transfer, reordered behind the next
transfer's first frame, is accepted by any ISO-TP receiver when its sequence
number fits (ISO-TP frames carry no message identity). A single CAN bus does
not reorder a sender's frames, so that fault is checked only for "no panic,
no hang".

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

## When CI runs

| Event | Workflow | What runs |
|---|---|---|
| Pull request | `pr.yml` | `docs.yml` (docsync, otfabric-policy) always; `ci.yml`, `interop.yml`, `kernels.yml` unless every changed file is documentation; then `OT Fabric Required Checks` |
| Push to a branch | none | nothing; dispatch `ci.yml`, `interop.yml`, `kernels.yml` to test a branch without a pull request |
| Push to `main` | `docs.yml` | the light checks; the merged commit was tested as the pull request's merge commit |
| Schedule | `nightly.yml` | the scheduled runs, on `main` |
| Release | `release.yml` | the release workflow tests the release commit again |

Documentation means an added or modified `*.md` file outside `.github/`, `testdata/` and `vendor/`, other than `AGENTS.md` and `CLAUDE.md`. A deleted or renamed document, or any other file, runs everything. `OT Fabric Required Checks` fails unless every job that applies succeeded; its summary lists them.
