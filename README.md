# go-canbus - CAN, CAN FD, ISO-TP and CAN Databases for Go

[![Go Version](https://img.shields.io/badge/Go-1.23%2B-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Go Reference](https://pkg.go.dev/badge/github.com/otfabric/go-canbus.svg)](https://pkg.go.dev/github.com/otfabric/go-canbus)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI Status](https://github.com/otfabric/go-canbus/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/go-canbus/actions/workflows/ci.yml)
[![Interop](https://github.com/otfabric/go-canbus/actions/workflows/interop.yml/badge.svg)](https://github.com/otfabric/go-canbus/actions/workflows/interop.yml)
[![Kernels](https://github.com/otfabric/go-canbus/actions/workflows/kernels.yml/badge.svg)](https://github.com/otfabric/go-canbus/actions/workflows/kernels.yml)
[![Code Coverage](https://codecov.io/gh/otfabric/go-canbus/graph/badge.svg)](https://codecov.io/gh/otfabric/go-canbus)

A pure Go library for Controller Area Network: classical CAN and CAN FD
frames, Linux SocketCAN, a deterministic virtual bus, ISO-TP
(ISO 15765-2), capture and replay, and DBC/KCD message databases with a
signal codec and a Go code generator. No cgo, no dependencies outside the
standard library.

| Package | What it does |
|---------|--------------|
| [`canbus`](https://pkg.go.dev/github.com/otfabric/go-canbus) | Frames (11/29-bit IDs, CAN FD with BRS/ESI, remote, error frames, DLC 9..15), the CAN FD DLC table, filters with exact Linux `CAN_RAW_FILTER` semantics, error-frame decoding, the `Transport` interface and `Bus` (one transport shared by many subscriptions with bounded queues), slog logging and metrics hooks |
| [`socketcan`](https://pkg.go.dev/github.com/otfabric/go-canbus/socketcan) | Linux `CAN_RAW` sockets: CAN FD, kernel filters, error frames, loopback and receive-own, kernel timestamps, drop accounting, context cancellation, allocation-free send/receive; kernel ISO-TP sockets (`DialISOTP`) |
| [`socketcan/link`](https://pkg.go.dev/github.com/otfabric/go-canbus/socketcan/link) | rtnetlink administration, kept apart from the transport: list and inspect CAN interfaces (state, bit timing, error counters), up/down, bit rates, CAN FD, restart, vcan/vxcan create/delete |
| [`virtual`](https://pkg.go.dev/github.com/otfabric/go-canbus/virtual) | An in-memory bus for tests and simulation on every OS: SocketCAN delivery rules, queue policies, seeded fault injection (drop, duplicate, delay, corrupt, error frames, interface down) |
| [`isotp`](https://pkg.go.dev/github.com/otfabric/go-canbus/isotp) | ISO-TP in userspace: normal, extended and mixed addressing, flow control with block size, STmin, WAIT and OVERFLOW, timeouts, padding, CAN FD and messages above 4095 bytes |
| [`capture`](https://pkg.go.dev/github.com/otfabric/go-canbus/capture) | candump logs, lossless JSON Lines, pcap and pcapng (`LINKTYPE_CAN_SOCKETCAN`); streaming replay with original, scaled or maximum timing, filters and limits |
| [`candb`](https://pkg.go.dev/github.com/otfabric/go-canbus/candb) | The format-independent message model and signal codec: Intel and Motorola, signed, float, scaling, value tables, simple and extended multiplexing, CAN FD; exact 64-bit raw values |
| [`dbc`](https://pkg.go.dev/github.com/otfabric/go-canbus/dbc), [`kcd`](https://pkg.go.dev/github.com/otfabric/go-canbus/kcd) | DBC parser and writer, KCD parser; every unsupported construct yields a diagnostic |
| [`codegen`](https://pkg.go.dev/github.com/otfabric/go-canbus/codegen) | Deterministic Go generation: typed message structs, enums, constants, physical accessors, multiplexer-aware encode/decode |

The command-line tool built on it is
[canbusctl](https://github.com/otfabric/canbusctl); the independent
reference implementations it is tested against are in
[canbus-interop](https://github.com/otfabric/canbus-interop).

## Install

```bash
go get github.com/otfabric/go-canbus
```

## Quick start

Receive frames from a SocketCAN interface (Linux):

```go
conn, err := socketcan.Open("can0", socketcan.WithFD())
if err != nil {
	log.Fatal(err)
}
bus, _ := canbus.NewBus(conn)
defer bus.Close()

sub, _ := bus.Subscribe(canbus.WithFilters(canbus.FilterSet{
	Filters: []canbus.Filter{canbus.ExactStandard(0x123)},
}))
for {
	f, err := sub.Receive(ctx)
	if err != nil {
		return
	}
	fmt.Println(f) // 123#DEADBEEF
}
```

Send, on any transport (a `virtual` port works the same way):

```go
f, _ := canbus.NewFD(0x18DA10F1, true, true, payload) // 29-bit, CAN FD, BRS
err := bus.Send(ctx, &f)
```

Decode with a DBC file:

```go
db, diags, err := dbc.ParseFile("vehicle.dbc")
msg, signals, err := db.DecodeFrame(&f)
for _, s := range signals {
	fmt.Println(s.Signal.Name, s.Signal.RawString(s.Value), s.Physical, s.Signal.Unit)
}
```

ISO-TP over a bus:

```go
conn, _ := isotp.Dial(bus, isotp.Config{TxID: 0x7E0, RxID: 0x7E8, Padding: true})
err := conn.Send(ctx, request)
response, err := conn.Receive(ctx)
```

More in [examples/](examples): passive monitoring, sending classical and
CAN FD frames, DBC decoding, code generation, an ISO-TP exchange, virtual
simulation with faults, capture and replay, and two processes on vcan.

## What is in scope

| Layer | Scope |
|-------|-------|
| Physical interface, bit rate | Configured explicitly through `socketcan/link`; opening a socket never changes an interface |
| Data link (ISO 11898-1 framing concepts) | Classical CAN 2.0A/B, CAN FD; not CAN XL |
| Transport | ISO-TP (ISO 15765-2), userspace and Linux kernel |
| Message definitions | DBC (read/write), KCD (read); ARXML not yet (see [LIMITATIONS.md](LIMITATIONS.md)) |
| Application protocols | None: CANopen, J1939, UDS, NMEA 2000, ISOBUS and DroneCAN belong in their own modules on top of `canbus.Bus` and `isotp` |

A CAN identifier names a message, not a device. Nothing here identifies a
physical node from traffic.

## Platforms

| | Linux (amd64, arm64, armv7) | macOS, Windows (amd64, arm64) |
|---|---|---|
| Frames, filters, virtual bus, ISO-TP, capture/replay, candb/dbc/kcd/codegen | Yes | Yes |
| SocketCAN, kernel ISO-TP, netlink administration | Yes | No: `ErrUnsupported` |

Hardware adapters other than SocketCAN (PEAK, Kvaser, Vector, slcan
without the kernel driver) are not implemented; they plug in through the
`canbus.Transport` interface.

## Documentation

[API.md](API.md) · [ARCHITECTURE.md](ARCHITECTURE.md) ·
[ERRORS.md](ERRORS.md) · [OBSERVABILITY.md](OBSERVABILITY.md) ·
[INTEROPERABILITY.md](INTEROPERABILITY.md) · [TESTING.md](TESTING.md) ·
[LIMITATIONS.md](LIMITATIONS.md) · [SECURITY.md](SECURITY.md) ·
[REQUIREMENTS.md](REQUIREMENTS.md) · [CHANGELOG.md](CHANGELOG.md) ·
[RELEASE.md](RELEASE.md) · [CONTRIBUTING.md](CONTRIBUTING.md)

## Standards

ISO 11898-1 and ISO 15765-2 are not publicly available. This library is
implemented from public material (the Linux SocketCAN documentation and
headers, the Bosch CAN 2.0 and CAN FD 1.0 specifications, public protocol
descriptions) and tested against independent implementations. It makes
no claim of ISO or CiA conformance.

## License

MIT, see [LICENSE](LICENSE).
