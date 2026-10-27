// SPDX-License-Identifier: MIT

// Package e2e holds end-to-end tests that combine the packages of
// go-canbus the way an application does: simulated ECUs from generated
// code, a gateway decoding with a database, recording into every trace
// format, replaying and decoding again, ISO-TP diagnostics alongside, on
// the virtual bus everywhere and on vcan with the kernel on Linux; code
// generated from DBC, KCD and ARXML compiled and exercised in a separate
// module; several processes on vcan.
package e2e
