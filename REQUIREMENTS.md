# Requirements

Status column: **done** = implemented and tested (test named), **partial**,
**planned**, **out** = out of scope. Kept in step with the code; see
[PROGRESS.md](PROGRESS.md) for the running state.

## Standards and sources

| Source | Public? | Used for | Notes |
|--------|---------|----------|-------|
| ISO 11898-1:2015 (CAN, CAN FD) | No (ISO, paid) | Frame concepts only | Implemented from public material: Linux `include/uapi/linux/can.h`, Bosch CAN 2.0 (1991, public) and CAN FD 1.0 (2012, public) specifications. No conformance claim. |
| ISO 15765-2:2016/2024 (ISO-TP) | No (ISO, paid) | Segmentation behaviour | Implemented from public descriptions, the Linux `can-isotp` implementation and documentation and independent implementations used as test oracles (Linux kernel, python-can-isotp). No conformance claim. |
| Linux SocketCAN (`Documentation/networking/can.rst`, `linux/can.h`, `can/raw.h`, `can/isotp.h`, `can/netlink.h`, `can/error.h`) | Yes (GPL-2.0 WITH Linux-syscall-note headers) | Socket ABI, constants | Constants re-declared in Go; no header code copied. |
| DBC format | No formal public spec | Parser | Vector's "DBC File Format Documentation" circulates informally; behaviour cross-checked against cantools and canmatrix. |
| KCD (Kayak) | Yes (LGPL-3.0 schema, Kayak project) | Parser | Schema semantics only; no schema file vendored. |
| AUTOSAR ARXML | Yes (AUTOSAR, registration) | Not implemented | Planned via a pinned build-time canmatrix conversion; see LIMITATIONS.md. |
| candump log format | Yes (can-utils, `lib.c`) | capture | |
| pcap `LINKTYPE_CAN_SOCKETCAN` (227) | Yes (tcpdump.org) | capture | |

## Functional requirements

| ID | Requirement | Status |
|----|-------------|--------|
| F-FR-1 | 11-bit and 29-bit identifiers with explicit format flag; validation of range | planned |
| F-FR-2 | Classical CAN 0–8 bytes, DLC 9–15 preserved (`Len8DLC`) | planned |
| F-FR-3 | CAN FD lengths 0–64 with discrete DLC mapping, BRS, ESI | planned |
| F-FR-4 | Remote frames (classical only) with requested DLC | planned |
| F-FR-5 | Error frames represented explicitly; parsed into `ErrorInfo` | planned |
| F-FR-6 | Timestamp, interface, own-echo metadata | planned |
| F-TR-1 | Transport interface; Bus fan-out with subscriptions, bounded queues, overflow policy | planned |
| F-TR-2 | SocketCAN raw: FD, kernel filters, error mask, loopback, recv-own, timestamps, overflow counter, context cancellation, close semantics | planned |
| F-TR-3 | Virtual bus: deterministic, multi-producer/consumer, queue policies, fault injection | planned |
| F-TR-4 | Netlink administration (separate API): list, info, up/down, bitrate, FD, vcan/vxcan create/delete | planned |
| F-FI-1 | Exact/masked standard/extended filters, inversion, join; same semantics in software and kernel | planned |
| F-TP-1 | ISO-TP userspace: normal, normal-fixed, extended, mixed addressing | planned |
| F-TP-2 | SF/FF/CF/FC, BS, STmin (ms and 100–900 µs), WAIT/WFTmax, OVFLW, SN wrap | planned |
| F-TP-3 | N_As/N_Bs/N_Cr timeouts, padding, invalid sequence handling | planned |
| F-TP-4 | CAN FD ISO-TP (TX_DL up to 64, SF escape), FF escape > 4095 bytes, MaxMessageSize bound | planned |
| F-TP-5 | Kernel CAN_ISOTP adapter with the same configuration | planned |
| F-CA-1 | candump log read/write (classical, FD, RTR, error) | planned |
| F-CA-2 | Lossless JSONL capture, versioned schema | planned |
| F-CA-3 | pcap LINKTYPE_CAN_SOCKETCAN read/write | planned |
| F-CA-4 | Replay: original, scaled, maximum speed; filters, limits, rate limiting; streaming | planned |
| F-DB-1 | Canonical model independent of DBC syntax | planned |
| F-DB-2 | DBC: messages, signals, Intel/Motorola, signed, scaling, units, limits, value tables, comments, attributes and defaults, senders/receivers, cycle time, float signals, CAN FD lengths, multiplexing and extended multiplexing | planned |
| F-DB-3 | Exact 64-bit raw values; no float round-trip loss | planned |
| F-DB-4 | Diagnostics for unknown/unsupported constructs | planned |
| F-DB-5 | DBC writer (for convert) | planned |
| F-DB-6 | KCD parser | planned |
| F-GN-1 | Deterministic gofmt-clean Go generation: structs, ID constants, enums, encode/decode, length checks, scaling, mux-aware, docs, provenance; JSON descriptors | planned |
| F-OB-1 | slog logging, Metrics callbacks | planned |

## Non-functional requirements

| ID | Requirement |
|----|-------------|
| N-1 | Pure Go, no cgo, no third-party runtime dependency; Go 1.23 floor |
| N-2 | ≥ 90 % statement coverage of production packages (generated test fixtures and examples excluded, documented in TESTING.md) |
| N-3 | Race detector clean; concurrency stress tests; goroutine leak checks |
| N-4 | Fuzz targets for every parser/decoder of untrusted input |
| N-5 | Zero allocations on the SocketCAN and virtual send/receive hot paths (benchmarked) |
| N-6 | Every interoperability claim backed by an automated test against an independent implementation |
| N-7 | Compiles on linux/{amd64,arm64,arm}, darwin/{amd64,arm64}, windows/{amd64,arm64} |

## Out of scope (initial)

CANopen, SAE J1939, UDS (ISO 14229), NMEA 2000, ISOBUS, DroneCAN/Cyphal,
CAN XL production support, hardware vendor drivers (PEAK, Kvaser, Vector,
slcan) — the Transport interface is the extension point for them.
