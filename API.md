# API

The public API by package. GoDoc ([pkg.go.dev](https://pkg.go.dev/github.com/otfabric/go-canbus))
has every symbol; this is the map.

## canbus

| Symbol | Purpose |
|--------|---------|
| `Frame`, `ID`, `Flags` (`FlagExtended`, `FlagRemote`, `FlagFD`, `FlagBRS`, `FlagESI`, `FlagError`) | One frame as a value; `Payload`, `SetPayload`, `DLC`, `Validate`, `String` (cansend notation) |
| `NewStandard`, `NewExtended`, `NewFD`, `NewRemote` | Validated constructors |
| `DLCToLen`, `LenToDLC`, `ValidFDLen`, `RoundUpFDLen`, `PadFD` | Data length codes |
| `Filter`, `FilterSet`, `ExactStandard`, `ExactExtended`, `ParseFilter`, `KernelID` | Identifier filters with `CAN_RAW_FILTER` semantics; `Filter.Kernel()` gives the kernel's `can_filter` |
| `ErrorInfo`, `ParseError`, `NewErrorFrame`, `ErrorClass`, `ControllerStatus`, `ProtocolViolation` | Error frames |
| `Transport` (+ optional `Filterer`, `Statser`, `DropCounter`, `Namer`, `Capabilitier`) | The transport contract |
| `Bus`, `NewBus`, `WithLogger`, `WithMetrics`, `WithInterfaceName`, `WithStartOnSubscribe` | One transport shared by many users |
| `Subscription`, `WithFilters`, `WithQueueSize`, `WithOverflow` (`DropNewest`, `DropOldest`, `Block`), `WithoutEcho` | Filtered, bounded receive queues |
| `Metrics`, `NopMetrics`, `DropReason`, `State`, `Stats` | Observability |
| Errors | see [ERRORS.md](ERRORS.md) |

Concurrency: `Bus.Send` from any goroutine; one `Subscription.Receive`
caller per subscription; `Subscribe`/`Close` any time. A `Transport`
allows concurrent `Send` and one `Receive` at a time. `Close` unblocks
everything with `ErrClosed`.

## socketcan (Linux)

`Open(ifname, ...Option) (*Conn, error)` with `WithFD`, `WithFilters`,
`WithoutLoopback`, `WithRecvOwn`, `WithoutTimestamps`,
`WithReceiveBuffer`, `WithSendBuffer`. `Conn` implements `Transport`,
`Filterer` (`SetFilters`), `Statser`, `DropCounter`, `Namer`,
`Capabilitier`. `"any"` binds to every CAN interface (receive only).
`DialISOTP(ifname, isotp.Config) (*ISOTPConn, error)`: kernel ISO-TP.
`Supported()` reports the platform.

## socketcan/link (Linux)

`List`, `Get` → `Interface` (kind, state, MTU, bit timing, data bit
timing, clock, control modes, restart-ms, error counters, statistics);
`SetUp`, `SetDown`, `SetMTU`, `Configure(Config{Bitrate, SamplePoint,
DataBitrate, DataSamplePoint, Set, Clear, RestartMs})`, `Restart`,
`AddVCAN`, `AddVXCAN`, `Delete`.

## slcan

| Identifier | Purpose |
|------------|---------|
| `Open(device, baud, Config)` | A serial adapter through go-serial (115200 baud by default; USB adapters ignore it) |
| `New(io.ReadWriteCloser, Config)` | slcan over any byte stream: a pseudo-terminal, a TCP connection to a serial server |
| `Config` (`Bitrate`, `DataBitrate`, `ListenOnly`, `NoCommands`, `Name`, `QueueSize`), `Bitrates`, `DataBitrates` | Setup: "C", "S0".."S9", "Y2"/"Y5", then "O" or "L"; `NoCommands` for peers that are not adapters |
| `Conn` | `canbus.Transport` with `Name`, `Capabilities`, `Stats`, `Drops`, `SetFilters` (software; error frames only with an `ErrorMask`, as in SocketCAN) and `State` (controller state and error counters of the last "s" record); a lost device is `canbus.ErrInterfaceDown` |
| `AppendFrame`, `ParseFrame` | The record codec ("t", "T", "r", "R", "d", "D", "b", "B") |
| `ParseErrorRecord`, `ParseStateRecord`, `StateFrame` | Adapter status: "e" (bus errors) and "s" (controller state, error counters) records, decoded into the error frames the Linux slcan driver delivers |

## virtual

`New(Config{Name, Seed, QueueSize, Clock})` → `*Bus` with `Open(...PortOption)`
(`WithFD`, `WithRecvOwn`, `WithoutLoopback`, `WithQueueSize`,
`WithBlocking`, `WithFilters`), `SetFaults(Faults{Drop, Duplicate, Delay,
DelayBy, Corrupt, DropNext})`, `FaultStats`, `SetUp`, `Inject`,
`InjectRaw`, `InjectError`, `Close`.

## isotp

`Dial(*canbus.Bus, Config) (*Conn, error)`; `Conn.Send`, `Receive`,
`Close`; `Endpoint` (also implemented by `socketcan.ISOTPConn`). `Config`:
`TxID`, `RxID`, `ExtendedIDs`, `Addressing` (`Normal`, `Extended`,
`Mixed`), `TxAddress`, `RxAddress`, `FD`, `TxDL`, `BRS`, `BlockSize`,
`STmin`, `ForceTxSTmin`, `Padding`, `PadByte`, `TimeoutBs`, `TimeoutCr`,
`MaxWait`, `MaxMessageSize`, `SendFlowWait`, `QueueSize`, `Logger`,
`Metrics`. `EncodeSTmin`, `DecodeSTmin`.

## capture

`Reader`, `Writer`, `NewReader` (format detection), `NewWriter`;
`NewCandumpReader`/`Writer`, `ParseCandumpLine`, `ParseFrame`,
`FormatFrame`; `NewJSONLReader`/`Writer`, `Record`, `ToRecord`;
`NewPCAPReader`/`Writer`, `NewPCAPNGReader`; `NewASCReader`/`Writer`,
`NewBLFReader`/`Writer`, `NewTRCReader`/`Writer`; `FormatFromPath`; `Replay(ctx, Reader,
Sender, ReplayOptions) (ReplayStats, error)`; `RecordFrames`.

## candb, dbc, kcd, codegen

`candb.Database`, `Message`, `Signal`, `ValueDescription`, `ValueTable`,
`AttributeDefinition`, `Attribute`, `MuxRange`; `Message.Decode`,
`Encode`, `EncodeWith(EncodeOptions{Lax, FillMissing})`, `Frame`;
`Signal.Extract`, `Insert`, `Value`, `Decode`, `RawFromInt`,
`RawFromUint`, `RawFromPhysical`, `RawString`; inputs `Phys`, `Raw`,
`RawInt`, `Label`; `Database.DecodeFrame`, `Validate` → `[]Diagnostic`.
`candb/bits`: the bit-field runtime of generated code.

`dbc.Parse`, `ParseBytes`, `ParseFile`, `Write`; `kcd.Parse`,
`ParseFile`, `Write` (returns diagnostics for what KCD cannot carry);
`arxml.Parse`, `ParseFile`. Parsers return `(db, diagnostics, err)`, `err`
a `*ParseError` when any diagnostic is an error. `candb.PhysText` encodes a
physical value given as decimal text (exact for 64-bit identity signals).

`codegen.Generate(db, Options{Package, Source, SourceSHA256})`,
`GenerateJSON`. Generated packages expose per message `<Msg>ID`,
`<Msg>Length`, `type <Msg> struct`, `Encode`, `Decode`, `Frame`,
`<Sig>Phys`, `Set<Sig>Phys`, enums `<Msg><Sig>` with `String`, and
`New`, `Decode`, `Message`, `ErrRange`, `ErrLength`, `ErrUnknownMessage`.

## chaos

`New(Config{Seed, Drop, Duplicate, Delay, DelayBy, Reorder, Corrupt,
Filter})` → `*Bridge`; `Run(ctx, src, dst)` forwards one direction,
`Both(ctx, x, y)` both; `Stats()`.
