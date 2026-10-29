# Gaps and follow-ups

What the CAN ecosystem (go-canbus, [canbusctl](https://github.com/otfabric/canbusctl),
[canbus-interop](https://github.com/otfabric/canbus-interop)) does not do
yet, what is not verified, and the follow-ups worth doing, as of go-canbus
v0.7.0, canbusctl v0.6.0 and canbus-interop v0.5.0 (2026-10-29). Work
paused here; nothing below is in progress.

[LIMITATIONS.md](LIMITATIONS.md) lists every limitation in detail; this
page groups them into decisions. Effort: **S** about a day, **M** a few
days, **L** a week or more.

## Where things stand

| Area | State | Verified against |
|------|-------|------------------|
| Classical CAN, CAN FD frames, filters, error frames | Complete | Linux kernel (vcan, 6.1 to 7.0), python-can, socketcan-rs, can-utils, Scapy |
| CAN XL | SocketCAN, `canbus.Bus`, virtual bus, candump/JSON Lines/pcap/pcapng traces, replay, chaos bridge | Linux kernel 6.2+ (VCIDs 6.9+), can-utils, tcpdump/libpcap, Wireshark 4.6 |
| SocketCAN, netlink administration, kernel ISO-TP | Complete for vcan | Linux kernel in microVMs (6.1, 6.6, 6.8, 6.12, 6.18) |
| slcan adapters | Classical and CAN FD, status records | Linux slcan driver (on a pty), python-can |
| ISO-TP (userspace) | Normal, extended, mixed addressing, CAN FD, > 4095 bytes | Linux kernel ISO-TP, python-can-isotp, can-utils |
| Traces | candump, JSON Lines, pcap, pcapng, ASC, BLF, TRC | python-can, can-utils, tcpdump, Wireshark |
| Databases | DBC, KCD read/write, ARXML (CAN subset of AUTOSAR 4) read, code generation | cantools, canmatrix |
| canbusctl | Every library feature above as commands | In-process and e2e tests, interop images |

## Gaps in function

| # | Gap | Impact | Follow-up | Effort |
|---|-----|--------|-----------|--------|
| 1 | No vendor hardware adapters (PEAK PCAN-Basic, Kvaser CANlib, Vector XL). gs_usb and most USB adapters work through SocketCAN on Linux; slcan adapters work everywhere | On Windows and macOS only slcan adapters can be used | One `canbus.Transport` per vendor. PCAN-Basic and CANlib need their vendor libraries (cgo or dynamic loading), which breaks the pure-Go rule: put each in its own module | L each |
| 2 | No higher-layer protocols: CANopen, SAE J1939, UDS (ISO 14229), NMEA 2000, ISOBUS, DroneCAN/Cyphal | Users build them on `canbus.Bus` and `isotp` themselves | Separate modules. J1939 (address claim, transport protocol) and UDS on `isotp` are the most asked for | L each |
| 3 | CAN XL is missing from slcan, ISO-TP, the message databases, and ASC/BLF/TRC traces | CAN XL works only on SocketCAN and the virtual bus, and only candump, JSON Lines, pcap and pcapng traces carry it | Follow the upstreams: no slcan CAN XL protocol, no CAN XL ISO-TP in Linux, no CAN XL in DBC/ASC/BLF exist yet | wait |
| 4 | ARXML: no writer; no AUTOSAR 3.x, container, secured or diagnostic PDUs, nested multiplexing, signal groups, end-to-end protection; SEGMENT-POSITIONS of multiplexed PDUs not applied | AUTOSAR projects with those constructs get diagnostics, not data | Container and secured PDUs first (most common after plain I-PDUs) | M |
| 5 | DBC: environment variables, signal types, relation attributes, categories and filters skipped (with a diagnostic) | Round trips through DBC lose them | Model them in `candb` only if a user needs them | M |
| 6 | KCD: remote frame definitions not modelled; label groups above 256 values not expanded | Rare | On demand | S |
| 7 | ISO-TP: no functional addressing helpers, no N_As/N_Ar confirmation timing, no kernel half-duplex mode; kernel sockets have fixed 1 s timeouts and send no WAIT | Diagnostics stacks that need functional requests build them by hand | Functional addressing helper (one SF to a functional ID, many responders) together with a UDS module | S–M |
| 8 | slcan: no echo of sent frames, no adapter timestamps (LAWICEL "Z1" ignored), no send confirmation, acknowledgements not matched to commands, DLC 9..15 sent as 8 | Timestamps are host arrival times; a lost frame is not reported | Parse "Z1" timestamps; match acknowledgements to commands | M |
| 9 | Physical values are float64 (raw values beyond 2^53 lose precision in `Physical`; the raw API is exact) | Only 64-bit signals with scaling | None planned: the raw API covers it | — |
| 10 | No MF4 (ASAM MDF) traces | MDF users convert elsewhere | MF4 reader/writer for CAN bus logging records; the format is public (ASAM, registration) | L |

## Gaps in verification

| # | Gap | Why it matters | Follow-up |
|---|-----|----------------|-----------|
| V1 | No test on real CAN hardware: bit rates and sample points, CAN FD data phase and transceiver delay compensation, error counters, bus-off and `restart-ms`, ESI from an error-passive transmitter, arbitration and timing under load | vcan is a perfect medium: it proves frame encoding and protocol logic, not electrical or timing behaviour | A bench with two USB adapters (gs_usb on Linux), a self-hosted CI runner; CAN XL needs a CAN XL controller, of which few exist |
| V2 | One kernel CAN stack: the four raw-CAN oracles (python-can, socketcan-rs, can-utils, Scapy) are all clients of Linux `CAN_RAW` | They catch encoding and socket-option mistakes, not controller differences | Comes with V1 |
| V3 | CAN XL has one independent stack (can-utils); python-can, socketcan-rs and Scapy have no CAN XL (13 interop skips) | Fewer oracles for CAN XL than for CAN FD | Add each stack when it gains CAN XL; canbus-interop's weekly upstream check reports new versions |
| V4 | ISO-TP WAIT and OVERFLOW flow controls cannot be provoked in the reference stacks (neither isotprecv nor python-can-isotp sends them); tested against go-canbus's own receiver only | Their handling is not cross-checked | A scripted ISO-TP peer in canbus-interop that answers with WAIT/OVFLW on request |
| V5 | ISO-TP timing (STmin gaps, N_Bs/N_Cr) is exercised, not measured, in the interop suite | A timing regression would pass | Measure frame gaps on the kernel side (timestamps on a monitor socket) in the go-canbus tests |
| V6 | TRC 1.2 has no independent reader; python-can reads TRC 1.0, 1.1, 1.3, 2.0, 2.1 | 1.2 is checked against this package's own writer only | PEAK's PCAN-View files, if samples can be obtained |
| V7 | Ubuntu 24.04's tools are too old for CAN XL: Wireshark 4.2 cannot dissect it, libpcap 1.10.4 alters CAN FD flags and the CAN XL SDT bit 2 | CI installs Wireshark from its PPA and accounts for the old libpcap; the exact libpcap check runs only in the Lima VM (libpcap 1.10.6) | Move CI to a runner image with libpcap ≥ 1.10.6 when one exists |

## Known differences with other stacks

The interop matrix records 132 known differences per architecture
(canbus-interop `tests/known-differences.tsv`, go-canbus
[INTEROPERABILITY.md](INTEROPERABILITY.md)). None is a go-canbus defect;
each is a choice where DBC or ARXML have no normative answer:

- **Multiplexer values without a branch** (cantools refuses to decode or
  encode them; go-canbus decodes the static signals).
- **Extended multiplexing**: canmatrix 1.3.3 cannot encode it.
- **ARXML multiplexed PDUs**: canmatrix 1.3.3 reads only the selector;
  cantools and go-canbus read the static and dynamic parts.
- **ARXML PDU byte offset in a frame**: go-canbus applies it (AUTOSAR);
  cantools 45 and canmatrix 1.3.3 place the PDU at byte 0.
- **Data longer than the message**: canmatrix rejects the frame.
- **python-can traces and slcan**: python-can reads a classical DLC 9..15 in
  ASC as a length and a 0-byte CAN FD frame as remote; it reads TRC FE
  records as bit-rate switched; its slcan interface sends CAN FD frames
  with ESI without the flag (go-canbus's slcan refuses ESI rather than drop
  it).

Follow-up: report the cantools and canmatrix ARXML PDU-offset handling
upstream (an issue with the `pdu-offset.arxml` fixture), and the python-can
TRC/slcan ESI readings.

## Follow-ups, suggested order

1. **J1939 and UDS modules** on `canbus.Bus` and `isotp` (gap 2, with the
   ISO-TP functional addressing helper of gap 7): the step from frames to
   applications most users need.
2. **Hardware bench and self-hosted runner** (V1, V2): the only way to
   verify bit timing, error handling and bus-off.
3. **Vendor transports** (gap 1): PCAN-Basic first (the most common adapter
   on Windows), in its own module.
4. **ISO-TP WAIT/OVFLW peer** in canbus-interop (V4) and **timing
   measurement** (V5).
5. **ARXML container and secured PDUs** (gap 4), driven by real files.
6. **Upstream reports** for the known differences above.

## Housekeeping

- go-canbus **v0.6.0** contains only the go-serial v0.1.8 update. It was
  published by a release run meant for the CAN XL work, which became
  v0.7.0; its notes say so. **v0.6.1** is a second tag on the same commit,
  with empty release notes.
- docs/WEBSITE-INTEGRATION.md (the brief for otfabric.io) still says "no
  CAN XL; no ARXML yet": refresh it when the site is updated.
- canbusctl gaps: [canbusctl ROADMAP.md](https://github.com/otfabric/canbusctl/blob/main/ROADMAP.md).
  canbus-interop gaps: [canbus-interop LIMITATIONS.md](https://github.com/otfabric/canbus-interop/blob/main/LIMITATIONS.md).
