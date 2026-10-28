// SPDX-License-Identifier: MIT

package canbus

import (
	"errors"
	"testing"
)

func mk(id ID, fl Flags) *Frame { return &Frame{ID: id, Flags: fl} }

func TestFilterMatch(t *testing.T) {
	cases := []struct {
		name string
		f    Filter
		fr   *Frame
		want bool
	}{
		{"exact std", ExactStandard(0x123), mk(0x123, 0), true},
		{"exact std other", ExactStandard(0x123), mk(0x124, 0), false},
		{"exact std vs ext same number", ExactStandard(0x123), mk(0x123, FlagExtended), false},
		{"exact ext", ExactExtended(0x18FF0001), mk(0x18FF0001, FlagExtended), true},
		{"exact ext vs std", ExactExtended(0x123), mk(0x123, 0), false},
		{"mask range", Filter{ID: 0x100, Mask: 0x700}, mk(0x1AB, 0), true},
		{"mask range miss", Filter{ID: 0x100, Mask: 0x700}, mk(0x2AB, 0), false},
		// Kernel semantics: FormatAny with an 11-bit mask also matches
		// extended identifiers with the same low bits.
		{"any format low bits", Filter{ID: 0x123, Mask: 0x7FF}, mk(0x10000123, FlagExtended), true},
		{"invert", Filter{ID: 0x123, Mask: 0x7FF, Format: FormatStandard, Invert: true}, mk(0x124, 0), true},
		{"invert hit", Filter{ID: 0x123, Mask: 0x7FF, Format: FormatStandard, Invert: true}, mk(0x123, 0), false},
		{"data only rejects rtr", Filter{Remote: RemoteDataOnly}, mk(1, FlagRemote), false},
		{"data only data", Filter{Remote: RemoteDataOnly}, mk(1, 0), true},
		{"rtr only", Filter{Remote: RemoteOnly}, mk(1, FlagRemote), true},
		{"rtr only data", Filter{Remote: RemoteOnly}, mk(1, 0), false},
		{"error frame never", Filter{}, mk(0x40, FlagError), false},
		{"match all", Filter{}, mk(0x1FFFFFFF, FlagExtended|FlagFD), true},
	}
	for _, c := range cases {
		if got := c.f.Match(c.fr); got != c.want {
			id, mask := c.f.Kernel()
			t.Errorf("%s: Match = %v (kernel %08X/%08X, frame %08X)", c.name, got, id, mask, KernelID(c.fr))
		}
	}
}

func TestKernelEncoding(t *testing.T) {
	id, mask := ExactExtended(0x1234).Kernel()
	if id != 0x80001234 || mask != 0x9FFFFFFF {
		t.Errorf("ext = %08X/%08X", id, mask)
	}
	id, mask = Filter{ID: 0x7FF, Mask: 0x7FF, Format: FormatStandard, Remote: RemoteOnly, Invert: true}.Kernel()
	if id != 0x600007FF || mask != 0xC00007FF {
		t.Errorf("std = %08X/%08X", id, mask)
	}
	if KernelID(mk(0x45, FlagError)) != 0x20000045 || KernelID(mk(0xFFF, 0)) != 0x7FF || KernelID(mk(1, FlagRemote|FlagExtended)) != 0xC0000001 {
		t.Error("KernelID")
	}
}

func TestFilterSet(t *testing.T) {
	var empty FilterSet
	if !empty.Match(mk(5, 0)) || empty.Match(mk(0x40, FlagError)) {
		t.Error("empty set: all data frames, no error frames")
	}
	s := FilterSet{Filters: []Filter{ExactStandard(1), ExactStandard(2)}}
	if !s.Match(mk(2, 0)) || s.Match(mk(3, 0)) {
		t.Error("OR")
	}
	j := FilterSet{Join: true, Filters: []Filter{{ID: 0x100, Mask: 0x700}, {ID: 0x0F, Mask: 0x0F}}}
	if !j.Match(mk(0x10F, 0)) || j.Match(mk(0x100, 0)) {
		t.Error("JOIN")
	}
	e := FilterSet{ErrorMask: ErrClassBusOff}
	if !e.Match(mk(ID(ErrClassBusOff), FlagError)) || e.Match(mk(ID(ErrClassNoAck), FlagError)) {
		t.Error("error mask")
	}
}

