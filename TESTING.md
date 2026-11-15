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
| Fuzzing: every fuzz target of every package, discovered with `go test -list '^Fuzz'` (`scripts/fuzz.sh`), never listed | any OS | `make fuzz FUZZTIME=30s`; CI: 10 s a target on every pull request, the Nightly workflow 5 minutes a target |
| Tests where `int` has 32 bits (the codecs, parsers and trace readers convert file and wire integers to `int`) | Linux amd64, or any host that runs 386 binaries | `GOARCH=386 go test -count=1 ./...`; CI: the Nightly workflow. SocketCAN is not built for 386 |
| Golden code generation | any OS | `make generate-check` |
| Interoperability with independent stacks | Linux, Docker, vcan0 | `make interop` (see [INTEROPERABILITY.md](INTEROPERABILITY.md)) |
| SocketCAN tests under another kernel (microVM) | Linux with KVM, virtme-ng | `make microvm KERNEL=v6.12`; CI: the Kernels workflow (Ubuntu generic, mainline 6.1, 6.6, 6.12, 6.18). See docs/MICROVM.md |
| End-to-end scenarios | any OS; on Linux with vcan0 also SocketCAN, kernel ISO-TP, processes | `make e2e` |
| Chaos monkey (random fault schedules, invariants) | any OS; Linux with vcan0 + vcan1 adds kernel ISO-TP through the chaos bridge | `make chaos CHAOS_ITERATIONS=200`; CI: 12/6/10/8 seeds on every pull request, the Nightly workflow 500 seeds on amd64 and arm64 |
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
module, so the jobs that run those tests (CI's vcan job, the Interop
workflow and the chaos job of the Nightly workflow) build it from the
kernel's source
(`scripts/ci-can-modules.sh`) and set the variable unconditionally: a module
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
  accounting with exact `received + dropped = sent`, and send and receive
  without allocation for a context that cannot be cancelled
  (`BenchmarkSendReceive`; a context that can be cancelled, and so the
  reader of a `canbus.Bus`, registers the cancellation for every call:
  `BenchmarkSendReceiveCancellable`, `BenchmarkBusSendReceive`).
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

## Lifecycle contract matrix

The long-lived types and the rows L1 to L16 of otfabric/engineering
`standards/lifecycle.md`. A cell names the test, or says why the row does
not apply. The tests of `canbus.Bus`, `Subscription`, `virtual`, `isotp`
and `slcan` (on a `net.Pipe`) run in a `testing/synctest` bubble: the test
goes on when the call it wants blocked is known to wait (`synctest.Wait`),
deadlines take fake time, and a goroutine left behind fails the test.
`socketcan` uses real sockets, which do not fit a bubble: its tests need
`vcan0` (and `can-isotp` for `ISOTPConn`), except `TestSendTurn`.
`chaos.Bridge` runs for the life of a context and the capture writers only
flush on `Close`: neither is long-lived in the sense of the matrix.

