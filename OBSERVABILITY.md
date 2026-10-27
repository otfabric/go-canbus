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

Counters without callbacks: `Bus.Stats()`, `Subscription.Received()` /
`Dropped()`, `socketcan.Conn.Stats()` / `Drops()`, `virtual.Bus.FaultStats()`,
and the kernel's view through `link.Get` (interface statistics, error
counters, CAN device statistics).
