# Progress

Durable state of the CAN bus ecosystem work (go-canbus, canbus-interop,
canbusctl). Newest first within each section.

## Status (2026-10-29): go-canbus v0.7.0, canbusctl v0.6.0 released

| Repo | Release | Notes |
|------|---------|-------|
| go-canbus | v0.7.0 | v0.7.0: CAN XL on the Bus (one ordered queue per WithXL subscription), virtual bus, pcap/pcapng (libpcap layout, PCAPNGWriter), replay/RecordAll, chaos; kernel CAN_RAW_FILTER semantics for CAN XL (MatchXL); oracles: tcpdump/libpcap captures, Wireshark ≥ 4.4; interop 170 pass / 0 fail / 13 skip / 132 known; coverage 93.3 %. v0.6.0 and v0.6.1 (same commit): go-serial v0.1.8 only (v0.6.0 published by a mis-chained release run; notes corrected). v0.5.0: CAN XL (XLFrame, SocketCAN XL sockets, candump/JSONL traces), interop vs canbus-interop v0.5.0: 170 pass / 0 fail / 13 skip / 132 known; coverage 92.9 %. v0.4.0: slcan adapter status records as error frames (kernel-identical), multiplexed ARXML I-PDUs, go-serial v0.1.7, Subscription.Received fix; interop vs canbus-interop v0.4.0: 167 pass / 0 fail / 4 capability skips / 132 known. v0.3.0: slcan transport (any OS, go-serial), PEAK TRC; interop vs canbus-interop v0.3.0: 163 pass / 0 fail / 4 capability skips; coverage 92.8 %. v0.2.2: Bus WithStartOnSubscribe (no frames lost before the first Subscribe); ASC/BLF, ARXML, KCD writer, chaos + e2e suites, kernel ISO-TP fixes; interop vs canbus-interop v0.2.0: 145 pass / 0 fail / 4 capability skips; coverage 92.3 % |
| canbus-interop | v0.5.0 | v0.5.0: contract 1.3 (CAN XL: canutils, Linux ≥ 6.9), xl.jsonl, suite xl; canutils counts accepted frames (candump -L). v0.4.0: multiplexed.arxml fixture, cantools probe (canmatrix known); contract 1.2 (pythoncan slcan interfaces, pyserial 3.5; ISO-TP adapter 1 MiB rcvbuf); v0.2.0: contract 1.1 (log-convert, ARXML); self-test 341 pass / 0 fail; weekly upstream pin check; ISO-TP in a microVM on amd64 |
| canbusctl | v0.6.0 | v0.6.0: record --xl, replay --xl, trace convert keeps/reports CAN XL (was silently dropped), trace info counts it, xl commands on virtual, pcapng output; on go-canbus v0.7.0. v0.5.0: xl send, xl monitor (CAN XL on SocketCAN; candump/JSONL output); interop vs canbus-interop v0.5.0: 88 pass / 6 skip / 76 known; coverage 94.1 %. v0.4.0: --errors delivers error frames (monitor, record), slcan status records, multiplexed ARXML; interop vs canbus-interop v0.4.0: 86 pass / 76 known / 0 skip; coverage 94.4 % (Linux, vcan). v0.3.0: slcan adapters (-i slcan:<device> or slcan:tcp://), listen-only passive commands, TRC; kernel-slcan e2e in microVMs; interop 84 pass / 27 known / 0 skip; coverage 92.4 %. v0.2.2: on go-canbus v0.2.2 (buses read from the first subscription on); v0.2.1: deadline workarounds removed; v0.2.0: trace convert/info, ARXML, KCD output, bridge (chaos), --fc-delay; interop 63 pass / 0 fail (27 known differences); coverage 92.2 % (macOS) |

## Decisions

- CAN XL in a Bus: a `WithXL` subscription has one queue of every kind,
  read with `ReceiveAny` (no `Frames()` channel). A first design with a
  second queue merged by timestamp reordered frames on Windows, whose
  clock gives many frames the same timestamp: order must not depend on
  clocks.
- CAN XL filters: the kernel's CAN_RAW_FILTER matches VCID<<16 | priority
  as a standard data identifier (probed on Linux 7.0; TestXLKernelFilterParity
  compares on the CI kernels). FilterSet.MatchXL mirrors it.
- pcap CAN XL layout as libpcap writes it on Linux: priority/VCID word
  big-endian, length and acceptance field little-endian (tcpdump capture
  on Linux 7.0 in capture/testdata; our packets byte-identical).
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
- slcan oracles: the kernel's slcan line discipline on a pty (TIOCSETD
  N_SLCAN, SIOCGIFNAME, root) and python-can slcan over TCP (pyserial
  socket://). A hung-up tty reads 0 bytes forever (treat as EOF); go-serial
  must not be closed during a read (Close waits for the reader); File.Fd()
  makes a pty master blocking (use SyscallConn); macOS pty masters do not
  end a pending read on Close.
- Bus: frames read before the first Subscribe reach nobody;
  WithStartOnSubscribe defers the reader (canbusctl's openBus uses it).
  Found by canbusctl e2e in slow microVMs (record kept 21/60 frames).
- e2e process harnesses wait for the kernel's CAN receive lists
  (/proc/net/can/rcvlist_*, count entries: they carry match counters) to
  grow before sending: a fixed pause raced on a slow microVM.
- slcan status records follow the Linux driver byte for byte; the kernel is
  the oracle (TestKernelSlcanStatus in the microVMs). Kernels before 6.18
  ignore records of ≤ 4 characters and accept trailing characters after a
  state record; go-canbus decodes short records and requires exactly 8.
- ARXML MULTIPLEXED-I-PDUs follow cantools (selector `<frame>_selector<n>`,
  the alternatives' own selector mapping dropped); canmatrix 1.3.3 reads
  only the selector, so cantools is the only oracle (canbus-interop v0.4.0
  probes it against hand-worked frames).
- Release workflow does not rename CHANGELOG "Unreleased": rename it to
  the version in the release PR (missed for v0.4.0 and v0.5.0, fixed after).
- Merging: list every check (`gh pr checks N`) and look at each non-pass,
  including codecov/patch, before merging; never chain a merge after a
  summary in the same command.
- CAN XL: XLFrame is separate from Frame (2048-byte payload as a slice;
  Frame stays an allocation-free value). Kernel: CAN_RAW_XL_FRAMES implies
  FD, frame size must be 12 + len, VCID-tagged frames are dropped unless
  CAN_RAW_XL_VCID_OPTS (6.9+) says otherwise; go-canbus sets TX_PASS and an
  all-VCID RX filter like cansend/candump. can-utils v2025.01 cansend fails
  on any XL-MTU interface before 6.9. A new vcan has MTU 2060 since 6.2.
- Known differences (not defects): cantools rejects multiplexer values that
  select no signal; canmatrix rejects payloads longer than the message and
  cannot encode extended multiplexing.

## Environment

- Docker Desktop's kernel has no CAN modules. The Lima VM `canvm` does
  (`limactl shell canvm`); the Mac home is mounted read-only there: use
  `GOFLAGS=-buildvcs=false`, write to /tmp, and `sg docker -c` for Docker.
- `canvm` has tcpdump 4.99.6/libpcap 1.10.6 and tshark/dumpcap 4.6.4 (CAN XL
  oracles); run tcpdump with `-Z root` when tshark (as root) reads the file.
- Local interop images: `ghcr.io/otfabric/canbus-interop-<a>:dev` (tagged in
  the VM; built by canbus-interop's `make images`).

## Next actions

1. Paused on 2026-10-29 at the owner's request. Gaps and follow-ups, in
   suggested order: [ROADMAP.md](ROADMAP.md).
2. otfabric.io: the owner's, after approval (docs/WEBSITE-INTEGRATION.md).
