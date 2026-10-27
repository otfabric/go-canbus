# Progress

Durable state of the CAN bus ecosystem work (go-canbus, canbus-interop,
canbusctl). Newest first within each section.

## Status (2026-10-27): released

| Repo | Release | Notes |
|------|---------|-------|
| go-canbus | v0.1.0 | CI, Interop (canbus-interop v0.1.0 by digest, amd64+arm64: 120 pass / 0 fail / 4 capability skips) and Kernels (microVMs: Ubuntu 6.8, mainline 6.1–6.18) green |
| canbus-interop | v0.1.0 | 7 public multi-arch GHCR images; self-test 326 pass / 4 known / 9 skip |
| canbusctl | v0.1.0 | 7 binaries; on go-canbus v0.1.0; 94.5 % coverage |

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
- MicroVMs (docs/MICROVM.md): virtme-ng on GitHub amd64 runners (KVM);
  arm64 runners have no /dev/kvm. Found that a userspace ISO-TP receiver at
  STmin 0 can overflow its socket (Ubuntu 6.8 guest): tests use a 1 MiB
  receive buffer; LIMITATIONS.md says so.
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

1. Apply the microVM pattern to canbusctl and canbus-interop (replace the
   out-of-tree can-isotp build), and to PROFINET/EtherNet/IP L2 tests.
2. otfabric.io pages from docs/WEBSITE-INTEGRATION.md (not applied).
3. ARXML via pinned canmatrix conversion; vendor transports; CANopen,
   J1939 and UDS modules on canbus.Bus and isotp.
