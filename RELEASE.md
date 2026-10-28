# go-canbus Releases

Releases are made with the Release workflow (Actions → Release → bump):
it computes the version, tags it and publishes this file's section of
that version as the release notes. Add the section, headed exactly
`## vX.Y.Z` and closed by a line `---`, before running it.

## v0.2.2

**Date:** 2026-10-28
**Previous release:** v0.2.1

## Summary

A Bus can now start reading at its first subscription, so no frame that
arrives between `NewBus` and `Subscribe` is lost. Found by canbusctl's
end-to-end tests in a slow microVM: `record` saw 21 of 60 frames.

## Additions

- `canbus.WithStartOnSubscribe()`: the bus's reader starts at the first
  `Subscribe` (or `Close`); until then frames wait in the transport (the
  socket's receive buffer). Without it the reader starts in `NewBus`, as
  before, and frames read before the first subscription reach nobody — now
  documented on `Bus`.
- The `monitor` and `capture-replay` examples and the README use it.

---

## v0.2.1

**Date:** 2026-10-28
**Previous release:** v0.2.0

## Summary

Two fixes reported by canbusctl v0.2.0: a socket deadline that fires a
moment before its context's deadline now ends calls the way the context
does.

## Fixes

- `chaos.Bridge.Run` returns nil when ctx ends, also when the transport's
  deadline fired first (it returned a deadline error) and when ctx ends
  during a send.
- `socketcan.ISOTPConn` reports such a deadline as `context.DeadlineExceeded`
  (or `canbus.ErrTimeout`), not as an `*OpError` "i/o timeout".

---

## v0.2.0

**Date:** 2026-10-28
**Previous release:** v0.1.0

## Summary

More formats in and out, and much deeper testing. Vector ASC and BLF traces,
AUTOSAR 4.x ARXML databases and KCD output join the codecs; a new `chaos`
package bridges two transports with seeded faults; chaos-monkey and
end-to-end suites run on every push, a nightly job runs 500 fault schedules,
and the Kernels workflow runs the SocketCAN and kernel ISO-TP tests in
microVMs with five kernels. They found and fixed two kernel ISO-TP bugs in
how go-canbus drives the kernel (see Fixes). Interop runs against
canbus-interop v0.2.0 (contract 1.1).

## Changes

- `capture`: Vector ASC and BLF readers and writers (checked both ways with python-can); `FormatFromPath`.
- `arxml`: AUTOSAR 4.x reader for CAN: clusters, frame triggerings (CAN FD, extended IDs, senders/receivers), frames with PDUs at byte offsets, I-PDUs, signals with signed and IEEE 754 base types, compu methods (LINEAR, TEXTTABLE, SCALE_LINEAR_AND_TEXTTABLE), units, limits; diagnostics for the rest.
- `kcd.Write`: KCD output, read back unchanged by canmatrix.
- `candb.PhysText`: exact physical input from decimal text (64-bit values).
- `isotp.Config.ListenOnly` (also kernel sockets) and `FlowControlDelay` (N_Br).
- `chaos`: fault-injecting bridge (drop, duplicate, delay, reorder, corrupt; filter; seeded).
- `virtual.FaultStats` has JSON tags.

## Fixes

- **Kernel ISO-TP send could resend a message** (`socketcan.DialISOTP`): with `CAN_ISOTP_WAIT_TX_DONE` the kernel waits inside `sendmsg` interruptibly; Go's preemption signal (SIGURG) aborted the transfer and the restarted syscall sent the message again. The write now runs on a locked thread with signals blocked.
- **Stalls against the kernel's ISO-TP sender on vcan**: the kernel drops a flow control that arrives before it has processed the echo of its own last frame of a block. Use `FlowControlDelay` (a few hundred µs) against kernel peers on virtual interfaces.
- The KCD writer no longer writes empty attributes (canmatrix rejected them).

## Testing

- Interop against canbus-interop v0.2.0: raw CAN with can-utils, python-can, socketcan-rs and Scapy; ISO-TP with the kernel and python-can-isotp; DBC, ARXML and KCD with cantools and canmatrix; ASC, BLF and candump with python-can.
- Chaos monkey: ISO-TP never delivers wrong data under drop/duplicate/delay and recovers; corruption and reordering never panic or hang; Bus operations under churn keep order and leak nothing; also kernel ISO-TP ↔ go-canbus through the chaos bridge.
- End-to-end: vehicle scenario (generated ECUs, gateway, recording into five formats, ISO-TP, replay) on the virtual bus and on vcan with the kernel; processes on vcan; generated code from DBC, KCD and ARXML compiled in a separate module.

---

## v0.1.0

**Date:** 2026-10-27
**Previous release:** none

## Summary

First release of go-canbus: a pure Go CAN library for classical CAN and
CAN FD frames, Linux SocketCAN, a deterministic virtual bus, ISO-TP, capture
and replay, and DBC/KCD databases with a signal codec and a Go code
generator. Tested against independent implementations from
canbus-interop.

## Changes

- `canbus`: frames, DLC, filters with Linux `CAN_RAW_FILTER` semantics, error frames, `Transport`, `Bus` with subscriptions, slog and metrics.
- `socketcan`: raw sockets (FD, kernel filters, loopback, receive-own, timestamps, drop accounting, cancellation; zero allocations) and kernel ISO-TP sockets.
- `socketcan/link`: rtnetlink administration.
- `virtual`: deterministic in-memory bus with fault injection.
- `isotp`: ISO 15765-2 in userspace.
- `capture`: candump, JSON Lines, pcap, pcapng; replay.
- `candb`, `dbc`, `kcd`, `codegen`: message databases, exact codec (64-bit raw values, `PhysText`), generator.

## Testing

- Interop against canbus-interop v0.1.0 (can-utils and kernel ISO-TP, python-can, socketcan-rs, Scapy, python-can-isotp, cantools, canmatrix): 120 scenarios pass on amd64 and arm64; known differences of the oracles in INTEROPERABILITY.md.
- Kernels workflow: the SocketCAN, netlink and kernel ISO-TP tests in microVMs with Ubuntu generic 6.8 and mainline 6.1, 6.6, 6.12, 6.18.
- Coverage ≥ 90 % on vcan; race detector; 11 fuzz targets.

---
