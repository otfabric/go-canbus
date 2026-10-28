# Changelog

Release notes with details are in [RELEASE.md](RELEASE.md).

## Unreleased

ARXML MULTIPLEXED-I-PDUs (static and dynamic parts, Intel and Motorola
selectors), read as cantools reads them; checked against cantools with
canbus-interop's multiplexed.arxml fixture.

slcan adapter status: "e" (bus error) and "s" (controller state, error
counters) records become error frames, byte for byte as the Linux slcan
driver delivers them; `Conn.SetFilters` (software, SocketCAN defaults)
and `Conn.State`. go-serial v0.1.7. `Subscription.Received` counts a frame
before it is queued, so a consumer that has the frame sees it counted
(TestBusFanOutAndFilters failed once under the race detector).

## v0.3.0 (2026-10-28)

Package `slcan`: serial-line CAN adapters (CANable, CANtact, USBtin,
LAWICEL) on every OS through go-serial, or slcan over any byte stream;
tested against the Linux kernel's slcan driver and python-can. PEAK TRC
traces in `capture` (1.0–2.1 read, 2.1 written). Interop against
canbus-interop v0.3.0 (contract 1.2).

## v0.2.2 (2026-10-28)

`canbus.WithStartOnSubscribe`: a Bus that reads from its first
subscription on, so frames arriving between NewBus and Subscribe are not
lost.

## v0.2.1 (2026-10-28)

Deadline edge cases in chaos.Bridge.Run and kernel ISO-TP sockets.

## v0.2.0 (2026-10-28)

Vector ASC and BLF traces; AUTOSAR 4.x ARXML reader; KCD writer; exact
decimal physical input; the chaos package (fault-injecting bridge) and
chaos-monkey suites; end-to-end suite; Kernels (microVM) and nightly chaos
workflows; interop against canbus-interop v0.2.0.

## v0.1.0 (2026-10-27)

First release: frames and filters, Bus, SocketCAN and kernel ISO-TP,
netlink administration, virtual bus with fault injection, userspace
ISO-TP, candump/JSON Lines/pcap/pcapng capture and replay, candb model and
codec, DBC parser and writer, KCD parser, Go code generator.
