# Interoperability

go-canbus is tested against independent implementations published by
[otfabric/canbus-interop](https://github.com/otfabric/canbus-interop), through
its container contract only (no code of those stacks is linked or vendored).
The suite is `interop/` (build tag `interop`), run with `make interop` on
Linux with Docker and `vcan0`, and in CI by the Interop workflow on amd64 and
arm64, against the published images of canbus-interop v0.1.0 pinned by digest in `interop/harness_test.go`. It writes a result matrix (`bin/interop-report.json`): every scenario
is `pass`, `fail`, `skip` (with the reason) or, for individual frames,
`known-difference` (with the reason below). A skipped scenario is never
counted as passed.

## What is independent

| Stack | Language | Licence | Role here | Independent of go-canbus in |
|-------|----------|---------|-----------|-----------------------------|
| linux-can/can-utils v2025.01 | C | GPL-2.0-only OR BSD-3-Clause | raw CAN (cansend, candump, canplayer) | frame encoding, candump log format |
| Linux kernel CAN_ISOTP (via isotpsend/isotprecv) | C | GPL-2.0 | ISO-TP | the whole ISO-TP state machine |
| python-can 4.6.1 | Python | LGPL-3.0 | raw CAN, capture/replay | frame encoding, log format |
| socketcan-rs 4.0.0 | Rust | MIT | raw CAN | frame encoding |
| Scapy 2.8.0 | Python | GPL-2.0 | raw CAN | frame encoding |
| python-can-isotp 2.0.7 | Python | MIT | ISO-TP (userspace) | the whole ISO-TP state machine |
| cantools 45.1.0 | Python | MIT | DBC oracle | DBC parsing, signal codec |
| canmatrix 1.3.3 | Python | BSD-2-Clause | DBC and KCD oracle | DBC/KCD parsing, signal codec |

All raw-CAN stacks, go-canbus included, use the same Linux SocketCAN kernel
code on a vcan interface: these tests show that each userspace stack puts the
same frames on the socket and reads them the same way. They are not
independent CAN controllers, and vcan has no arbitration, timing or errors.
The two ISO-TP peers (kernel and python-can-isotp) are genuinely independent
implementations of the protocol.

## Results

Run 2026-10-27 on Ubuntu (kernel 7.0, vcan0 MTU 72, can-isotp loaded) with
the canbus-interop images (`make interop`; since v0.1.0 they are the published images, pinned by digest, in CI on amd64 and arm64 with the same result):
**120 scenarios passed, 0 failed, 4 skipped**, 66 frames with a known
difference.

| Area | Scenario | can-utils / kernel | python-can | socketcan-rs | Scapy | python-can-isotp | cantools | canmatrix |
|------|----------|---|---|---|---|---|---|---|
| Raw CAN | go → stack: standard, extended, remote, CAN FD + BRS, CAN FD + ESI | pass | pass | pass | pass | | | |
| Raw CAN | stack → go: the same plus error frames | pass | pass | pass | pass | | | |
| Raw CAN | 2000-frame burst both ways, order and loss | pass | pass | pass | pass | | | |
| Raw CAN | multiple consumers at once (all stacks + 2 Bus subscriptions) | pass | pass | pass | pass | | | |
| Capture | stack candump log → go reader; go writer → stack replay | pass | pass | skip¹ | skip¹ | | | |
| ISO-TP | go → stack and stack → go, 1..4095 bytes: normal, 29-bit, extended addressing, mixed addressing, padding, BS/STmin, CAN FD (TX_DL 64 and 16) | pass (16) | | | | pass (16) | | |
| ISO-TP | go receiver answers OVERFLOW, stack sender fails | pass | | | | pass | | |
| DBC | decode 7 fixtures (Intel/Motorola, cross-byte, signed, 64-bit, scaling, value tables, mux, extended mux, CAN FD, float): raw exact, physical, labels, active signals | | | | | | pass² | pass² |
| DBC | encode physical and raw values, compare bytes | | | | | | pass² | pass² |
| KCD | canmatrix's KCD→DBC layout vs go-canbus's KCD parser, decode agreement | | | | | | | pass |

¹ The stack's adapter has no capture/replay operation (capability).
² With the known differences below.

## Known differences

| Oracle | Difference | Why go-canbus differs |
|--------|------------|-----------------------|
| cantools | Rejects decoding and encoding a multiplexer value that selects no multiplexed signal (for example `Mux = 3` when only 0..2 exist) | go-canbus decodes the switch and the always-present signals, as canmatrix does; a payload with an undescribed switch value is still a valid frame |
| canmatrix | Rejects payloads longer than the message length | go-canbus, like cantools, decodes the first Length bytes: CAN FD frames are padded to the next valid FD length |
| canmatrix | Cannot encode extended multiplexing (`EncodingComplexMultiplexed`) | Limitation of canmatrix 1.3.3; go-canbus's extended-multiplexing encoding is checked against cantools |

## Test choices

- go → stack bursts are paced at 200 µs per frame: the receivers of the
  interpreted stacks overflow their socket buffers at the full vcan rate.
  Stack → go bursts run at full rate.
- python-can-isotp as a *receiver* asks for STmin 0 but loses consecutive
  frames of 4095-byte messages sent back to back (its Python receive path is
  slower than vcan); the suite asks it for STmin 1 ms. The kernel receiver
  keeps STmin 0.
- Raw values of float signals are compared through decoding only: the
  oracles read `encode --raw` values of float signals as numbers, not as bit
  patterns.
- 64-bit physical inputs are passed as decimal text (`candb.PhysText`):
  through float64 they would lose bits above 2^53.

## Defects these tests found in go-canbus (fixed)

- can-utils since 2023 writes the CAN FD flags nibble with FDF set
  (`123##4`); the candump parser rejected it.
- 64-bit physical values given as JSON numbers lost precision (no exact
  input path existed).

## Needs real hardware

Bit timing, sample points, CAN FD data phase, error counters, bus-off and
restart, ESI from an error-passive node, arbitration: vcan cannot show them.
