# Errors

Sentinels are wrapped with detail: test with `errors.Is`, extract
structure with `errors.As`.

| Error | Package | When |
|-------|---------|------|
| `ErrClosed` | canbus | Operation on a closed bus, subscription, transport or ISO-TP connection; ends blocked calls on Close |
| `ErrTimeout` | canbus | A transport deadline passed outside a context deadline (a context's own deadline returns `context.DeadlineExceeded`) |
| `ErrInvalidFrame` | canbus | `Frame.Validate` failed; `*FrameError` carries frame and reason |
| `ErrUnsupported` | canbus | SocketCAN off Linux, FD frame on a classical socket, non-CAN interface, vcan bit timing, kernel ISO-TP options the kernel lacks |
| `ErrQueueFull` | canbus | A bounded queue rejected a frame |
| `ErrInvalidOption` | canbus | Bad constructor or Config value, bad filter text |
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
| `ErrUnknownMessage`, `ErrUnknownSignal`, `ErrShortPayload`, `ErrRange`, `ErrMissingSignal`, `ErrInactiveSignal`, `ErrLayout` | candb | Codec failures |
| `*ParseError` | dbc, kcd | Parse with error diagnostics (database and all diagnostics are still returned) |
| `*ParseError`, `ErrUnknownFormat` | capture | Malformed trace record (format and record number), undetectable format |
