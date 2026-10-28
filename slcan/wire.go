// SPDX-License-Identifier: MIT

package slcan

import (
	"fmt"

	"github.com/otfabric/go-canbus"
)

const hexDigits = "0123456789ABCDEF"

func appendHex(dst []byte, v uint32, digits int) []byte {
	for i := digits - 1; i >= 0; i-- {
		dst = append(dst, hexDigits[(v>>(4*uint(i)))&0xF])
	}
	return dst
}

// AppendFrame appends the slcan record of f, terminated by "\r", to dst:
// "t" (standard data), "T" (extended data), "r"/"R" (remote), "d"/"D"
// (CAN FD) or "b"/"B" (CAN FD with bit rate switch). A classical frame's
// DLC 9..15 is sent as 8. Error frames and CAN FD frames with the error
// state indicator cannot be expressed: they return
// [canbus.ErrUnsupported].
func AppendFrame(dst []byte, f *canbus.Frame) ([]byte, error) {
	if err := f.Validate(); err != nil {
		return dst, err
	}
	ext := f.IsExtended()
	var cmd byte
	switch {
	case f.IsError():
		return dst, fmt.Errorf("%w: slcan cannot send error frames", canbus.ErrUnsupported)
	case f.IsFD() && f.Flags&canbus.FlagESI != 0:
		return dst, fmt.Errorf("%w: slcan cannot send the error state indicator", canbus.ErrUnsupported)
	case f.IsFD() && f.Flags&canbus.FlagBRS != 0:
		cmd = 'b'
	case f.IsFD():
		cmd = 'd'
	case f.IsRemote():
		cmd = 'r'
	default:
		cmd = 't'
	}
	if ext {
		cmd -= 'a' - 'A'
	}
	dst = append(dst, cmd)
	if ext {
		dst = appendHex(dst, uint32(f.ID), 8)
	} else {
		dst = appendHex(dst, uint32(f.ID), 3)
	}
	dlc := f.DLC()
	if !f.IsFD() && dlc > 8 {
		dlc = 8
	}
	dst = append(dst, hexDigits[dlc])
	if !f.IsRemote() {
		for _, b := range f.Payload() {
			dst = append(dst, hexDigits[b>>4], hexDigits[b&0xF])
		}
	}
	return append(dst, '\r'), nil
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

func parseHex(s []byte) (uint32, bool) {
	var v uint32
	for _, c := range s {
		d, ok := unhex(c)
		if !ok {
			return 0, false
		}
		v = v<<4 | uint32(d)
	}
	return v, true
}

// ParseFrame parses one slcan line (without its "\r") into f. It reports
// false, without error, for lines that are not frames: acknowledgements
// ("", "z", "Z"), commands a host sends (O, C, L, S6, ...) and status or
// version replies. A frame may be followed by a four-digit timestamp (the
// Z1 option of LAWICEL adapters); it is ignored.
func ParseFrame(line []byte, f *canbus.Frame) (bool, error) {
	if len(line) == 0 {
		return false, nil
	}
	var flags canbus.Flags
	idLen := 3
	switch line[0] {
	case 't':
	case 'T':
		flags, idLen = canbus.FlagExtended, 8
	case 'r':
		flags = canbus.FlagRemote
	case 'R':
		flags, idLen = canbus.FlagExtended|canbus.FlagRemote, 8
	case 'd':
		flags = canbus.FlagFD
	case 'D':
		flags, idLen = canbus.FlagExtended|canbus.FlagFD, 8
	case 'b':
		flags = canbus.FlagFD | canbus.FlagBRS
	case 'B':
		flags, idLen = canbus.FlagExtended|canbus.FlagFD|canbus.FlagBRS, 8
	default:
		return false, nil
	}
	if len(line) < 2+idLen {
		return false, fmt.Errorf("slcan: short record %q", line)
	}
	id, ok := parseHex(line[1 : 1+idLen])
	if !ok {
		return false, fmt.Errorf("slcan: bad identifier in %q", line)
	}
	if idLen == 3 && id > 0x7FF || id > 0x1FFFFFFF {
		return false, fmt.Errorf("slcan: identifier out of range in %q", line)
	}
	dlc, ok := unhex(line[1+idLen])
	if !ok {
		return false, fmt.Errorf("slcan: bad DLC in %q", line)
	}
	fd := flags&canbus.FlagFD != 0
	n := canbus.DLCToLen(dlc, fd)
	rest := line[2+idLen:]
	*f = canbus.Frame{ID: canbus.ID(id), Flags: flags, Len: uint8(n)}
	if flags&canbus.FlagRemote != 0 {
		if dlc > 8 {
			return false, fmt.Errorf("slcan: bad remote DLC in %q", line)
		}
		f.Len = dlc
		n = 0
	} else if !fd && dlc > 8 {
		f.Len8DLC = dlc
	}
	if len(rest) != 2*n && len(rest) != 2*n+4 {
		return false, fmt.Errorf("slcan: %d data bytes announced in %q", n, line)
	}
	for i := 0; i < n; i++ {
		hi, ok1 := unhex(rest[2*i])
		lo, ok2 := unhex(rest[2*i+1])
		if !ok1 || !ok2 {
			return false, fmt.Errorf("slcan: bad data in %q", line)
		}
		f.Data[i] = hi<<4 | lo
	}
	if len(rest) == 2*n+4 {
		if _, ok := parseHex(rest[2*n:]); !ok {
			return false, fmt.Errorf("slcan: bad timestamp in %q", line)
		}
	}
	return true, nil
}
