# Limitations

## Standards and conformance

- ISO 11898-1 and ISO 15765-2 are not public. The implementation follows
  public material and independent implementations; there is **no ISO or
  CiA conformance claim** and no certification.
- DBC has no public specification. Constructs are supported as Vector
  tools, cantools and canmatrix treat them; differences are listed in
  INTEROPERABILITY.md.

## Not implemented

- CAN XL frames (a CAN XL MTU on the interface is accepted for CAN FD
  sockets; XL frames themselves are not received or sent).
- Hardware adapters other than SocketCAN (PEAK PCAN-Basic, Kvaser CANlib,
  Vector XL, serial slcan without the kernel's slcan driver, gs_usb is
  covered by SocketCAN on Linux). Implement `canbus.Transport` to add one.
- SocketCAN on macOS and Windows (there is none). Interface commands
  return `ErrUnsupported`.
- ARXML beyond the CAN subset of AUTOSAR 4.x listed in the `arxml`
  package: no AUTOSAR 3.x, multiplexed, container, secured or diagnostic
  PDUs, signal groups, end-to-end protection, other bus types; they are
  reported as diagnostics. A PDU at a byte offset in its frame is placed at
  that offset (AUTOSAR); cantools 45.1.0 and canmatrix 1.3.3 place it at 0.
- ASC and BLF carry no Linux error class: error frames read from them are
  protocol errors. python-can reads a classical frame's DLC 9..15 in ASC as
  a length and a 0-byte CAN FD frame as remote; go-canbus follows Vector's
  meaning.
- DBC: environment variables, signal types, relation attributes,
  categories and filters are skipped with a warning diagnostic. Value
  descriptions of environment variables likewise.
- KCD: remote frame definitions are noted, not modelled; LabelGroups
  wider than 256 values are not expanded.
- ISO-TP: functional addressing helpers, N_As/N_Ar confirmation timing
  (frames are handed to the transport immediately), and half-duplex mode
  of the kernel. Kernel sockets use the kernel's fixed 1 s timeouts and
  cannot send WAIT flow controls.
- Higher layers: CANopen, J1939, UDS, NMEA 2000, ISOBUS, DroneCAN/Cyphal.

## Behaviour to know

- Physical values are float64: raw values beyond 2^53 lose precision in
  `Physical`; the raw API (`Value.Raw`, `Signed`, `RawString`,
  generated fields) is exact.
- `Bus` subscriptions drop the newest frame on overflow by default;
  `Block` applies backpressure to every subscriber.
- The virtual bus models delivery only: no arbitration, timing,
  acknowledgement, error confinement or retransmission. A corrupted frame
  arrives corrupted; a real bus would reject it by CRC.
- vcan is a perfect medium: tests on it prove interoperability of frame
  encoding and protocol logic, not electrical or timing behaviour.
- ISO-TP cannot tell a consecutive frame of an earlier transfer from one
  of the current transfer: a gateway that reorders frames can corrupt a
  message undetected (see TESTING.md, chaos monkey). A single CAN bus keeps
  each sender's order.
- Kernel drop counts arrive with the next queued frame (SO_RXQ_OVFL).
- A userspace ISO-TP receiver that advertises STmin 0 must absorb a whole
  block at bus speed. On a slow host its socket's receive queue can
  overflow (seen with Ubuntu's 6.8 kernel in a 2-vCPU microVM): open the
  socket with `socketcan.WithReceiveBuffer(1 << 20)` or advertise a non-zero
  STmin. The receiver reports the loss as `ErrSequence`, never as data.
- A CAN identifier names a message, not a device; nothing identifies
  physical nodes.

## Needs real hardware to validate

Bit rates and sample points, CAN FD data phase and transceiver delay
compensation, error counters and bus-off recovery (`restart-ms`), ESI set
by an error-passive transmitter, arbitration and timing under load.
