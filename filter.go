// SPDX-License-Identifier: MIT

package canbus

import (
	"fmt"
	"strconv"
	"strings"
)

// Format selects identifier formats in a [Filter].
type Format uint8

// Identifier formats.
const (
	FormatAny      Format = iota // standard and extended identifiers
	FormatStandard               // 11-bit identifiers only
	FormatExtended               // 29-bit identifiers only
)

// RemoteMatch selects data or remote frames in a [Filter].
type RemoteMatch uint8

// Remote frame selection.
const (
	RemoteAny      RemoteMatch = iota // data and remote frames
	RemoteDataOnly                    // data frames only
	RemoteOnly                        // remote frames only
)

// Linux flag bits of can_id, as used by CAN_RAW_FILTER.
const (
	linuxEFF = 0x80000000 // CAN_EFF_FLAG
	linuxRTR = 0x40000000 // CAN_RTR_FLAG
	linuxERR = 0x20000000 // CAN_ERR_FLAG
	linuxINV = 0x20000000 // CAN_INV_FILTER (in a filter's can_id)
)

// Filter selects frames by identifier: a frame matches when
// (frameID & Mask) == (ID & Mask), restricted by Format and Remote, and
// negated by Invert.
//
// The semantics are those of Linux's CAN_RAW_FILTER: [Filter.Kernel]
// returns the can_filter the kernel would be given, and the software
// matcher evaluates exactly that expression on the kernel's flag-encoded
// identifier. In particular, with FormatAny a mask of 0x7FF also matches
// extended identifiers whose low 11 bits agree, as in the kernel.
type Filter struct {
	ID     ID
	Mask   ID
	Format Format
	Remote RemoteMatch
	Invert bool
}

// ExactStandard returns a filter for one 11-bit identifier.
func ExactStandard(id ID) Filter {
	return Filter{ID: id, Mask: MaxStandardID, Format: FormatStandard}
}

// ExactExtended returns a filter for one 29-bit identifier.
func ExactExtended(id ID) Filter {
	return Filter{ID: id, Mask: MaxExtendedID, Format: FormatExtended}
}

// Kernel returns the Linux can_filter values (can_id, can_mask) of f.
func (f Filter) Kernel() (id, mask uint32) {
	id = uint32(f.ID) & uint32(MaxExtendedID)
	mask = uint32(f.Mask) & uint32(MaxExtendedID)
	switch f.Format {
	case FormatStandard:
		mask |= linuxEFF
	case FormatExtended:
		mask |= linuxEFF
		id |= linuxEFF
	}
	switch f.Remote {
	case RemoteDataOnly:
		mask |= linuxRTR
	case RemoteOnly:
		mask |= linuxRTR
		id |= linuxRTR
	}
	if f.Invert {
		id |= linuxINV
	}
	return id, mask
}

// KernelID returns the Linux can_id of a frame: the identifier with
// CAN_EFF_FLAG, CAN_RTR_FLAG and CAN_ERR_FLAG.
func KernelID(fr *Frame) uint32 {
	id := uint32(fr.ID)
	if fr.Flags&FlagExtended != 0 {
		id = id&uint32(MaxExtendedID) | linuxEFF
	} else if fr.Flags&FlagError == 0 {
		id &= uint32(MaxStandardID)
	}
	if fr.Flags&FlagRemote != 0 {
		id |= linuxRTR
	}
	if fr.Flags&FlagError != 0 {
		id = id&uint32(ErrClassAll) | linuxERR
	}
	return id
}

// Match reports whether a data or remote frame passes the filter. Error
// frames are selected by an error mask instead; Match reports false for
// them.
func (f Filter) Match(fr *Frame) bool {
	if fr.Flags&FlagError != 0 {
		return false
	}
	return f.matchKernelID(KernelID(fr))
}

func (f Filter) matchKernelID(cid uint32) bool {
	id, mask := f.Kernel()
	eq := cid&mask == id&^linuxINV&mask
	if id&linuxINV != 0 {
		return !eq
	}
	return eq
}

