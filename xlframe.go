// SPDX-License-Identifier: MIT

package canbus

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// XLFlags are the flags of a CAN XL frame (struct canxl_frame's flags
// without CANXL_XLF, which every CAN XL frame carries).
type XLFlags uint8

// CAN XL flags.
const (
	// XLSEC is the simple extended content bit (security, segmentation).
	XLSEC XLFlags = 0x01
	// XLRRS is the remote request substitution bit (Linux ≥ 6.18 names
	// it; older kernels pass it through as a reserved bit).
	XLRRS XLFlags = 0x02
)

// xlf is CANXL_XLF, the bit that marks a CAN XL frame in the flags byte.
const xlf = 0x80

// String lists the set flags, for example "SEC|RRS".
func (f XLFlags) String() string {
	if f == 0 {
		return "0"
	}
	var parts []string
	if f&XLSEC != 0 {
		parts = append(parts, "SEC")
	}
	if f&XLRRS != 0 {
		parts = append(parts, "RRS")
	}
	if rest := f &^ (XLSEC | XLRRS); rest != 0 {
		parts = append(parts, "0x"+strconv.FormatUint(uint64(rest), 16))
	}
	return strings.Join(parts, "|")
}

// CAN XL limits.
const (
	MaxXLPriority = 0x7FF // largest 11-bit CAN XL priority
	MinXLLen      = 1     // smallest CAN XL payload
	MaxXLLen      = 2048  // largest CAN XL payload
)

// XLFrame is one CAN XL frame (ISO 11898-1:2024) with its receive
// metadata, laid out like Linux's struct canxl_frame.
//
// Unlike [Frame], the payload is a slice: it holds up to 2048 bytes.
// Transports copy it; they do not keep it after Send returns.
type XLFrame struct {
	// Priority is the 11-bit arbitration priority.
	Priority uint16
	// VCID is the virtual CAN network identifier (0 = none).
	VCID uint8
	// Flags are the frame's flags other than the XL format bit.
	Flags XLFlags
	// SDT is the SDU (service data unit) type of the payload.
	SDT uint8
	// AF is the 32-bit acceptance field.
	AF uint32
	// Data is the payload, 1..2048 bytes.
	Data []byte
	// Timestamp is the receive time reported by the transport, or the
	// zero time.
	Timestamp time.Time
	// Interface names the interface the frame was received on, if known.
	Interface string
	// Echo reports that the frame is the receiving socket's own
	// transmission.
	Echo bool
}

// Validate checks the priority, the payload length and the flags.
func (f *XLFrame) Validate() error {
	switch {
	case f.Priority > MaxXLPriority:
		return f.invalid(fmt.Sprintf("priority 0x%X exceeds 11 bits", f.Priority))
	case len(f.Data) < MinXLLen || len(f.Data) > MaxXLLen:
		return f.invalid(fmt.Sprintf("payload of %d bytes (CAN XL carries 1..2048)", len(f.Data)))
	case f.Flags&^(XLSEC|XLRRS) != 0:
		return f.invalid(fmt.Sprintf("reserved flags 0x%02X", uint8(f.Flags&^(XLSEC|XLRRS))))
	}
	return nil
}

func (f *XLFrame) invalid(reason string) error {
	return fmt.Errorf("%w %s: %s", ErrInvalidFrame, f.String(), reason)
}

// String formats the frame as can-utils does (cansend, candump):
// "<vcid><prio>#<flags>:<sdt>:<af>#<data>" with a two-digit VCID, a
// three-digit priority and the flags byte including the XL format bit
// 0x80, for example "00242#80:01:00000000#AABBCC".
func (f XLFrame) String() string {
	var b strings.Builder
	b.Grow(22 + 2*len(f.Data))
	fmt.Fprintf(&b, "%02X%03X#%02X:%02X:%08X#", f.VCID, f.Priority&MaxXLPriority, uint8(f.Flags)|xlf, f.SDT, f.AF)
	b.WriteString(strings.ToUpper(hex.EncodeToString(f.Data)))
	return b.String()
}

// ParseXLFrame parses a CAN XL frame in can-utils notation:
// "<vcid><prio>#<flags>:<sdt>:<af>#<data>" (five hex digits before the
// first "#") or "<prio>#<flags>:<sdt>:<af>#<data>" (three digits, VCID 0).
// Data bytes may be separated by dots. The XL format bit 0x80 in the flags
// is implied, as cansend implies it.
func ParseXLFrame(s string) (XLFrame, error) {
	var f XLFrame
	bad := func(why string) (XLFrame, error) {
		return XLFrame{}, fmt.Errorf("%w: %q: %s", ErrInvalidFrame, s, why)
	}
	head, rest, ok := strings.Cut(s, "#")
	if !ok {
		return bad("no '#'")
	}
	var prio uint64
	var err error
	switch len(head) {
	case 3:
		prio, err = strconv.ParseUint(head, 16, 16)
	case 5:
		var v uint64
		if v, err = strconv.ParseUint(head[:2], 16, 8); err == nil {
			f.VCID = uint8(v)
			prio, err = strconv.ParseUint(head[2:], 16, 16)
		}
	default:
		return bad("the priority has 3 hex digits, or 5 with the VCID")
	}
	if err != nil {
		return bad("bad priority")
	}
	f.Priority = uint16(prio)
	// "FF:SS:AAAAAAAA#data"
	if len(rest) < 15 || rest[2] != ':' || rest[5] != ':' || rest[14] != '#' {
		return bad("not a CAN XL header <flags>:<sdt>:<af>#")
	}
	fl, err1 := strconv.ParseUint(rest[0:2], 16, 8)
	sdt, err2 := strconv.ParseUint(rest[3:5], 16, 8)
	af, err3 := strconv.ParseUint(rest[6:14], 16, 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return bad("bad CAN XL header")
	}
	f.Flags, f.SDT, f.AF = XLFlags(fl&^xlf), uint8(sdt), uint32(af)
	data := strings.ReplaceAll(rest[15:], ".", "")
	if f.Data, err = hex.DecodeString(data); err != nil {
		return bad("bad data")
	}
	if err := f.Validate(); err != nil {
		return XLFrame{}, err
	}
	return f, nil
}

// IsXLText reports whether s looks like a CAN XL frame in can-utils
// notation (a CAN XL header after the first "#"), as opposed to a
// classical or CAN FD frame.
func IsXLText(s string) bool {
	_, rest, ok := strings.Cut(s, "#")
	return ok && len(rest) >= 15 && rest[2] == ':' && rest[5] == ':' && rest[14] == '#'
}