| Row | `canbus.Bus`, `Subscription` | `virtual.Bus`, `Port` | `slcan.Conn` | `isotp.Conn` | `socketcan.Conn` | `socketcan.ISOTPConn` |
|---|---|---|---|---|---|---|
| L1 Close twice | `TestBusCloseTwice` | `TestCloseTwice` | `TestCloseConcurrently`, `TestCloseAndPeerGone` | `TestConnCloseTwice` | `TestCloseConcurrently`, `TestCancelAndClose` | `TestCloseConcurrently` |
| L2 Two goroutines call Close | `TestBusCloseTwice` (the transport is closed once) | `TestCloseTwice` | `TestCloseConcurrently` (the stream is closed once, one closing `C`) | `TestConnCloseTwice` | `TestCloseConcurrently` | `TestCloseConcurrently` |
| L3 Close during a blocked read | `TestBusCloseDuringReceive`, `TestBusCloseSemantics` | `TestCloseTwice`, `TestCloseUnblocks` | `TestCloseUnblocksReceive`, `TestCloseConcurrently` | `TestConnCloseDuringReceive`, `TestFlowControlTimeoutAndClose` | `TestCancelAndClose` (Close 20 ms after the Receive started: a real socket gives no event to wait for), `TestCloseConcurrently` | `TestCloseConcurrently` |
| L4 Close during a write | `TestBusCloseDuringSend` | `TestPortCloseEndsSend`, `TestBusCloseEndsSend` | `TestStalledPeer` | `TestCloseWithSendWaiting` (a transfer and a Send that waits for it; no frame after Close returned) | `TestSendTurn` (a Send that waits for another; needs no interface). A write that waits in the kernel is not tested: vcan never fills its queue | `TestKernelISOTPCloseDuringStall` (Close from another goroutine during a transfer kept open by WAIT flow controls returns within 200 ms, no consecutive frame after it) |
| L5 Close during handshake | n/a: no handshake | n/a | n/a: the setup commands are written by `New`/`Open` before a Conn exists; their failure is L14 | n/a | n/a | n/a |
| L6 Cancel a blocked call | `TestReceiveCancel`, `TestReceiveContextCancel` | `TestSendWaitingFollowsContext`, `TestInjectAndBlockedSendCancel` | `TestStalledPeer` (Send at its deadline, at cancellation, behind another Send) | `TestSendWaitingFollowsContext` (the next Send goes out), `TestConnCloseDuringReceive` | `TestCancelAndClose`, `TestSendTurn` | Receive: `TestKernelISOTPErrors`; Send: `TestKernelISOTPSendDeadlineWaitStall`, `TestKernelISOTPSendCancelSlowBlocks` (the connection is closed afterwards), `TestKernelISOTPCloseDuringStall` (behind another Send) |
| L7 Response after the request timed out | n/a: no request and response | n/a | n/a | `TestStrayFlowControl`: stray, duplicated and late flow controls are dropped (logged at Debug, not counted) | n/a | n/a: the kernel's |
| L8 Response after reconnect | n/a: no reconnect. The reader retries a failing `Receive` with back-off: `TestInterfaceDownAndUp`, `TestBusTransportFailures` | n/a | n/a | n/a | n/a | n/a |
| L9 Callback calls a public method | `TestCallbackCallsBus` | n/a: no callbacks (`Config.Clock` is a time source) | n/a: no callbacks | `TestCallbackSendsReply` | n/a: no callbacks | n/a: no callbacks |
| L10 Callback calls Close | `TestCallbackClosesBus`: `go bus.Close()` completes after the callback returned (OBSERVABILITY.md) | n/a | n/a | `TestCallbackClosesConn` | n/a | n/a |
| L11 Slow callback | n/a: isolation is not promised; a callback that waits holds up the reader (OBSERVABILITY.md). The `Block` policy: `TestBusBlockingSubscriberLosesNothing` | n/a | n/a | n/a: as the Bus | n/a | n/a |
| L12 Queue at capacity | `TestBusOverflowPolicies`, `TestBusXLOverflowAndEcho` (`Dropped`) | `TestQueueOverflowCountsDrops` | `TestLongGarbageAndDrops` | not tested: a message that does not fit the receive queue is dropped with a log entry, without a counter (LIMITATIONS.md) | `TestOverflowAccounting` | n/a: the kernel's queue |
| L13 Concurrent sessions | `TestBusFanOutAndFilters`, `TestBusStress` | `TestLoopbackRules` | n/a: one stream | `TestExtendedAddressFiltering`, `TestMetricsAndConcurrentSend` | `TestKernelFilterSemantics` | `TestKernelVersusUserspace` |
| L14 Failure during construction | `TestNewBusFailure` (no goroutine, the transport not closed), `TestBusTransportFailures` | n/a: `New` cannot fail; `Open` on a closed bus: `TestCloseTwice` | `TestConstructionFailure` (the stream closed once, no descriptor left by `Open`), `TestBadConfig` | `TestConfigValidation` (nothing is acquired before the configuration is valid) | `TestOpenErrors` | `TestKernelISOTPErrors`, `TestDialISOTPConfigBounds` |
| L15 `Shutdown(ctx)` | n/a: no graceful shutdown | n/a | n/a | n/a | n/a | n/a |
| L16 Close racing reconnect | n/a: no reconnect | n/a | n/a | n/a | n/a | n/a |

Callbacks that do not return normally are covered by
`TestCallbackFailureLogged` in `canbus` and `isotp`.

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
| Schedule | `nightly.yml` | on `main`, every night: the chaos suites with 500 seeds on amd64 and arm64, every fuzz target for 5 minutes, and the tests with `GOARCH=386` |
| Release | `release.yml` | the release workflow tests the release commit again |

Documentation means an added or modified `*.md` file outside `.github/`, `testdata/` and `vendor/`, other than `AGENTS.md` and `CLAUDE.md`. A deleted or renamed document, or any other file, runs everything. `OT Fabric Required Checks` fails unless every job that applies succeeded; its summary lists them.
