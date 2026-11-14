# Errors

Sentinels are wrapped with detail: test with `errors.Is`, extract
structure with `errors.As`.

| Error | Package | When |
|-------|---------|------|
| `ErrClosed` | canbus | Operation on a closed bus, subscription, transport or ISO-TP connection; ends blocked calls on Close |
| `ErrTimeout` | canbus | A transport deadline passed outside a context deadline (a context's own deadline returns `context.DeadlineExceeded`) |
| `ErrInvalidFrame` | canbus | `Frame.Validate` failed; `*FrameError` carries frame and reason |
| `ErrUnsupported` | canbus | SocketCAN off Linux, FD frame on a classical socket, non-CAN interface, vcan bit timing, kernel ISO-TP options the kernel lacks (`socketcan.DialISOTP` with `MaxWait` other than 0 or 10, or `TimeoutBs`/`TimeoutCr` other than 0 or 1 s) |
| `ErrQueueFull` | canbus | A bounded queue rejected a frame |
| `ErrInvalidOption` | canbus | Bad constructor or Config value, bad filter text; `socketcan.DialISOTP` ForceTxSTmin outside 0..127 ms or MaxMessageSize above 1025 KiB; `link.SetMTU` MTU ≤ 0 or ≥ 2^32; `link.AddVCAN`/`AddVXCAN` name empty or over 15 bytes |
| `ErrInterfaceDown` | canbus | Interface missing or down (ENETDOWN, ENODEV); a Bus reader reports it and recovers when the interface returns |
| `ErrBufferOverflow` | canbus | ENOBUFS beyond a send's context |
| `*OpError` | canbus | Operation, interface and cause of a transport failure |
| `ErrTimeout` | isotp | N_Bs (no flow control) or N_Cr (no consecutive frame); kernel ECOMM/ETIMEDOUT |
| `ErrOverflow` | isotp | Receiver answered OVERFLOW; kernel EMSGSIZE |
| `ErrWaitLimit` | isotp | More WAIT flow controls than `MaxWait` |
| `ErrSequence` | isotp | Wrong consecutive frame sequence number; kernel EILSEQ |
| `ErrTooLarge` | isotp | Beyond `MaxMessageSize` (send or announced by a first frame) |
| `ErrInterrupted` | isotp | A new single/first frame replaced a reception |
| `ErrProtocol` | isotp | Invalid flow control; kernel EBADMSG |
| `ErrUnknownMessage`, `ErrUnknownSignal`, `ErrShortPayload`, `ErrRange`, `ErrMissingSignal`, `ErrInactiveSignal`, `ErrLayout` | candb | Codec failures; `ErrLayout` also for a signal start bit beyond 2^24, a negative message length, or one beyond the CAN XL (64 for CAN FD) payload |
| `*ParseError` | dbc, kcd, arxml | Parse with error diagnostics (database and all diagnostics are still returned); in dbc also a message identifier above 32 bits in any statement; in arxml also the nesting, signal and value bounds (LIMITATIONS.md) |
| `ErrName` | dbc | `Write`: a name in an identifier position (node, value table, message, sender, signal, receiver, multiplexer, signal group) is not a DBC identifier, or a node or receiver name is a DBC keyword; nothing is written |
| `*ParseError`, `ErrUnknownFormat` | capture | Malformed trace record (format and record number), including a timestamp outside the format's or `time.Duration`'s range; undetectable format |
| `ErrUnrepresentable` | capture | A writer cannot store a frame without corrupting the file (timestamp outside the format's range, pcapng interface name over 65535 bytes, BLF CAN FD channel over 255, candump interface name containing a space, control byte or DEL); nothing is written for that frame |
