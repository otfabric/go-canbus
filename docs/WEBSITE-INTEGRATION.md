# otfabric.io integration proposal

A proposal for adding the CAN projects to otfabric.io
(`otfabric/website`). Nothing here has been applied to the website.

## Positioning

| Topic | Place on the site | Notes |
|-------|-------------------|-------|
| CAN bus / CAN FD | Industrial fieldbus and transport technologies, next to Modbus RTU and PROFINET | A data-link technology: frames, identifiers, arbitration. Not an application protocol. |
| ISO-TP (ISO 15765-2) | Transport protocols, next to COTP/TPKT | Segmentation of messages up to 4095 bytes (4 GiB with the escape) over CAN |
| DBC (and KCD) | Information models and message definitions, next to SunSpec models and IEC 61850 SCL | A database format describing messages and signals. **Not a network protocol.** |
| CANopen, J1939, UDS | Future higher-level protocol implementations (roadmap) | Planned as separate modules on top of `canbus.Bus` and `isotp` |

## Repository descriptions and topics

| Repository | Description | Topics |
|------------|-------------|--------|
| go-canbus | Pure Go CAN and CAN FD: Linux SocketCAN, virtual bus, ISO-TP, candump/pcap capture and replay, DBC/KCD codec and Go code generator | `can-bus`, `canbus`, `can-fd`, `socketcan`, `iso-tp`, `iso15765`, `dbc`, `kcd`, `code-generation`, `golang`, `industrial-automation`, `automotive`, `ot-security`, `otfabric` |
| canbus-interop | Containerized independent CAN implementations (can-utils, python-can, socketcan-rs, Scapy, python-can-isotp, cantools, canmatrix) with one contract, for interoperability testing on vcan | `can-bus`, `socketcan`, `vcan`, `interoperability`, `iso-tp`, `dbc`, `docker`, `testing`, `otfabric` |
| canbusctl | CAN command-line toolkit: monitor, send, record, replay, DBC decode/encode/generate, ISO-TP and virtual simulation with safety guards | `can-bus`, `cli`, `socketcan`, `candump`, `dbc`, `iso-tp`, `golang`, `otfabric` |

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
  "description": "Classical CAN and CAN FD frames, Linux SocketCAN with kernel filters and zero-allocation I/O, a deterministic virtual bus with fault injection, ISO-TP in userspace and through the kernel, candump/pcap capture and replay, and DBC/KCD databases with an exact signal codec and a Go code generator.",
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
  "description": "Monitor, send, record and replay CAN and CAN FD traffic, decode and encode with DBC/KCD, generate Go, run ISO-TP and simulate virtual buses; transmissions on real buses need --yes and are rate- and count-limited.",
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
  "description": "Seven independent CAN, ISO-TP and DBC implementations in containers with one contract, for interoperability tests on Linux vcan.",
  "icon": "canbus-interop"
}
```

## Proposed `ecosystem.json` protocol entries

```json
{ "id": "can", "name": "CAN / CAN FD", "standard": "ISO 11898-1 (Bosch CAN 2.0, CAN FD 1.0)",
  "focus": "Data link: classical and FD frames, SocketCAN", "maturity": "Very mature", "commercial": "Very established",
  "summary": "Ubiquitous in vehicles and machines; the open-source landscape centres on Linux SocketCAN with can-utils, python-can and language bindings." }
{ "id": "isotp", "name": "ISO-TP", "standard": "ISO 15765-2",
  "focus": "Transport segmentation over CAN", "maturity": "Mature", "commercial": "Established",
  "summary": "In the Linux kernel since 5.10, with userspace implementations in Python, C and Rust; the carrier of UDS diagnostics." }
{ "id": "dbc", "name": "DBC", "standard": "Vector de facto format (no public specification)",
  "focus": "Message and signal definitions (information model, not a protocol)", "maturity": "Mature", "commercial": "Very established",
  "summary": "cantools and canmatrix are the reference open-source parsers; KCD and AUTOSAR ARXML are the main alternatives." }
```

## Feature summary for a protocol page

**What it does**: CAN and CAN FD frames with explicit 11/29-bit identifiers,
BRS, ESI, remote and error frames; Linux SocketCAN (filters, loopback,
timestamps, drop accounting, cancellation); interface administration via
netlink; a virtual bus for tests on every OS; ISO-TP with every addressing
mode, flow control and CAN FD; candump, JSON Lines, pcap and pcapng traces
with replay; DBC and KCD with Intel/Motorola, signed, float, scaling, value
tables, simple and extended multiplexing, exact 64-bit raw values; typed Go
generated from a database.

**How it is tested**: against can-utils, the Linux kernel's ISO-TP,
python-can, socketcan-rs, Scapy, python-can-isotp, cantools and canmatrix on
vcan (120 interop scenarios), plus race, fuzz and fault-injection tests;
≥ 90 % coverage.

**Limitations to state**: no ISO or CiA conformance claim (the standards are
not public); SocketCAN only on Linux, no vendor adapters (PEAK, Kvaser,
Vector) yet; no CAN XL; no ARXML yet; vcan proves interoperability of frames
and protocol logic, not electrical behaviour or timing; a CAN identifier
names a message, not a device: no device discovery.

**Roadmap**: CANopen, SAE J1939 and UDS as separate libraries on top of
go-canbus; ARXML import; vendor adapter transports.
