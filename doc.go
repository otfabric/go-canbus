// SPDX-License-Identifier: MIT

// Package canbus is the transport-neutral core of go-canbus: CAN and CAN FD
// frames, identifiers, data length codes, filters, error frames, the
// [Transport] interface and the [Bus], which shares one transport among
// many subscribers.
//
// Transports live in subpackages: socketcan (Linux SocketCAN raw sockets)
// and virtual (a deterministic in-memory bus for tests and simulation).
// Higher layers build on a Bus: isotp (ISO 15765-2), capture (candump,
// JSON Lines and pcap traces, replay), and candb with dbc, kcd and codegen
// (message and signal databases).
//
// A CAN identifier names a message, not a device: nothing in this module
// infers which physical node sent a frame.
package canbus
