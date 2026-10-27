# Progress

Durable state of the CAN bus ecosystem work (go-canbus, canbus-interop,
canbusctl). Newest first within each section.

## Status (2026-10-27)

| Repo | Branch / PR | State |
|------|-------------|-------|
| go-canbus | feat/core | M1–M8 and M10 done: core, virtual, SocketCAN, netlink, capture, candb/dbc/kcd, codegen, ISO-TP (userspace + kernel + listen-only), examples, docs, CI (vcan jobs), interop suite and workflow, website proposal (docs/WEBSITE-INTEGRATION.md). |
| canbus-interop | feat/adapters, PR #1 | Adapters, schemas, fixtures, self-test, CI and release workflows built by a sub-agent; pending: Scapy `send --fd` sends classical documents as FD frames (reported). |
| canbusctl | feat/cli, PR #1 | All command groups, CI green, 94.5 % coverage; follow-ups requested: float formatting, ListenOnly monitor, PhysText. |

Measured in the Lima VM (Ubuntu, kernel 7.0, vcan0 MTU 72, can-isotp):
library coverage 91.6 %; interop 120 pass / 0 fail / 4 skip (capability
gaps), 66 frames with documented known differences (INTEROPERABILITY.md);
SocketCAN send+receive 0 allocs/op.

## Decisions

- Frame is a value type with a [64]byte payload; `Len8DLC` keeps raw DLC
  9..15; error frames keep the Linux class in ID.
- Filters reproduce CAN_RAW_FILTER exactly (fuzzed against the kernel
  expression and checked against the kernel on vcan).
- SocketCAN via syscall only; raw recvmsg because syscall.Recvmsg rejects
  AF_CAN addresses; poller integration through os.NewFile.
- vcan on Linux ≥ 6.2 defaults to the CAN XL MTU (2060): FD sockets accept
  MTU ≥ 72.
- Physical rounding is half-to-even (cantools and canmatrix both).
  64-bit physical inputs: candb.PhysText (exact for factor 1, offset 0).
- KCD big-endian offsets: MSB-first linear index, as canmatrix reads them.
- Kernel ISO-TP errnos: ECOMM = N_Bs, EMSGSIZE = OVERFLOW, ETIMEDOUT = N_Cr,
  EILSEQ = sequence, EBADMSG = bad FC.
- Coverage is measured in the CI vcan job; can-isotp is built out of tree
  on runner kernels without it (scripts/ci-can-modules.sh).
- Known differences (not defects): cantools rejects multiplexer values that
  select no signal; canmatrix rejects payloads longer than the message and
  cannot encode extended multiplexing.

## Environment

- Docker Desktop's kernel has no CAN modules. The Lima VM `canvm` does
  (`limactl shell canvm`); the Mac home is mounted read-only there: use
  `GOFLAGS=-buildvcs=false`, write to /tmp, and `sg docker -c` for Docker.
- Local interop images: `ghcr.io/otfabric/canbus-interop-<a>:dev` (tagged in
  the VM; built by canbus-interop's `make images`).

## Next actions

1. Get all three PRs green, then merge in order: canbus-interop → go-canbus
   → canbusctl.
2. Release canbus-interop v0.1.0 (tag → GHCR images), pin the image digests
   in go-canbus interop/harness_test.go (InteropRelease) and drop the
   source build from the Interop workflow; release go-canbus v0.1.0; bump
   canbusctl to it and release v0.1.0.
3. Later: ARXML via pinned canmatrix conversion, vendor transports,
   CANopen/J1939/UDS modules.
