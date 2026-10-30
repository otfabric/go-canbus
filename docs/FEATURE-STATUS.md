# Support and feature matrix

What go-canbus supports, on what, how each feature is tested, and against what. Status: v0.7.0, with canbus-interop v0.5.0. Scenario-by-scenario results, known differences and test choices are in [INTEROPERABILITY.md](../INTEROPERABILITY.md); test levels in [TESTING.md](../TESTING.md); what is out of scope in [LIMITATIONS.md](../LIMITATIONS.md).

None of this is a conformance claim: there is no ISO or CiA conformance claim and no certification, and vcan has no arbitration, timing or errors.

## 1. Versions

| go-canbus | Go | canbus-interop (contract) | Supported |
|---|---|---|---|
| **v0.7.0** | 1.23 or later (CI: 1.23 to 1.26) | v0.5.0 (1.3) | **yes** |

## 2. Platforms

| Platform | SocketCAN, kernel ISO-TP, netlink | Everything else | Tested in CI |
|---|---|---|---|
| Linux amd64, arm64 | ✅ | ✅ | the test suite; Interop; kernels in micro-VMs |
| Linux armv7 | ✅ | ✅ | cross-compiled only (`make cross`, the `cross` job) |
| macOS, Windows | — `ErrUnsupported` | ✅ | `go test ./...` on `macos-latest`, `windows-latest` |

## 3. Feature support

✅ supported · ◐ partly (see the notes) · — not implemented. **Independent**: software that is not go-canbus exchanged it with go-canbus in the Interop workflow. All raw-CAN stacks share the kernel's SocketCAN code on vcan, so they show that userspace stacks agree, not that CAN controllers do.

| Feature | Status | Independent |
|---|:---:|---|
| Classical CAN 2.0A/B, CAN FD on SocketCAN | ✅ | can-utils, python-can, socketcan-rs, Scapy |
| CAN XL (Linux 6.2 or later) | ✅ | can-utils; the other stacks have no CAN XL |
| Virtual bus, `canbus.Bus` | ✅ | n/a (local) |
| slcan serial adapters | ✅ | python-can; the Linux slcan driver (`make test-admin`, micro-VMs) |
| ISO-TP, userspace and kernel | ✅ | Linux CAN_ISOTP, python-can-isotp |
| candump logs | ✅ | can-utils, python-can |
| ASC, BLF, PEAK TRC logs | ✅ | python-can |
| pcap, pcapng | ✅ | Wireshark's tshark (`capture/wireshark_test.go`) and tcpdump, in the CI job on Linux, not in the Interop workflow |
| DBC read and write | ✅ | cantools, canmatrix |
| KCD read and write | ✅ | canmatrix |
| AUTOSAR 4.x ARXML | ◐ read, CAN subset | cantools; canmatrix except multiplexed PDUs |
| Code generation (`codegen`) | ✅ | n/a (local) |
| PEAK, Kvaser, Vector adapters | — plug in through `canbus.Transport` | n/a |
| CANopen, J1939, UDS, NMEA 2000, ISOBUS, DroneCAN | — out of scope | n/a |
