// SPDX-License-Identifier: MIT

package canbus

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ID is a CAN identifier without flag bits: 11 bits for a standard frame,
// 29 bits for an extended frame. Whether an identifier is standard or
// extended is recorded in [Frame.Flags], not in the ID, because 0x123 as a
// standard and as an extended identifier are different identifiers on the
// bus.
type ID uint32

// Identifier ranges.
const (
	MaxStandardID ID = 0x7FF      // largest 11-bit identifier
	MaxExtendedID ID = 0x1FFFFFFF // largest 29-bit identifier
)

// Flags carries the frame format bits of a [Frame].
type Flags uint8

// Frame flags.
const (
	// FlagExtended marks a 29-bit (CAN 2.0B) identifier.
	FlagExtended Flags = 1 << iota
	// FlagRemote marks a remote transmission request (classical CAN only).
	FlagRemote
	// FlagFD marks a CAN FD frame.
	FlagFD
	// FlagBRS marks a CAN FD frame sent with bit rate switching.
	FlagBRS
	// FlagESI marks a CAN FD frame whose transmitter was error passive.
	FlagESI
	// FlagError marks an error frame: a report of the local controller or
	// driver, never sent on the bus. See [ParseError].
	FlagError
)

// String lists the set flags, for example "EXT|FD|BRS".
func (f Flags) String() string {
	if f == 0 {
		return "0"
	}
	names := []string{"EXT", "RTR", "FD", "BRS", "ESI", "ERR"}
	var parts []string
	for i, n := range names {
		if f&(1<<i) != 0 {
			parts = append(parts, n)
		}
	}
	if rest := f &^ (1<<len(names) - 1); rest != 0 {
		parts = append(parts, "0x"+strconv.FormatUint(uint64(rest), 16))
	}
	return strings.Join(parts, "|")
}

// Payload size limits.
const (
	MaxClassicalLen = 8  // largest classical CAN payload
	MaxFDLen        = 64 // largest CAN FD payload
)

// Frame is one CAN or CAN FD frame with its receive metadata.
//
// The payload is a fixed array so that a Frame is a plain value: sending
// and receiving frames does not allocate. Bytes of Data beyond Len are zero
// in frames produced by this module. Use [Frame.Payload] for the valid
// bytes.
type Frame struct {
	// ID is the identifier without flag bits. For error frames it holds
	// the Linux error class bits (CAN_ERR_*).
	ID ID
	// Flags holds the format bits.
	Flags Flags
	// Len is the payload length in bytes: 0..8 for classical frames, one
	// of 0..8, 12, 16, 20, 24, 32, 48, 64 for CAN FD frames. For remote
	// frames it is the requested data length (the DLC), and no data is
	// carried.
	Len uint8
	// Len8DLC preserves a classical frame's raw DLC of 9..15, which also
	// means 8 data bytes. It is 0 otherwise.
	Len8DLC uint8
	// Data is the payload; only the first Len bytes are meaningful.
	Data [MaxFDLen]byte
	// Timestamp is the receive time reported by the transport (the
	// kernel's for SocketCAN), or the zero time.
	Timestamp time.Time
	// Interface names the interface the frame was received on, if known.
	Interface string
	// Echo reports that the frame is the receiving socket's own
	// transmission, delivered back because receive-own-messages is on.
	Echo bool
}

// NewStandard returns a classical data frame with an 11-bit identifier.
func NewStandard(id ID, data []byte) (Frame, error) {
	return newFrame(id, 0, data)
}

// NewExtended returns a classical data frame with a 29-bit identifier.
func NewExtended(id ID, data []byte) (Frame, error) {
	return newFrame(id, FlagExtended, data)
}

// NewFD returns a CAN FD frame. extended selects a 29-bit identifier, brs
// bit rate switching. A payload whose length is not a valid CAN FD length
// is rejected: pad it with [PadFD] first.
func NewFD(id ID, extended, brs bool, data []byte) (Frame, error) {
	fl := FlagFD
	if extended {
		fl |= FlagExtended
	}
	if brs {
		fl |= FlagBRS
	}
	return newFrame(id, fl, data)
}

// NewRemote returns a remote transmission request for dlc bytes (0..8).
func NewRemote(id ID, extended bool, dlc uint8) (Frame, error) {
	var f Frame
	f.ID = id
	f.Flags = FlagRemote
	if extended {
		f.Flags |= FlagExtended
	}
	f.Len = dlc
	return f, f.Validate()
}

func newFrame(id ID, fl Flags, data []byte) (Frame, error) {
	var f Frame
	f.ID = id
	f.Flags = fl
	if len(data) > MaxFDLen {
		return f, &FrameError{Frame: f, Reason: fmt.Sprintf("payload of %d bytes exceeds %d", len(data), MaxFDLen)}
	}
	f.Len = uint8(len(data))
	copy(f.Data[:], data)
	return f, f.Validate()
}

// Payload returns the valid payload bytes, a slice of f.Data. A remote
// frame carries no data: its payload is empty whatever its Len.
func (f *Frame) Payload() []byte {
	if f.Flags&FlagRemote != 0 {
		return f.Data[:0]
	}
	n := int(f.Len)
	if n > MaxFDLen {
		n = MaxFDLen
	}
	return f.Data[:n]
}

