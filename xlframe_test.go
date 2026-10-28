// SPDX-License-Identifier: MIT

package canbus_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus"
)

func TestXLFrameText(t *testing.T) {
	for _, c := range []struct {
		in, out string
		want    canbus.XLFrame
	}{
		{"00242#80:01:00000000#AABBCC", "00242#80:01:00000000#AABBCC", canbus.XLFrame{Priority: 0x242, SDT: 1, Data: []byte{0xAA, 0xBB, 0xCC}}},
		{"242#81:00:12345678#11.22", "00242#81:00:12345678#1122", canbus.XLFrame{Priority: 0x242, Flags: canbus.XLSEC, AF: 0x12345678, Data: []byte{0x11, 0x22}}},
		{"7F7FF#03:FF:FFFFFFFF#00", "7F7FF#83:FF:FFFFFFFF#00", canbus.XLFrame{VCID: 0x7F, Priority: 0x7FF, Flags: canbus.XLSEC | canbus.XLRRS, SDT: 0xFF, AF: 0xFFFFFFFF, Data: []byte{0}}},
	} {
		f, err := canbus.ParseXLFrame(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if f.Priority != c.want.Priority || f.VCID != c.want.VCID || f.Flags != c.want.Flags || f.SDT != c.want.SDT || f.AF != c.want.AF || !bytes.Equal(f.Data, c.want.Data) {
			t.Fatalf("%s: %+v, want %+v", c.in, f, c.want)
		}
		if f.String() != c.out {
			t.Fatalf("%s: String %s, want %s", c.in, f.String(), c.out)
		}
		if !canbus.IsXLText(c.in) {
			t.Fatalf("IsXLText(%s)", c.in)
		}
	}
	for _, bad := range []string{"", "242", "2420#80:00:00000000#11", "242#80:00:00000000#", "242#80-00:00000000#11", "242#80:00:0000000#11",
		"242#80:00:00000000#1", "242#80:00:00000000#ZZ", "800#80:00:00000000#11", "XX242#80:00:00000000#11", "242#G0:00:00000000#11",
		"242#80:04:00000000#11" + strings.Repeat("00", 2048), "242#84:00:00000000#11"} {
		if _, err := canbus.ParseXLFrame(bad); !errors.Is(err, canbus.ErrInvalidFrame) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for _, s := range []string{"123#1122", "123##1AA", "12345678#", "123#R"} {
		if canbus.IsXLText(s) {
			t.Errorf("IsXLText(%s)", s)
		}
	}
	max := canbus.XLFrame{Data: make([]byte, canbus.MaxXLLen)}
	if err := max.Validate(); err != nil {
		t.Fatal(err)
	}
	if canbus.XLFlags(0).String() != "0" || (canbus.XLSEC|canbus.XLRRS|0x10).String() != "SEC|RRS|0x10" {
		t.Fatal("XLFlags.String")
	}
}

func FuzzParseXLFrame(f *testing.F) {
	for _, s := range []string{"00242#80:01:00000000#AABBCC", "242#81:00:12345678#11.22", "7F7FF#83:FF:FFFFFFFF#00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		x, err := canbus.ParseXLFrame(s)
		if err != nil {
			return
		}
		if err := x.Validate(); err != nil {
			t.Fatalf("%q parsed to an invalid frame: %v", s, err)
		}
		y, err := canbus.ParseXLFrame(x.String())
		if err != nil || y.String() != x.String() {
			t.Fatalf("%q → %s → %v %v", s, x.String(), y, err)
		}
	})
}
