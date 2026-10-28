# Architecture

go-canbus is a pure Go (no cgo) CAN library. It is layered so that each layer
can be used, tested and replaced on its own, and so that higher-level
protocols (CANopen, J1939, UDS, ...) can be added later as separate modules
without changes to the core.

```
 ┌──────────────────────────────────────────────────────────────────────────┐
 │ applications: canbusctl, generated DBC code, future go-canopen/go-j1939  │
 ├───────────────────────┬──────────────────────┬───────────────────────────┤
 │ candb  (message model,│ isotp (ISO 15765-2,  │ capture (candump, JSONL,  │
 │  signal codec)        │  userspace)          │  pcap; replay)            │
 │ dbc / kcd (parsers)   │ socketcan.ISOTP      │                           │
 │ codegen (Go output)   │  (kernel CAN_ISOTP)  │                           │
 ├───────────────────────┴──────────────────────┴───────────────────────────┤
 │ canbus: Frame, ID, Flags, DLC, Filter, ErrorInfo, Bus (fan-out),         │
 │         Transport interface, errors, slog logging, Metrics               │
 ├──────────────────────────────┬───────────────────────────────────────────┤
 │ socketcan (linux): CAN_RAW,  │ virtual: deterministic in-memory bus with │
 │  CAN FD, kernel filters,     │  fault injection                          │
 │  timestamps, overflow; link  │ slcan: serial-line CAN adapters (any OS,  │
 │  (netlink admin, separate)   │  serial port, pty or TCP)                 │
 └──────────────────────────────┴───────────────────────────────────────────┘
```

## Layers kept apart

| Concern | Where | Not here |
|---------|-------|----------|
| Physical interface, bit rate, controller state | `socketcan/link` (explicit administrative API) | Opening a bus never reconfigures an interface |
| Classical CAN / CAN FD data-link frames | `canbus.Frame` | Signals, transport segmentation |
| ISO-TP segmentation (ISO 15765-2) | `isotp`, `socketcan.DialISOTP` | UDS services |
| Message/signal layout | `candb` (IR), `dbc`, `kcd`, `codegen` | DBC is a database format, not a protocol |
| Application protocols | future modules | CANopen, J1939, UDS, NMEA 2000 are out of scope |

A CAN identifier names a message, not a device. Nothing in this module
claims to identify a physical node from traffic.

## Package `canbus`

### Frame

```go
type Frame struct {
    ID        ID        // 11 or 29 bit identifier, without flag bits
    Flags     Flags     // Extended, Remote, FD, BRS, ESI, Error
    Len       uint8     // payload length in bytes (0..8 classical, 0..64 FD)
    Len8DLC   uint8     // classical frames: raw DLC 9..15 when Len is 8, else 0
    Data      [64]byte  // payload; bytes beyond Len are zero
    Timestamp time.Time // receive time (kernel or transport), zero if unknown
    Interface string    // interface the frame was received on, if known
    Echo      bool      // the frame is this socket's own transmission (recv own)
}
```

The fixed array keeps a frame a plain value: sending and receiving do not
allocate (see `BenchmarkFrame*`; a `[]byte` payload variant allocated on
every receive and was rejected). Methods: `Payload()`, `DLC()`, `Validate()`,
`IsExtended()`, ...; constructors `NewStandard`, `NewExtended`, `NewFD`,
`NewRemote`. `DLCToLen` / `LenToDLC` implement the CAN FD discrete mapping
(0–8, 12, 16, 20, 24, 32, 48, 64).

Error frames (`FlagError`) carry the Linux error class in `ID` and detail
bytes in `Data`; `ParseError(f)` turns them into an `ErrorInfo`. They are a
local controller report, never transmitted.

### Transport and Bus

```go
type Transport interface {
    Send(ctx context.Context, f *Frame) error
    Receive(ctx context.Context, f *Frame) error
    Close() error
}
```

A Transport is one endpoint: a SocketCAN socket or a virtual port.
Implementations are safe for one concurrent `Send` caller per goroutine and
one `Receive` caller at a time; `Close` unblocks both with `ErrClosed`.
Optional interfaces: `Filterer` (kernel filters), `Statser` (counters),
`Capabilities()`.

`Bus` wraps one Transport for many users: a single reader goroutine fans
received frames out to `Subscription`s, each with its own software filter
set and bounded queue (overflow policy: drop newest (default), drop oldest
or block). `Bus.Send` is safe for concurrent use. Subscriptions may be added
and closed at any time; closing the Bus closes all subscriptions and the
Transport. Ownership: the Bus owns its Transport after `NewBus`.