// SetPayload copies data into the frame, sets Len and clears the bytes
// beyond it. It does not validate the length against the frame type.
func (f *Frame) SetPayload(data []byte) {
	n := copy(f.Data[:], data)
	f.Len = uint8(n)
	clear(f.Data[n:])
}

// IsExtended reports a 29-bit identifier.
func (f *Frame) IsExtended() bool { return f.Flags&FlagExtended != 0 }

// IsFD reports a CAN FD frame.
func (f *Frame) IsFD() bool { return f.Flags&FlagFD != 0 }

// IsRemote reports a remote transmission request.
func (f *Frame) IsRemote() bool { return f.Flags&FlagRemote != 0 }

// IsError reports an error frame.
func (f *Frame) IsError() bool { return f.Flags&FlagError != 0 }

// DLC returns the data length code of the frame as transmitted on the bus:
// for classical frames Len, or Len8DLC when set; for CAN FD frames the code
// of Len (see [LenToDLC]).
func (f *Frame) DLC() uint8 {
	if f.Flags&FlagFD != 0 {
		return LenToDLC(int(f.Len))
	}
	if f.Len == 8 && f.Len8DLC > 8 {
		return f.Len8DLC
	}
	return f.Len
}

// Validate checks that the frame can be transmitted or represented: the
// identifier fits its format, the length is valid for the frame type, the
// flags are consistent (no remote CAN FD frame, no BRS/ESI without FD,
// Len8DLC only with Len 8) and the bytes beyond Len are zero. Error frames
// are only checked for their length. The returned error is a *[FrameError]
// wrapping [ErrInvalidFrame].
func (f *Frame) Validate() error {
	bad := func(format string, args ...any) error {
		return &FrameError{Frame: *f, Reason: fmt.Sprintf(format, args...)}
	}
	if f.Flags&^(FlagExtended|FlagRemote|FlagFD|FlagBRS|FlagESI|FlagError) != 0 {
		return bad("unknown flags %s", f.Flags)
	}
	if f.Flags&FlagError != 0 {
		if f.Len > MaxClassicalLen {
			return bad("error frame length %d exceeds 8", f.Len)
		}
		return nil
	}
	if f.Flags&FlagExtended != 0 {
		if f.ID > MaxExtendedID {
			return bad("extended identifier 0x%X exceeds 29 bits", uint32(f.ID))
		}
	} else if f.ID > MaxStandardID {
		return bad("standard identifier 0x%X exceeds 11 bits", uint32(f.ID))
	}
	if f.Flags&FlagFD != 0 {
		if f.Flags&FlagRemote != 0 {
			return bad("CAN FD frames have no remote form")
		}
		if !ValidFDLen(int(f.Len)) {
			return bad("invalid CAN FD length %d", f.Len)
		}
		if f.Len8DLC != 0 {
			return bad("Len8DLC is for classical frames only")
		}
	} else {
		if f.Flags&(FlagBRS|FlagESI) != 0 {
			return bad("BRS and ESI need a CAN FD frame")
		}
		if f.Len > MaxClassicalLen {
			return bad("classical frame length %d exceeds 8", f.Len)
		}
		if f.Len8DLC != 0 && (f.Len != 8 || f.Len8DLC < 9 || f.Len8DLC > 15) {
			return bad("Len8DLC %d needs Len 8 and a value of 9..15", f.Len8DLC)
		}
	}
	start := int(f.Len)
	if f.Flags&FlagRemote != 0 {
		start = 0
	}
	for _, b := range f.Data[start:] {
		if b != 0 {
			return bad("data beyond the payload length is not zero")
		}
	}
	return nil
}

// String formats the frame in candump's compact notation: "123#A1B2C3",
// "12345678#" for extended identifiers, "123#R" (remote), "123##1A1B2"
// (CAN FD with its flags nibble).
func (f Frame) String() string {
	var b strings.Builder
	b.Grow(16 + 2*int(f.Len))
	f.appendCompact(&b)
	return b.String()
}

func (f *Frame) appendCompact(b *strings.Builder) {
	if f.Flags&(FlagExtended|FlagError) != 0 {
		fmt.Fprintf(b, "%08X", uint32(f.ID)|errIDBits(f.Flags))
	} else {
		fmt.Fprintf(b, "%03X", uint32(f.ID))
	}
	b.WriteByte('#')
	switch {
	case f.Flags&FlagFD != 0:
		b.WriteByte('#')
		var nib byte
		if f.Flags&FlagBRS != 0 {
			nib |= 1
		}
		if f.Flags&FlagESI != 0 {
			nib |= 2
		}
		b.WriteByte("0123456789ABCDEF"[nib])
	case f.Flags&FlagRemote != 0:
		b.WriteByte('R')
		if f.Len != 0 {
			b.WriteByte("0123456789ABCDEF"[f.Len&0xF])
		}
		return
	}
	b.WriteString(strings.ToUpper(hex.EncodeToString(f.Payload())))
	if f.Flags&FlagFD == 0 && f.Len == 8 && f.Len8DLC > 8 {
		b.WriteByte('_')
		b.WriteByte("0123456789ABCDEF"[f.Len8DLC&0xF])
	}
}

// errIDBits is the CAN_ERR_FLAG bit candump prints in error frame IDs.
func errIDBits(fl Flags) uint32 {
	if fl&FlagError != 0 {
		return 0x20000000
	}
	return 0
}