func TestParseFilter(t *testing.T) {
	cases := map[string]Filter{
		"123:7FF":             {ID: 0x123, Mask: 0x7FF},
		"12345678:1FFFFFFF":   {ID: 0x12345678, Mask: 0x1FFFFFFF, Format: FormatExtended},
		"123~7FF,std":         {ID: 0x123, Mask: 0x7FF, Format: FormatStandard, Invert: true},
		"001:0,ext,rtr":       {ID: 1, Format: FormatExtended, Remote: RemoteOnly},
		"00000001:0,any,data": {ID: 1, Remote: RemoteDataOnly},
	}
	for in, want := range cases {
		got, err := ParseFilter(in)
		if err != nil || got != want {
			t.Errorf("ParseFilter(%q) = %+v, %v", in, got, err)
			continue
		}
		back, err := ParseFilter(got.String())
		if err != nil || back != got {
			t.Errorf("round trip of %q via %q = %+v, %v", in, got.String(), back, err)
		}
	}
	for _, bad := range []string{"123", "xyz:7FF", "123:zz", "123:7FF,foo", "20000000:0", "1:20000000"} {
		if _, err := ParseFilter(bad); !errors.Is(err, ErrInvalidOption) {
			t.Errorf("ParseFilter(%q) = %v", bad, err)
		}
	}
}

// FuzzFilter checks the software matcher against the kernel's expression,
// written out independently here from net/can/af_can.c and raw.c.
func FuzzFilter(f *testing.F) {
	f.Add(uint32(0x123), uint32(0x7FF), uint8(1), uint8(0), false, uint32(0x123), uint8(0))
	f.Add(uint32(0x1234), uint32(0x1FFFFFFF), uint8(2), uint8(2), true, uint32(0x1234), uint8(FlagExtended|FlagRemote))
	f.Fuzz(func(t *testing.T, fid, fmask uint32, format, remote uint8, inv bool, frid uint32, frflags uint8) {
		flt := Filter{ID: ID(fid & 0x1FFFFFFF), Mask: ID(fmask & 0x1FFFFFFF), Format: Format(format % 3), Remote: RemoteMatch(remote % 3), Invert: inv}
		fr := Frame{ID: ID(frid & 0x1FFFFFFF), Flags: Flags(frflags) & (FlagExtended | FlagRemote)}
		if fr.Flags&FlagExtended == 0 {
			fr.ID &= 0x7FF
		}
		// Reference.
		canID := uint32(fr.ID)
		if fr.Flags&FlagExtended != 0 {
			canID |= 0x80000000
		}
		if fr.Flags&FlagRemote != 0 {
			canID |= 0x40000000
		}
		kid, kmask := flt.Kernel()
		want := (canID & kmask) == (kid &^ 0x20000000 & kmask)
		if kid&0x20000000 != 0 {
			want = !want
		}
		if got := flt.Match(&fr); got != want {
			t.Fatalf("Match = %v, kernel = %v", got, want)
		}
		if p, err := ParseFilter(flt.String()); err != nil || p != flt {
			t.Fatalf("round trip %q: %+v %v", flt.String(), p, err)
		}
	})
}

// TestFilterMatchXL pins the kernel's CAN_RAW_FILTER decisions for CAN XL
// frames, observed on Linux 7.0 (vcan, CAN_RAW_XL_FRAMES with a VCID
// receive mask of 0): the filter sees VCID<<16 | priority as a standard
// data frame identifier.
func TestFilterMatchXL(t *testing.T) {
	frames := []string{"00242#80:00:00000000#01", "00243#80:00:00000000#02", "05242#80:00:00000000#03", "05243#80:00:00000000#04"}
	cases := []struct {
		name string
		fs   FilterSet
		want string // the frames that pass, by index
	}{
		{"none", FilterSet{}, "0123"},
		{"exact standard", FilterSet{Filters: []Filter{ExactStandard(0x242)}}, "02"},
		{"mask only", FilterSet{Filters: []Filter{{ID: 0x242, Mask: 0x7FF}}}, "02"},
		{"inverted", FilterSet{Filters: []Filter{{ID: 0x242, Mask: 0x7FF, Invert: true}}}, "13"},
		{"vcid bits", FilterSet{Filters: []Filter{{ID: 0x50242, Mask: 0xFF07FF}}}, "2"},
		{"extended", FilterSet{Filters: []Filter{ExactExtended(0x242)}}, ""},
		{"remote only", FilterSet{Filters: []Filter{{ID: 0x242, Mask: 0x7FF, Remote: RemoteOnly}}}, ""},
		{"join", FilterSet{Join: true, Filters: []Filter{{ID: 0x242, Mask: 0x7FF}, {ID: 5 << 16, Mask: 0xFF << 16}}}, "2"},
	}
	for _, c := range cases {
		got := ""
		for i, s := range frames {
			x, err := ParseXLFrame(s)
			if err != nil {
				t.Fatal(err)
			}
			if c.fs.MatchXL(&x) {
				got += string(rune('0' + i))
			}
			if len(c.fs.Filters) == 1 && c.fs.Filters[0].MatchXL(&x) != c.fs.MatchXL(&x) {
				t.Errorf("%s: Filter.MatchXL disagrees with the set", c.name)
			}
		}
		if got != c.want {
			t.Errorf("%s: %q pass, want %q", c.name, got, c.want)
		}
	}
	if id := XLKernelID(&XLFrame{Priority: 0x7FF, VCID: 0xAB}); id != 0xAB07FF {
		t.Errorf("XLKernelID = %#x", id)
	}
}
