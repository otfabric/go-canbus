# Changelog

Release notes with details are in [RELEASE.md](RELEASE.md).

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
