# go-canbus Releases

Releases are made with the Release workflow (Actions → Release → bump):
it computes the version, tags it and publishes this file's section of
that version as the release notes. Add the section, headed exactly
`## vX.Y.Z` and closed by a line `---`, before running it.

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
