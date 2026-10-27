// SPDX-License-Identifier: MIT

//go:build interop

// Package interop tests go-canbus against the independent implementations
// published by otfabric/canbus-interop (can-utils and the Linux kernel's
// ISO-TP, python-can, socketcan-rs, Scapy, python-can-isotp, cantools,
// canmatrix), through their container contract only.
//
// It needs Linux, Docker and a vcan interface: make interop. See
// INTEROPERABILITY.md.
package interop
