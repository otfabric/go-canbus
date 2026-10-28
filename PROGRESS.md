# Progress

Durable state of the CAN bus ecosystem work (go-canbus, canbus-interop,
canbusctl). Newest first within each section.

## Status (2026-10-28): v0.2 released

| Repo | Release | Notes |
|------|---------|-------|
| go-canbus | v0.2.1 | ASC/BLF, ARXML, KCD writer, chaos + e2e suites, kernel ISO-TP fixes; interop vs canbus-interop v0.2.0: 145 pass / 0 fail / 4 capability skips; coverage 92.3 % |
| canbus-interop | v0.2.0 | contract 1.1 (log-convert, ARXML); self-test 341 pass / 0 fail; weekly upstream pin check; ISO-TP in a microVM on amd64 |
| canbusctl | v0.2.0 | trace convert/info, ARXML, KCD output, bridge (chaos), --fc-delay; interop 63 pass / 0 skip; coverage 94.2 % |

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
- Kernel ISO-TP: CAN_ISOTP_WAIT_TX_DONE sendmsg is written with all
  signals blocked on a locked thread (SIGURG preemption + SA_RESTART resent
  messages); userspace receivers facing a kernel sender on vcan delay their
  flow control (FlowControlDelay), the kernel drops an FC that beats its echo.
- Tests sharing vcan run with -p 1.
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

1. otfabric.io pages from docs/WEBSITE-INTEGRATION.md (not applied).
2. Optional: canbusctl drops its deadline workarounds on go-canbus v0.2.1;
   socketcan-rs and Scapy in canbusctl's interop tests.
3. Later: TRC traces, multiplexed I-PDUs in ARXML, vendor transports,
   CANopen/J1939/UDS modules on canbus.Bus and isotp.