### Filters

`Filter{ID, Mask, Format, Remote, Invert}` with `FilterSet{Filters, Join}`
has exactly the semantics of Linux `CAN_RAW_FILTER` (OR of filters, or AND
with `CAN_RAW_JOIN_FILTERS`; `CAN_INV_FILTER` per filter). The software
matcher computes the kernel's `(can_id & mask) == (id & mask)` on the same
flag-encoded values, so a filter means the same in software and kernel.
Error frames are selected separately (`ErrorMask`), as in the kernel.

### Errors, logging, metrics

Sentinels (`ErrClosed`, `ErrTimeout`, `ErrInvalidFrame`, `ErrUnsupported`,
`ErrQueueFull`, `ErrInvalidOption`, `ErrInterfaceDown`, `ErrBufferOverflow`)
are wrapped with detail; `*FrameError` and `*OpError` carry structure. Test
with `errors.Is` / `errors.As`.

Logging uses `*slog.Logger` (nil = silent). `Metrics` is a callback
interface (frames/bytes sent and received, drops by reason, queue depth,
errors, state changes, ISO-TP sessions); embed `NopMetrics`.

## Package `socketcan` (Linux)

`Open(ifname, opts...)` creates a `CAN_RAW` socket bound to the interface:
optional CAN FD (`CAN_RAW_FD_FRAMES`), kernel filters, error mask,
loopback and receive-own (`CAN_RAW_LOOPBACK`, `CAN_RAW_RECV_OWN_MSGS`),
`SO_TIMESTAMPNS` timestamps and `SO_RXQ_OVFL` drop counter. The descriptor
is non-blocking and registered with the Go runtime poller through
`os.NewFile`/`syscall.RawConn`, so `Receive` and `Send` block in the poller
and are cancelled by context or `Close` without a reader goroutine. Uses only
`syscall`; no cgo, no third-party module. On other systems the package
compiles and `Open` returns `ErrUnsupported`.

`socketcan.DialISOTP` opens a kernel `CAN_ISOTP` socket (Linux ≥ 5.10) with
the same `isotp.Config` as the userspace implementation.

`socketcan/link` reads and changes interface configuration through
rtnetlink: list CAN interfaces, state, bit timing, FD data bit timing,
control mode, error counters; set up/down, bit rate, FD, restart-ms;
create/delete `vcan`/`vxcan`. It is a separate administrative API and needs
`CAP_NET_ADMIN` for changes.

## Package `virtual`

A deterministic in-memory bus for tests and simulation. `virtual.New(cfg)`
returns a Bus; `Open()` returns ports (Transports). All frames go through
one ordered delivery queue, so every port sees the same global order.
Semantics follow SocketCAN: loopback to other ports on by default,
receive-own off by default, non-FD ports neither send nor receive FD
frames. Per-port queue size and overflow policy. Fault injection with a
seeded PRNG: drop, duplicate, delay, corrupt, error frames, port close; plus
scripted `Inject`. It models frame delivery only: no arbitration, bit
timing, error confinement or electrical behaviour.

## Package `isotp`

ISO 15765-2 in userspace on top of a `canbus.Bus`: normal, normal-fixed
(29-bit), extended and mixed addressing; SF/FF/CF/FC; block size, STmin
(including 100–900 µs values), FC wait with WFTmax, overflow; sequence
wrap-around; N_As/N_Bs/N_Cr timeouts; padding; classical and CAN FD
(TX_DL 8–64, SF escape, FF escape for > 4095 bytes). Bounded by
`MaxMessageSize`. One goroutine per connection drives the state machine.

## Package `capture`

Streaming readers/writers: candump log (`(ts) iface ID#DATA`, `##F` FD,
`R` remote, error frames), JSON Lines (lossless, schema
`schemas/capture-v1.schema.json`) and pcap with
`LINKTYPE_CAN_SOCKETCAN`. `Replay` sends a capture with original, scaled or
maximum timing, filters, frame/duration limits and rate limiting.

## Packages `candb`, `dbc`, `kcd`, `codegen`

`candb` is the format-independent message model (database, nodes,
messages, signals, value tables, multiplexing incl. extended, attributes,
comments) plus the signal codec. Raw values are carried as exact 64-bit
integers; physical values are derived from them. Big-endian (Motorola)
signals use the DBC start-bit convention. `dbc` parses and writes DBC with
diagnostics for anything it cannot represent; `kcd` parses KCD. `codegen`
turns a `candb.Database` into deterministic, gofmt-clean Go with typed
structs, enums, constants and encode/decode functions, importing only the
small `candb/bits` runtime.
