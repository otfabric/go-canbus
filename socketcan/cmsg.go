// SPDX-License-Identifier: MIT

package socketcan

import (
	"encoding/binary"
	"math"
	"time"
)

// Socket options and control messages the CAN_RAW receive path uses
// (asm-generic/socket.h). They are Linux values, declared here so that
// the control message decoder is portable and testable everywhere.
const (
	linuxSOLSocket = 1
	soTimestampNS  = 35 // SO_TIMESTAMPNS_OLD: struct timespec of the native long size
	soRxqOvfl      = 40
)

// Layout of struct cmsghdr and struct timespec on the supported Linux
// ABIs: cmsg_len is a size_t and timespec holds two longs, both of the
// native word size; the header is padded to that size.
const (
	wordSize   = 4 << (^uintptr(0) >> 63) // 4 or 8
	cmsgHdrLen = wordSize + 8             // cmsg_len, cmsg_level, cmsg_type
	oobSize    = 128                      // receive control buffer
	// sockaddrCANLen is the size of struct sockaddr_can.
	sockaddrCANLen = 24
)

func cmsgAlign(n int) int { return (n + wordSize - 1) &^ (wordSize - 1) }

// word reads a native size_t or long. b holds at least wordSize bytes.
func word(b []byte) uint64 {
	if wordSize == 8 {
		return binary.NativeEndian.Uint64(b)
	}
	return uint64(binary.NativeEndian.Uint32(b))
}

// controlLen bounds the msg_controllen the kernel returned by the size of
// the buffer it was given.
func controlLen(v uint64) int {
	if v > oobSize {
		return oobSize
	}
	return int(v)
}

// sockaddrIndex returns can_ifindex of a struct sockaddr_can, or 0 for a
// short or negative one.
func sockaddrIndex(sa []byte) int {
	if len(sa) < 8 {
		return 0
	}
	v := binary.NativeEndian.Uint32(sa[4:8])
	if v > math.MaxInt32 {
		return 0
	}
	return int(v)
}

// parseCmsgs decodes the SO_TIMESTAMPNS timestamp and the SO_RXQ_OVFL
// drop counter from a control message buffer without allocating (the
// standard library's ParseSocketControlMessage allocates). It stops at
// the first malformed header; a missing or invalid timestamp is zero.
func parseCmsgs(oob []byte) (ts time.Time, ovfl uint32, haveOvfl bool) {
	for len(oob) >= cmsgHdrLen {
		ln := word(oob)
		if ln > oobSize {
			return ts, ovfl, haveOvfl
		}
		l := int(ln)
		if l < cmsgHdrLen || l > len(oob) {
			return ts, ovfl, haveOvfl
		}
		level := binary.NativeEndian.Uint32(oob[wordSize:])
		typ := binary.NativeEndian.Uint32(oob[wordSize+4:])
		data := oob[cmsgHdrLen:l] // cmsgHdrLen is already aligned
		if level == linuxSOLSocket {
			switch typ {
			case soTimestampNS:
				if t, ok := timespec(data); ok {
					ts = t
				}
			case soRxqOvfl:
				if len(data) >= 4 {
					ovfl, haveOvfl = binary.NativeEndian.Uint32(data), true
				}
			}
		}
		if cmsgAlign(l) >= len(oob) {
			break
		}
		oob = oob[cmsgAlign(l):]
	}
	return ts, ovfl, haveOvfl
}

// timespec decodes a struct timespec of native longs. Negative or
// out-of-range fields (never sent by the kernel for a receive timestamp)
// report false.
func timespec(b []byte) (time.Time, bool) {
	if len(b) < 2*wordSize {
		return time.Time{}, false
	}
	sec, nsec := word(b), word(b[wordSize:])
	if wordSize == 4 && sec > math.MaxInt32 {
		return time.Time{}, false // a negative 32-bit time_t
	}
	if sec > math.MaxInt64 {
		return time.Time{}, false
	}
	if nsec >= uint64(time.Second) {
		return time.Time{}, false
	}
	return time.Unix(int64(sec), int64(nsec)), true
}
