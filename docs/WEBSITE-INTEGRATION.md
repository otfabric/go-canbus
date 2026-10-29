# otfabric.io integration proposal

A proposal for adding the CAN projects to otfabric.io
(`otfabric/website`). Nothing here has been applied to the website. Current
as of go-canbus v0.7.0, canbusctl v0.6.0 and canbus-interop v0.5.0
(2026-10-29).

## Positioning

| Topic | Place on the site | Notes |
|-------|-------------------|-------|
| CAN bus / CAN FD / CAN XL | Industrial fieldbus and transport technologies, next to Modbus RTU and PROFINET | A data-link technology: frames, identifiers, arbitration. Not an application protocol. CAN XL (ISO 11898-1:2024) carries up to 2048 bytes. |
| ISO-TP (ISO 15765-2) | Transport protocols, next to COTP/TPKT | Segmentation of messages up to 4095 bytes (4 GiB with the escape) over CAN |
| DBC, KCD, ARXML | Information models and message definitions, next to SunSpec models and IEC 61850 SCL | Database formats describing messages and signals. **Not network protocols.** |
| CANopen, J1939, UDS | Future higher-level protocol implementations (roadmap) | Planned as separate modules on top of `canbus.Bus` and `isotp` |

## Repository descriptions and topics

| Repository | Description | Topics |
|------------|-------------|--------|
| go-canbus | Pure Go CAN, CAN FD and CAN XL: Linux SocketCAN, slcan adapters on every OS, a virtual bus, ISO-TP, capture and replay (candump, JSON Lines, pcap/pcapng, ASC, BLF, TRC), DBC/KCD/ARXML codec and Go code generator | `can-bus`, `canbus`, `can-fd`, `can-xl`, `socketcan`, `slcan`, `iso-tp`, `iso15765`, `dbc`, `kcd`, `arxml`, `autosar`, `pcap`, `code-generation`, `golang`, `industrial-automation`, `automotive`, `ot-security`, `otfabric` |
| canbus-interop | Containerized independent CAN implementations (can-utils, python-can, socketcan-rs, Scapy, python-can-isotp, cantools, canmatrix) with one contract, for interoperability testing on vcan | `can-bus`, `socketcan`, `vcan`, `can-xl`, `interoperability`, `iso-tp`, `dbc`, `arxml`, `docker`, `testing`, `otfabric` |
| canbusctl | CAN command-line toolkit: monitor, send, record, replay, trace conversion, DBC/KCD/ARXML decode/encode/generate, ISO-TP, CAN XL, fault injection and virtual simulation with safety guards | `can-bus`, `cli`, `socketcan`, `slcan`, `candump`, `can-xl`, `dbc`, `iso-tp`, `golang`, `otfabric` |

## Proposed `src/data/projects.json` entries

```json
{
  "id": "go-canbus",
  "name": "go-canbus",
  "repo": "go-canbus",
  "category": "library",
  "protocols": ["can", "isotp", "dbc"],
  "status": "available",
  "install": "go get github.com/otfabric/go-canbus",
  "description": "Classical CAN, CAN FD and CAN XL frames; Linux SocketCAN with kernel filters and zero-allocation I/O; serial-line (slcan) adapters on every OS; a deterministic virtual bus with fault injection; ISO-TP in userspace and through the kernel; capture and replay in candump, JSON Lines, pcap/pcapng, Vector ASC/BLF and PEAK TRC; DBC/KCD/ARXML databases with an exact signal codec and a Go code generator.",
  "icon": "go-canbus"
}
```

```json
{
  "id": "canbusctl",
  "name": "canbusctl",
  "repo": "canbusctl",
  "category": "cli",
  "protocols": ["can", "isotp", "dbc"],
  "status": "available",
  "install": "go install github.com/otfabric/canbusctl@latest",
  "description": "Monitor, send, record and replay CAN, CAN FD and CAN XL traffic on SocketCAN, slcan adapters or a virtual bus; convert traces (candump, JSON Lines, pcap/pcapng, ASC, BLF, TRC); decode and encode with DBC, KCD or ARXML and generate Go; run ISO-TP; inject faults. Transmissions on real buses need --yes and are rate- and count-limited.",
  "icon": "canbusctl"
}
```

