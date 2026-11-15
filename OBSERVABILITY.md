# Observability

## Logging

`canbus.WithLogger(*slog.Logger)` and `isotp.Config.Logger`. Nil logs
nothing. Entries are constant messages with fields:

| Message | Level | Fields |
|---------|-------|--------|
| `canbus: state changed` | info | interface, from, to |
| `canbus: receive failed` | warn | interface, error |
| `canbus: frames lost` | warn | interface, reason, count |
| `isotp: send failed` / `receive failed` | debug | tx / rx, size, error |
| `isotp: receive queue full, message dropped` | warn | rx |
| `isotp: flow control not sent` | warn | error |
| `isotp: unexpected flow control dropped` | debug | rx, status |
| `isotp: flow control frames dropped` | warn | rx |
| `canbus: callback did not return normally` / `isotp: callback did not return normally` | error | callback (`Metrics.OnFrameReceived`, ...), value, stack |

## Metrics

`canbus.Metrics` (embed `NopMetrics`), called synchronously and
concurrently; must not block:

| Callback | Meaning |
|----------|---------|
| `OnFrameSent(iface, f)` | frame handed to the transport (count, `len(f.Payload())` bytes) |
| `OnFrameReceived(iface, f)` | every received frame before subscription filtering |
| `OnDrop(iface, reason, n)` | `queue-full` (a subscription), `kernel-overflow` (SO_RXQ_OVFL), `fault` |
| `OnError(iface, op, err)` | failed send or receive |
| `OnQueue(iface, depth, capacity)` | after queuing for a subscription (utilization) |
| `OnStateChange(iface, from, to)` | `active`, `warning`, `passive`, `bus-off` (from error frames), `down`, `closed` |

`isotp.Metrics`: `OnMessageSent(size, duration)`,
`OnMessageReceived(size, duration)`, `OnError(op, err)`.

### The callback contract

| | `canbus.Metrics` (and `XLMetrics`) | `isotp.Metrics` |
|---|---|---|
| Called on the reader goroutine, in order | `OnFrameReceived`, `OnXLFrameReceived`, `OnDrop`, `OnQueue`, `OnError` for `receive`, the `OnStateChange` that follows a frame or a receive error | `OnMessageReceived`, `OnError` for `receive` |
| Called on the goroutine of the caller | `OnFrameSent`, `OnXLFrameSent`, `OnError` for `send` (the goroutine that called `Send`); the `OnStateChange` of `NewBus` and of `Close` | `OnMessageSent`, `OnError` for `send`, after the transfer |
| A callback that waits | holds up every subscription of the bus (the reader delivers after it) | holds up reception, and the flow controls of a transfer in progress |
| May call | the other methods of the bus and its subscriptions: `Stats`, `State`, `Err`, `Subscribe`, `Send`, a subscription's `Close` | on the reader goroutine: `Send` of a message that fits a single frame |
| Must not call | `Bus.Close`: it waits for the reader goroutine, which waits for the callback | on the reader goroutine: `Conn.Close` (the same), `Send` of a longer message (its flow control is read by that goroutine), `Receive` without a deadline |
| Closing from a callback | `go bus.Close()`: that `Close` completes once the callback has returned | `go conn.Close()`, likewise |
| A callback that does not return normally | is stopped where the bus calls it and logged at error level with the value it ended with and the stack (`WithLogger`); the bus goes on as if it had returned: the frame is delivered, `Send` returns its result | the same, to `Config.Logger` |

The tests: `TestCallbackCallsBus`, `TestCallbackClosesBus`,
`TestCallbackFailureLogged` (package `canbus`), `TestCallbackSendsReply`,
`TestCallbackClosesConn`, `TestCallbackFailureLogged` (package `isotp`).
`NopMetrics` is called as it is, without the guard.

Counters without callbacks: `Bus.Stats()`, `Subscription.Received()` /
`Dropped()`, `socketcan.Conn.Stats()` / `Drops()`, `virtual.Bus.FaultStats()`,
and the kernel's view through `link.Get` (interface statistics, error
counters, CAN device statistics).
