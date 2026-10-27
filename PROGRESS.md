# Progress

Durable state of the CAN bus ecosystem work (go-canbus, canbus-interop,
canbusctl). Newest first within each section.

## Status (2026-10-27)

| Repo | Branch | State |
|------|--------|-------|
| go-canbus | feat/core | M1–M6 done: core, virtual, SocketCAN, netlink, capture, candb/dbc/kcd, codegen, ISO-TP (userspace + kernel), examples, Makefile, CI. Docs in progress. Interop suite (M8) waits for canbus-interop images. |
| canbus-interop | feat/adapters | Being built (adapters, schemas, selftest). Contract: docs/CONTAINER_CONTRACT.md. |
| canbusctl | feat/cli | Being built on go-canbus 8cf9cd6 (pseudo-version). |

Measured (Lima VM, Ubuntu kernel 7.0, vcan0 MTU 72, CANBUS_REQUIRE_VCAN=1):
library coverage 91.6 % (coverpkg, before the last fixes); SocketCAN send+receive
0 allocs/op, ~1.5–1.9 µs per round trip on vcan.

## Decisions

- Frame is a value type with a [64]byte payload (no allocation per frame);
  `Len8DLC` keeps raw DLC 9..15; error frames keep the Linux class in ID.
- Filters reproduce CAN_RAW_FILTER exactly (software matcher evaluates the
  kernel expression on kernel-encoded IDs; fuzzed against it, and checked
  against the kernel on vcan).
- SocketCAN via syscall only (no x/sys): raw recvmsg because syscall.Recvmsg
  rejects AF_CAN addresses; poller integration through os.NewFile.
- vcan on Linux ≥ 6.2 defaults to the CAN XL MTU (2060): FD sockets accept
  MTU ≥ 72.
- Physical rounding is half-to-even (cantools and canmatrix both round so).
- KCD big-endian offsets: MSB-first linear index, as canmatrix 1.3.3 reads
  them (probed with canconvert; Kayak docs are ambiguous).
- Kernel ISO-TP errnos: ECOMM = N_Bs timeout, EMSGSIZE = OVERFLOW,
  ETIMEDOUT = N_Cr, EILSEQ = sequence, EBADMSG = bad FC.
- Coverage is measured in the CI vcan job (the shared go-ci job has no vcan,
  so its Codecov upload is off).

## Environment

- Docker Desktop's kernel has no CAN modules. The Lima VM `canvm` does
  (`limactl shell canvm`); the Mac home is mounted read-only there: use
  `GOFLAGS=-buildvcs=false` and write outputs to /tmp.

## Next actions

1. go-canbus docs (README, API, TESTING, INTEROPERABILITY, SECURITY,
   LIMITATIONS, CHANGELOG, CONTRIBUTING, ERRORS, OBSERVABILITY, RELEASE).
2. interop/ suite (build tag `interop`) against the canbus-interop images:
   raw CAN both directions per adapter, ISO-TP kernel/python-can-isotp,
   DBC decode/encode differential vs cantools and canmatrix.
3. Review and integrate canbusctl and canbus-interop PRs; release order:
   canbus-interop (images) → go-canbus (pins image digests) → canbusctl.
4. otfabric.io integration proposal (docs/WEBSITE.md).