// String formats the filter as candump does: "123:7FF", "12345678:1FFFFFFF"
// for extended (with an 8-digit identifier), "123~7FF" when inverted, with
// ",std" / ",any" / ",data" / ",rtr" for what candump's notation cannot
// spell. [ParseFilter] reads it back unchanged.
func (f Filter) String() string {
	var b strings.Builder
	if f.Format == FormatExtended {
		fmt.Fprintf(&b, "%08X", uint32(f.ID))
	} else {
		fmt.Fprintf(&b, "%03X", uint32(f.ID))
	}
	if f.Invert {
		b.WriteByte('~')
	} else {
		b.WriteByte(':')
	}
	b.WriteString(strings.ToUpper(strconv.FormatUint(uint64(f.Mask), 16)))
	switch {
	case f.Format == FormatStandard:
		b.WriteString(",std")
	case f.Format == FormatAny && f.ID > 0xFFF:
		b.WriteString(",any") // more than 3 digits would parse as extended
	}
	switch f.Remote {
	case RemoteDataOnly:
		b.WriteString(",data")
	case RemoteOnly:
		b.WriteString(",rtr")
	}
	return b.String()
}

// ParseFilter parses the notation of [Filter.String] and candump's:
// "<id>:<mask>" or "<id>~<mask>" (inverted), hexadecimal; an identifier of
// more than 3 digits selects extended identifiers. Suffixes ",std", ",ext",
// ",any", ",data" and ",rtr" set Format and Remote explicitly.
func ParseFilter(s string) (Filter, error) {
	var f Filter
	parts := strings.Split(s, ",")
	head := parts[0]
	sep := strings.IndexAny(head, ":~")
	if sep < 0 {
		return f, fmt.Errorf("%w: filter %q: want <id>:<mask> or <id>~<mask>", ErrInvalidOption, s)
	}
	f.Invert = head[sep] == '~'
	idText, maskText := head[:sep], head[sep+1:]
	id, err := strconv.ParseUint(idText, 16, 32)
	if err != nil || id > uint64(MaxExtendedID) {
		return f, fmt.Errorf("%w: filter %q: bad identifier", ErrInvalidOption, s)
	}
	mask, err := strconv.ParseUint(maskText, 16, 32)
	if err != nil || mask > uint64(MaxExtendedID) {
		return f, fmt.Errorf("%w: filter %q: bad mask", ErrInvalidOption, s)
	}
	f.ID, f.Mask = ID(id), ID(mask)
	if len(idText) > 3 {
		f.Format = FormatExtended
	}
	for _, p := range parts[1:] {
		switch p {
		case "std":
			f.Format = FormatStandard
		case "ext":
			f.Format = FormatExtended
		case "any":
			f.Format = FormatAny
		case "data":
			f.Remote = RemoteDataOnly
		case "rtr":
			f.Remote = RemoteOnly
		default:
			return f, fmt.Errorf("%w: filter %q: unknown option %q", ErrInvalidOption, s, p)
		}
	}
	return f, nil
}

// FilterSet is a list of filters with the combination rule of
// CAN_RAW_FILTER: a frame passes when it matches any filter, or, with
// Join (CAN_RAW_JOIN_FILTERS), all of them. An empty set passes every
// data and remote frame. ErrorMask selects the error frames that pass
// (CAN_RAW_ERR_FILTER); zero passes none.
type FilterSet struct {
	Filters   []Filter
	Join      bool
	ErrorMask ErrorClass
}

// Match reports whether the frame passes the set.
func (s *FilterSet) Match(fr *Frame) bool {
	if fr.Flags&FlagError != 0 {
		return ErrorClass(fr.ID)&s.ErrorMask != 0
	}
	if len(s.Filters) == 0 {
		return true
	}
	cid := KernelID(fr)
	if s.Join {
		for _, f := range s.Filters {
			if !f.matchKernelID(cid) {
				return false
			}
		}
		return true
	}
	for _, f := range s.Filters {
		if f.matchKernelID(cid) {
			return true
		}
	}
	return false
}
