# go-canbus Releases

Releases are made with the Release workflow (Actions → Release → bump):
it computes the version, tags it and publishes this file's section of
that version as the release notes. Add the section, headed exactly
`## vX.Y.Z` and closed by a line `---`, before running it.

## v0.9.0

**Date:** 2026-11-08
**Previous release:** v0.8.0

### Summary

go-canbus is now tested against CAN logs recorded by others on twelve real
vehicles ([testdata/captures](testdata/captures/README.md); evidence class B),
committed byte for byte as published: ten passenger cars at their OBD-II port
(CANdid), the ten CAN domains of a 2020 electric vehicle with two workshop
testers and CaringCaribou on its diagnostic bus (CarDS), and a Renault Clio
(TU Eindhoven). 16 logs, 2.7 million frames, 131 MB, run offline in
`go test ./...`; `make captures-extended` checks the three recordings too
large to commit (14.7 million more frames, TU/e's Opel Astra included).
Wireshark 4.6.9 is the independent decoder. The recordings showed two
losses in the trace writers, both fixed. The ISO-TP connection reproduces
the frames of the 21 recorded tester–ECU pairs byte for byte; replaying
the recorded 515-byte response on a loaded CI runner exposed a spurious
N_Cr timeout, fixed.

### Fixed

- `capture`: the ASC, BLF and TRC writers wrote every interface that is not a bare number as channel 1, so a multi-bus recording (CarDS: `can0`..`can8`) collapsed into one channel. A name ending in a number now gets that number plus one (`can0` → 1, `can4` → 5), as python-can's writers number them; a bare number stays itself, any other name is 1. **Behaviour change**: frames of `can1`, `vcan2`, … are written on channels 2, 3, … instead of 1. A BLF channel above 65535 is now `ErrUnrepresentable` instead of channel 1.
- `capture`: the candump writer dropped the `R`/`T` direction token that `candump -L -x` writes and the reader accepts, so a log with tokens lost its `T` marks when written back. `CandumpWriter.SetDirection(true)` writes the token (`T` for `Frame.Echo`, else `R`); off by default, so plain output is unchanged.

- `isotp`: a consecutive frame already queued when N_Cr expired was ignored half the time (the receive goroutine's `select` picked the timer), giving `ErrTimeout` for a transfer whose frame arrived in time. Queued frames are now handled first; the timeout fires only if none continues the transfer.

### Added

- `capture`: `CandumpWriter.SetDirection`.
- `testdata/captures`: recordings of CANdid, CarDS and TU/e under clean names (`<tool-or-tester>_<vehicle>_<what-it-holds>.log`), `manifest.json` (sources, licences, original names, URLs and SHA-256, cuts, expected counts, a machine-readable coverage list), `SHA256SUMS`, Wireshark's ISO 15765 dissection of the CarDS diagnostic bus; `internal/corpus`; `internal/cmd/fetchcaptures` and `make captures-extended` for the whole recordings (HTTP range reads of CarDS's 3.6 GB archive).

### Tests

- `capture`: recorded lines decode to the manifest's counts and to what Wireshark's candump reader decodes; write-back is byte for byte; conversion through every format keeps each frame and gives each interface its own channel; the out-of-order first line of the CANdid logs through BLF and replay; truncated and corrupted recorded lines; recorded lines seed `FuzzReaders`; the extended corpus round trip.
- `capture`: four records of the Wireshark wiki's CANopen.pcap whose writer left 0x7F in the FD flags byte of classical frames: read as CAN FD, as tcpdump.org's LINKTYPE_CAN_SOCKETCAN definition and Wireshark 4.6.9 read them (pinned, not changed).
- `isotp`: `TestQueuedConsecutiveFrameBeatsNCr` holds the receive goroutine while a consecutive frame queues and N_Cr expires (fails without the fix).
- `isotp`: listen-only reassembly equals Wireshark's messages (incomplete first frames end in reception errors); as each recorded tester and ECU, requests, flow control, single, first and consecutive frames equal the recorded ones byte for byte; recorded frames seed `FuzzReceiver`.

### Known differences

pcap carries no interface names and pcap/pcapng no direction; BLF writes frames older than the first frame at its start time (258 CANdid frames); recorded ECUs send consecutive frames 8–20 ms apart where go-canbus uses the STmin asked for. Not in any recording, so verified against software peers only: remote and error frames, classical DLC 9..15, CAN XL, ISO-TP extended/mixed addressing, CAN FD ISO-TP, WAIT/OVERFLOW, multi-frame requests.

### Documentation

testdata/captures/README.md, docs/CAPTURE-COVERAGE.md; TESTING.md, INTEROPERABILITY.md, LIMITATIONS.md, docs/FEATURE-STATUS.md, API.md, README.md.

---

## v0.8.0

**Date:** 2026-11-04
**Previous release:** v0.7.2

## Summary

SAST review (gosec, opengrep at severity and confidence low): 96 findings,
91 fixed in code, 5 recorded as false positives (`unsafe` for three
syscalls the standard library has no AF_CAN form of; the seeded fault
generators of `virtual` and `chaos`). The review found 16 defects, most
where a value wrapped silently; they now return errors. Go 1.26 is the
minimum. Interoperability against canbus-interop v0.6.0: 170 passed,
0 failed, 13 skipped, 132 known-difference records (unchanged). Coverage
94.4 % (Linux with vcan).

### Changed

- **Go 1.26 is the minimum** (was 1.23); CI tests 1.26 and 1.27. `go fix` applied.
- Errors where values used to wrap or panic (no exported signature changes):
  - `dbc`: a message identifier above 32 bits in `CM_`, `VAL_`, `BA_`, `BO_TX_BU_`, `SIG_VALTYPE_`, `SG_MUL_VAL_` or `SIG_GROUP_` is a syntax error; it used to attach to message `id mod 2^32`.
  - `candb`: `Decode` and `Encode` return `ErrLayout` for a start bit beyond 2^24, a negative length, a length beyond the CAN XL payload or a CAN FD length over 64; they used to panic.
  - `capture`: new `ErrUnrepresentable`. Writers refuse frames their format cannot hold (pcap before 1970 or after 2106, pcapng interface names over 65535 bytes, BLF CAN FD channels over 255, timestamps outside each format's range); readers return `*ParseError` for timestamps beyond `time.Duration` or the year 9999.
  - `socketcan.DialISOTP`: `ForceTxSTmin` outside 0..127 ms is `ErrInvalidOption` (−1 ns became 4.29 s, 5 s became 705 ms).
  - `socketcan/link`: `SetMTU` refuses an MTU ≤ 0 or ≥ 2^32; `AddVCAN`/`AddVXCAN` refuse names that are empty or longer than 15 bytes; a malformed NLMSG_ERROR is a protocol error, not EPERM.
  - `arxml`: a selector code ≥ 2^63 or an INIT-VALUE outside int64 is dropped with a diagnostic.

### Fixed

- `isotp`: a first frame announcing up to 4 GiB made the receiver allocate it all at once; the buffer now starts at 64 KiB and grows as data arrives.
- `slcan`: the read loop panicked when a reader reported more bytes than its buffer.
- `capture` BLF: objects split across containers (python-can splits at 128 KiB) are joined instead of failing; nested containers are skipped instead of expanded; inflation is bounded by the announced size; an announced object is no longer allocated before it is read.
- `capture` pcapng: 32-bit builds panicked on captured lengths or interface indexes ≥ 2^31; the fraction of a timestamp overflowed at resolutions above about 10^10 ticks per second.
- `capture` candump and JSONL writers wrote timestamps their readers rejected.

### Security

- `unsafe` is confined to `socketcan/sys_linux.go` (bind, recvmsg, rt_sigprocmask); control messages are parsed with `encoding/binary`, still without allocation.
- New fuzz targets: candb round trip, bit insert/extract, KCD round trip, capture round trip over seven formats, ISO-TP sender flow control, slcan stream, netlink stream, control messages. New hostile-input tests for DBC, KCD, ARXML, candb, capture (truncation at every offset, memory bounds), ISO-TP and slcan peers, and kernel ISO-TP on vcan (`e2e/hostile_linux_test.go`).

### Dependencies

- canbus-interop v0.6.0 images (by digest); shared workflows v3.3.1; go-serial v0.2.1 (latest).

---

## v0.7.2

**Date:** 2026-10-31
**Previous release:** v0.7.1

### Fixed

- `dbc` read a message identifier with a leading zero as octal, so `BO_ 010` became message 8. Identifiers are decimal. **A DBC file that relied on the old reading now gets a different identifier.**
- Built on go-serial v0.2.1.

---

## v0.7.1

**Date:** 2026-10-30
**Previous release:** v0.7.0

### Dependencies

- `go-serial` → v0.2.0: on macOS and BSD the baud constant no longer ends up in `Cflag`, where it set parity and stop-bit flags on the port.

---

## v0.7.0

**Date:** 2026-10-29
**Previous release:** v0.6.0

## Summary

CAN XL beyond SocketCAN sockets: through a `canbus.Bus`, on the virtual
bus, in pcap and pcapng traces (checked against the kernel, libpcap and
Wireshark), in replay and recording, and through the chaos bridge.
Interoperability against canbus-interop v0.5.0 (contract 1.3), amd64 and
arm64: 170 passed, 0 failed, 13 skipped, 132 known-difference records
(unchanged). Coverage 93.3 % (Linux with vcan).

## Additions

- **CAN XL on a Bus**: `canbus.XLTransport` (`SendXL`, `ReceiveAny`),
  implemented by SocketCAN sockets and virtual bus ports; `Bus.SendXL`;
  subscriptions `WithXL` receive CAN, CAN FD and CAN XL frames in arrival
  order through one queue (`Subscription.ReceiveAny`; `Receive` and
  `ReceiveXL` skip the other kind); `XLMetrics`; `XLFrame.Clone`.
- **Kernel filter semantics for CAN XL**: `FilterSet.MatchXL`,
  `Filter.MatchXL`, `XLKernelID`. Linux's CAN_RAW_FILTER matches a CAN XL
  frame on VCID<<16 | priority as a standard data identifier: a filter for
  0x242 selects priority 0x242 of every VCID, an extended filter none.
  Found by probing Linux 7.0; `TestXLKernelFilterParity` installs each
  filter set on a socket and compares with `MatchXL` on every CI kernel.
- **Virtual bus**: `WithXL` and `WithVCIDFilter` ports (`SendXL`,
  `ReceiveXL`, `ReceiveAny`), `InjectXL`; faults apply to CAN XL frames
  (corruption anywhere in up to 2048 bytes).
- **pcap and pcapng**: CAN XL read and written in libpcap's Linux layout
  (priority/VCID word big-endian, length and acceptance field
  little-endian); a new `PCAPNGWriter` (an interface block per interface
  name, nanosecond timestamps). `NewWriter(w, FormatPCAPNG)` works. The
  pcap header's snap length is 2060.
- **Replay and recording**: `Replay` sends CAN XL frames through senders
  that carry it (`XLSender`; `ReplayStats.SkippedXL`,
  `ReplayOptions.OnXLFrame`); `RecordAll` records every kind in order.
- **Chaos bridge** forwards CAN XL between `XLTransport`s
  (`Stats.SkippedXL`).

## Verification

- Real captures from Linux 7.0 in `capture/testdata`: tcpdump 4.99.6 /
  libpcap 1.10.6 (pcap) and dumpcap 4.6.4 (pcapng, two interfaces) of CAN,
  CAN FD, remote and CAN XL frames up to 2048 bytes. Read exactly; the CAN
  XL packets `PCAPWriter` writes are byte-identical to libpcap's.
- Wireshark (tshark 4.6, CI and macOS): every frame kind written to pcap
  and pcapng is dissected into the frame written, interface names
  included; Wireshark and this package read the real captures alike.
- Live: as root, tcpdump captures CAN, CAN FD and CAN XL frames sent on
  `vcanxl0` (VCIDs where the kernel passes them); the capture is read back
  here and by tshark (CI kernels, Linux 7.0).
- Kernels (microVMs): 6.12 and 6.18 pass everything; 6.6 and Ubuntu's 6.8
  everything but the VCID cases (Linux ≥ 6.9); 6.1 has no CAN XL.
- Fuzzing: the reader fuzzer is seeded with the real captures and checks
  CAN XL frames too.

## Findings

- **Wireshark before 4.4 does not dissect CAN XL**: 4.2 (Ubuntu 24.04)
  shows CAN XL frames, libpcap's own captures included, as malformed CAN
  frames of length 128+. CI installs Wireshark from its PPA.
- **libpcap before 1.10.6 alters captures**: 1.10.4 (Ubuntu 24.04's
  tcpdump) writes CAN FD flags as 0 (BRS and ESI lost) and clears bit 2 of
  a CAN XL frame's SDT; reproduced against 1.10.6 on the same kernel.
  Files are read as captured (LIMITATIONS.md).
- A first design queued CAN XL frames apart and merged by timestamp; on
  Windows, whose clock gives many frames one timestamp, recordings came
  out reordered. Order now never depends on clocks.

---

## v0.6.1

**Date:** 2026-10-29
**Previous release:** v0.6.0

## Summary

The same commit as v0.6.0: go-serial v0.1.8, which fixes two POSIX serial
I/O bugs that affect the slcan transport (see v0.6.0). Tagged as the
patch release that update was prepared as; no code differs from v0.6.0.
CAN XL beyond SocketCAN sockets is in v0.7.0.

---

## v0.6.0

**Date:** 2026-10-29
**Previous release:** v0.5.0

## Summary

Published as v0.6.0 by mistake: a release run meant for the CAN XL work
(v0.7.0) picked this commit. It was prepared as v0.5.1.

Dependency update: go-serial v0.1.8, which fixes two POSIX serial I/O bugs
that affect the slcan transport. No API changes.

## Changes

### Dependencies

- **go-serial v0.1.7 → v0.1.8.**
  - `Write` no longer loses data when the tty output buffer is full. It
    used to return a short count with a nil error (or `n == -1` with
    `EAGAIN`); it now blocks until every byte is written.
  - `Read` works when the port's file descriptor is 1024 or higher. It
    used to return a spurious `io.EOF` at once.

---

## v0.5.0

**Date:** 2026-10-28
**Previous release:** v0.4.0

## Summary

CAN XL on SocketCAN, in traces and against can-utils. Interoperability
against canbus-interop v0.5.0 (contract 1.3), amd64 and arm64: 170
passed, 0 failed, 13 skipped (the 4 capture/replay skips as before, and 9
CAN XL scenarios with stacks that have no CAN XL), 132 known-difference
records (unchanged).

## Additions

- **`canbus.XLFrame`**: a CAN XL frame (ISO 11898-1:2024) as Linux's
  struct canxl_frame lays it out — 11-bit priority, VCID, flags `XLSEC` and
  `XLRRS`, SDU type, acceptance field, 1..2048 data bytes — with
  `Validate`, `String` and `ParseXLFrame` in can-utils notation
  (`<vcid><prio>#<flags>:<sdt>:<af>#<data>`), and `IsXLText`.
  `Capabilities.XL`.
- **SocketCAN CAN XL sockets**: `socketcan.WithXL` (CAN_RAW_XL_FRAMES,
  which implies CAN FD; the interface needs a CAN XL MTU, 76..2060) and
  `WithVCIDFilter`; `Conn.SendXL`, `ReceiveXL` (CAN XL only) and
  `ReceiveAny` (CAN, CAN FD or CAN XL, in arrival order). `Receive` skips
  CAN XL frames, so a `canbus.Bus` on an XL socket keeps working. As cansend
  and candump do, the socket passes sent VCIDs through and receives every
  VCID (CAN_RAW_XL_VCID_OPTS, Linux ≥ 6.9; earlier kernels ignore VCIDs).
  Kernels without CAN XL (before 6.2) and interfaces without the MTU give
  `canbus.ErrUnsupported`.
- **CAN XL in traces**: candump logs (lines in can-utils notation) and JSON
  Lines (record type `"xlframe"`, schema updated) through the optional
  `capture.XLReader` (`ReadAny`) and `capture.XLWriter` (`WriteXL`); their
  `Read` skips CAN XL frames. `ParseCandumpXLLine`, `XLRecord`.
- Example `examples/canxl`.

## Verification

- On vcan with the CAN XL MTU (`vcanxl0`, required in CI and in the
  microVMs on kernels ≥ 6.2): 1..2048-byte frames with every flag, CAN, CAN
  FD and CAN XL frames received in order by `ReceiveAny`, `Receive` and a
  socket without `WithXL` seeing only CAN and CAN FD, echo, VCID
  pass-through and `WithVCIDFilter`, errors. MicroVM kernels: 6.12 and 6.18
  pass all; 6.6 and Ubuntu's 6.8 pass all but the VCID test, which needs
  6.9; 6.1 has no CAN XL (the tests skip there).
- Against can-utils v2025.01 (canbus-interop contract 1.3): go-canbus →
  candump and cansend → go-canbus with the six frames of `xl.jsonl`
  (priorities 0 and 0x7FF, SEC and RRS, SDT, AF, VCIDs 0x42 and 0xFF, 1 to
  2048 bytes); candump's capture log of go-canbus's frames read back by
  go-canbus's candump reader, with candump's text of every CAN XL frame
  equal to `XLFrame.String`. python-can, socketcan-rs and Scapy have no CAN
  XL (capability skips).
- `ParseXLFrame` fuzzed (parse, format, parse is stable). Coverage 92.9 %
  (Linux with vcan).

---

## v0.4.0

**Date:** 2026-10-28
**Previous release:** v0.3.0

## Summary

Adapter status over slcan, multiplexed PDUs in ARXML, and two fixes.
Interoperability against canbus-interop v0.4.0 (contract 1.2), amd64 and
arm64: 167 passed, 0 failed, 4 skipped (capture/replay operations
socketcan-rs and Scapy do not have), 132 known-difference records.

## Additions

- **slcan adapter status.** Adapters that report status send "e" records
  (bus errors: ACK, bit 0/1, CRC, form, stuff, RX/TX overrun) and "s"
  records (controller state and error counters). A `slcan.Conn` turns them
  into error frames byte for byte as the Linux slcan driver does
  (`slcan_bump_err`, `slcan_bump_state`, `can_change_state`), including
  that a state record repeating the current state yields nothing. New:
  `ParseErrorRecord`, `ParseStateRecord`, `StateFrame`, `Conn.State`
  (state and counters of the last record), `Conn.SetFilters` (applied in
  software; as with SocketCAN, no error frames until an ErrorMask selects
  them). A `canbus.Bus` on slcan now tracks warning, passive and bus-off.
- **ARXML MULTIPLEXED-I-PDUs**, read as cantools reads them: a multiplexer
  switch `<frame>_selector<n>` at the selector field (Intel or Motorola),
  each dynamic alternative's signals multiplexed by its
  SELECTOR-FIELD-CODE, static parts always present, the initial dynamic
  part as the switch's initial value; AUTOSAR 4.1+ (`DYNAMIC-PARTS`,
  `STATIC-PARTS`) and earlier 4.x layouts. Diagnostics for bad selectors,
  codes that do not fit, alternatives that are not I-SIGNAL-I-PDUs and
  nested multiplexing.

## Fixes

- go-serial v0.1.7: a failing read no longer returns a negative count, and
  an unplugged adapter reads `io.EOF` instead of empty reads forever
  (go-canbus guarded against both since v0.3.0).
- `Subscription.Received` counts a frame before queueing it: a consumer
  could hold the frame before the counter showed it (found by the race
  detector in CI).

## Verification

- slcan status against the Linux kernel's slcan driver (6.1, 6.6, 6.12,
  6.18 and Ubuntu's kernel in microVMs): 13 "e" and "s" records written to
  the driver; its error frames equal go-canbus's, and where it delivers
  nothing (an unchanged state) neither does go-canbus. Kernels before 6.18
  ignore records of four characters or fewer (such as `e1a`) and accept
  characters after a state record; go-canbus decodes the short records
  and, like 6.18, requires state records of exactly eight characters.
- Multiplexed ARXML against cantools 45.1.1 with canbus-interop's
  `multiplexed.arxml`: decode of every selector code and encode agree
  byte for byte. cantools rejects undescribed selector values (known
  difference, as for DBC); canmatrix 1.3.3 reads only the selector (known
  difference). The known-difference records rise from 78 to 132 for these
  two reasons; no other result changed.
- Coverage: `slcan` 98.4 %, `arxml` 95.1 %.

---

## v0.3.0

**Date:** 2026-10-28
**Previous release:** v0.2.2

## Summary

Serial-line CAN adapters on every operating system, and PEAK TRC traces.
Interoperability against canbus-interop v0.3.0 (contract 1.2): 163 passed,
0 failed, 4 skipped (capabilities the reference stacks lack).

## Additions

- Package `slcan`: a `canbus.Transport` for serial-line CAN (LAWICEL ASCII)
  adapters such as the CANable, CANtact, USBtin and LAWICEL CANUSB, without
  the Linux slcan driver: `slcan.Open(device, baud, cfg)` over a serial port
  (through [go-serial](https://github.com/otfabric/go-serial), pure Go, the
  module's one dependency) on Linux, macOS, Windows and the BSDs, or
  `slcan.New` over any byte stream (a pty, a TCP serial server). Bit rates
  (`S0`–`S9`), CAN FD data rates (`Y2`/`Y5`, CANable 2.0), listen-only, CAN
  FD records `d`/`D`/`b`/`B`, drop accounting; a lost device is
  `canbus.ErrInterfaceDown`. The record codec (`AppendFrame`, `ParseFrame`)
  is fuzzed. Example: `examples/slcan`.
- PEAK TRC traces in `capture`: versions 1.0, 1.1, 1.2, 1.3, 2.0 and 2.1
  read, 2.1 written, with classical, CAN FD (`FD`, `FB`, `FE`, `BI`), remote
  and error frames; detected by `NewReader`, chosen by `.trc`.

## Verification

- slcan against the Linux kernel's own slcan driver: the N_SLCAN line
  discipline on a pseudo-terminal creates an slcanN interface whose frames
  are exchanged both ways with a go-canbus Conn on the other end
  (`make test-admin`; required in the Kernels microVMs). Over real ptys
  with go-serial on Linux and macOS. Against python-can 4.6.1's slcan
  interface over TCP (interop, both directions, CAN FD, 2000-frame burst).
- TRC against python-can 4.6.1 both ways and on records of TRC 1.0, 1.1,
  1.3 and 2.0 read alike by both. Recorded known differences: python-can
  reads FE as bit-rate switched, skips ER records and does not write
  remote, CAN FD or error frames; TRC 1.2 has no independent reader.
- Coverage 92.8 % (Linux with vcan); `slcan` 97.7 %.

---

## v0.2.2

**Date:** 2026-10-28
**Previous release:** v0.2.1

## Summary

A Bus can now start reading at its first subscription, so no frame that
arrives between `NewBus` and `Subscribe` is lost. Found by canbusctl's
end-to-end tests in a slow microVM: `record` saw 21 of 60 frames.

## Additions

- `canbus.WithStartOnSubscribe()`: the bus's reader starts at the first
  `Subscribe` (or `Close`); until then frames wait in the transport (the
  socket's receive buffer). Without it the reader starts in `NewBus`, as
  before, and frames read before the first subscription reach nobody — now
  documented on `Bus`.
- The `monitor` and `capture-replay` examples and the README use it.

---

## v0.2.1

**Date:** 2026-10-28
**Previous release:** v0.2.0

## Summary

Two fixes reported by canbusctl v0.2.0: a socket deadline that fires a
moment before its context's deadline now ends calls the way the context
does.

## Fixes

- `chaos.Bridge.Run` returns nil when ctx ends, also when the transport's
  deadline fired first (it returned a deadline error) and when ctx ends
  during a send.
- `socketcan.ISOTPConn` reports such a deadline as `context.DeadlineExceeded`
  (or `canbus.ErrTimeout`), not as an `*OpError` "i/o timeout".

---

## v0.2.0

**Date:** 2026-10-28
**Previous release:** v0.1.0

## Summary

More formats in and out, and much deeper testing. Vector ASC and BLF traces,
AUTOSAR 4.x ARXML databases and KCD output join the codecs; a new `chaos`
package bridges two transports with seeded faults; chaos-monkey and
end-to-end suites run on every push, a nightly job runs 500 fault schedules,
and the Kernels workflow runs the SocketCAN and kernel ISO-TP tests in
microVMs with five kernels. They found and fixed two kernel ISO-TP bugs in
how go-canbus drives the kernel (see Fixes). Interop runs against
canbus-interop v0.2.0 (contract 1.1).

## Changes

- `capture`: Vector ASC and BLF readers and writers (checked both ways with python-can); `FormatFromPath`.
- `arxml`: AUTOSAR 4.x reader for CAN: clusters, frame triggerings (CAN FD, extended IDs, senders/receivers), frames with PDUs at byte offsets, I-PDUs, signals with signed and IEEE 754 base types, compu methods (LINEAR, TEXTTABLE, SCALE_LINEAR_AND_TEXTTABLE), units, limits; diagnostics for the rest.
- `kcd.Write`: KCD output, read back unchanged by canmatrix.
- `candb.PhysText`: exact physical input from decimal text (64-bit values).
- `isotp.Config.ListenOnly` (also kernel sockets) and `FlowControlDelay` (N_Br).
- `chaos`: fault-injecting bridge (drop, duplicate, delay, reorder, corrupt; filter; seeded).
- `virtual.FaultStats` has JSON tags.

## Fixes

- **Kernel ISO-TP send could resend a message** (`socketcan.DialISOTP`): with `CAN_ISOTP_WAIT_TX_DONE` the kernel waits inside `sendmsg` interruptibly; Go's preemption signal (SIGURG) aborted the transfer and the restarted syscall sent the message again. The write now runs on a locked thread with signals blocked.
- **Stalls against the kernel's ISO-TP sender on vcan**: the kernel drops a flow control that arrives before it has processed the echo of its own last frame of a block. Use `FlowControlDelay` (a few hundred µs) against kernel peers on virtual interfaces.
- The KCD writer no longer writes empty attributes (canmatrix rejected them).

## Testing

- Interop against canbus-interop v0.2.0: raw CAN with can-utils, python-can, socketcan-rs and Scapy; ISO-TP with the kernel and python-can-isotp; DBC, ARXML and KCD with cantools and canmatrix; ASC, BLF and candump with python-can.
- Chaos monkey: ISO-TP never delivers wrong data under drop/duplicate/delay and recovers; corruption and reordering never panic or hang; Bus operations under churn keep order and leak nothing; also kernel ISO-TP ↔ go-canbus through the chaos bridge.
- End-to-end: vehicle scenario (generated ECUs, gateway, recording into five formats, ISO-TP, replay) on the virtual bus and on vcan with the kernel; processes on vcan; generated code from DBC, KCD and ARXML compiled in a separate module.

---

## v0.1.0

**Date:** 2026-10-27
**Previous release:** none

## Summary

First release of go-canbus: a pure Go CAN library for classical CAN and
CAN FD frames, Linux SocketCAN, a deterministic virtual bus, ISO-TP, capture
and replay, and DBC/KCD databases with a signal codec and a Go code
generator. Tested against independent implementations from
canbus-interop.

## Changes

- `canbus`: frames, DLC, filters with Linux `CAN_RAW_FILTER` semantics, error frames, `Transport`, `Bus` with subscriptions, slog and metrics.
- `socketcan`: raw sockets (FD, kernel filters, loopback, receive-own, timestamps, drop accounting, cancellation; zero allocations) and kernel ISO-TP sockets.
- `socketcan/link`: rtnetlink administration.
- `virtual`: deterministic in-memory bus with fault injection.
- `isotp`: ISO 15765-2 in userspace.
- `capture`: candump, JSON Lines, pcap, pcapng; replay.
- `candb`, `dbc`, `kcd`, `codegen`: message databases, exact codec (64-bit raw values, `PhysText`), generator.

## Testing

- Interop against canbus-interop v0.1.0 (can-utils and kernel ISO-TP, python-can, socketcan-rs, Scapy, python-can-isotp, cantools, canmatrix): 120 scenarios pass on amd64 and arm64; known differences of the oracles in INTEROPERABILITY.md.
- Kernels workflow: the SocketCAN, netlink and kernel ISO-TP tests in microVMs with Ubuntu generic 6.8 and mainline 6.1, 6.6, 6.12, 6.18.
- Coverage ≥ 90 % on vcan; race detector; 11 fuzz targets.

---