```json
{
  "id": "canbus-interop",
  "name": "canbus-interop",
  "repo": "canbus-interop",
  "category": "interop",
  "protocols": ["can", "isotp", "dbc"],
  "status": "available",
  "install": "docker pull ghcr.io/otfabric/canbus-interop-canutils",
  "description": "Seven independent CAN, ISO-TP and DBC/ARXML implementations in containers with one contract (CAN, CAN FD, CAN XL, slcan, trace formats), for interoperability tests on Linux vcan.",
  "icon": "canbus-interop"
}
```

## Proposed `ecosystem.json` protocol entries

```json
{ "id": "can", "name": "CAN / CAN FD / CAN XL", "standard": "ISO 11898-1 (Bosch CAN 2.0, CAN FD 1.0; CAN XL in the 2024 edition)",
  "focus": "Data link: classical, FD and XL frames, SocketCAN", "maturity": "Very mature (CAN XL: emerging)", "commercial": "Very established",
  "summary": "Ubiquitous in vehicles and machines; the open-source landscape centres on Linux SocketCAN with can-utils, python-can and language bindings. CAN XL is in Linux since 6.2." }
{ "id": "isotp", "name": "ISO-TP", "standard": "ISO 15765-2",
  "focus": "Transport segmentation over CAN", "maturity": "Mature", "commercial": "Established",
  "summary": "In the Linux kernel since 5.10, with userspace implementations in Python, C and Rust; the carrier of UDS diagnostics." }
{ "id": "dbc", "name": "DBC", "standard": "Vector de facto format (no public specification)",
  "focus": "Message and signal definitions (information model, not a protocol)", "maturity": "Mature", "commercial": "Very established",
  "summary": "cantools and canmatrix are the reference open-source parsers; KCD and AUTOSAR ARXML are the main alternatives." }
```

## Feature summary for a protocol page

**What it does**: CAN and CAN FD frames with explicit 11/29-bit identifiers,
BRS, ESI, remote and error frames, and CAN XL frames (priority, VCID, SDU
type, acceptance field, up to 2048 bytes); Linux SocketCAN (filters,
loopback, timestamps, drop accounting, cancellation); interface
administration via netlink; slcan (LAWICEL) USB adapters on Linux, macOS and
Windows; a virtual bus for tests on every OS; ISO-TP with every addressing
mode, flow control and CAN FD; candump, JSON Lines, pcap, pcapng, Vector ASC
and BLF, and PEAK TRC traces with replay; DBC and KCD read and write, AUTOSAR
4 ARXML read, with Intel/Motorola, signed, float, scaling, value tables,
simple and extended multiplexing, exact 64-bit raw values; typed Go
generated from a database; a fault-injecting bridge for testing real stacks.

**How it is tested**: against the Linux kernel (CAN_RAW, ISO-TP, slcan
driver; kernels 6.1 to 6.18 in microVMs, and 7.0), can-utils, python-can,
socketcan-rs, Scapy, python-can-isotp, cantools and canmatrix on vcan (315
interop results per architecture: 170 pass, 132 documented differences
between DBC/ARXML tools, 13 capability skips, 0 failures), traces against
tcpdump/libpcap captures and Wireshark; plus race, fuzz, chaos-monkey and
end-to-end tests; ≥ 90 % coverage (93 %).

**Limitations to state**: no ISO or CiA conformance claim (the standards are
not public); SocketCAN only on Linux, slcan adapters everywhere, no vendor
adapters (PEAK, Kvaser, Vector) yet; CAN XL on SocketCAN (Linux ≥ 6.2) and
the virtual bus, not over slcan or ISO-TP; ARXML read-only, CAN subset;
vcan proves interoperability of frames and protocol logic, not electrical
behaviour or timing, and nothing has been tested on real CAN hardware; a
CAN identifier names a message, not a device: no device discovery.

**Roadmap**: CANopen, SAE J1939 and UDS as separate libraries on top of
go-canbus; vendor adapter transports; validation on real hardware. Details:
[ROADMAP.md](../ROADMAP.md).
