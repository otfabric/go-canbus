// SPDX-License-Identifier: MIT

package slcan

import (
	"errors"
	"testing"

	"github.com/otfabric/go-canbus"
)

// mustFrame returns a helper that unwraps a frame constructor's result.
func mustFrame(t testing.TB) func(canbus.Frame, error) canbus.Frame {
	return func(f canbus.Frame, err error) canbus.Frame {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
}

func TestWireRoundTrip(t *testing.T) {
	must := mustFrame(t)
	fd64 := make([]byte, 64)
	for i := range fd64 {
		fd64[i] = byte(i)
	}
	len8 := must(canbus.NewStandard(0x7FF, []byte{1, 2, 3, 4, 5, 6, 7, 8}))
	cases := []struct {
		f    canbus.Frame
		wire string
	}{
		{must(canbus.NewStandard(0x123, []byte{0xA1, 0xB2})), "t1232A1B2\r"},
		{must(canbus.NewStandard(0, nil)), "t0000\r"},
		{must(canbus.NewExtended(0x18DAF110, []byte{0x02, 0x10, 0x03})), "T18DAF1103021003\r"},
		{must(canbus.NewRemote(0x456, false, 3)), "r4563\r"},
		{must(canbus.NewRemote(0x1FFFFFFF, true, 8)), "R1FFFFFFF8\r"},
		{must(canbus.NewFD(0x10, false, false, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})), "d01090102030405060708090A0B0C\r"},
		{must(canbus.NewFD(0x10, true, true, fd64)), "B00000010F" + upperHex(fd64) + "\r"},
		{must(canbus.NewFD(0x7FF, false, true, nil)), "b7FF0\r"},
		{len8, "t7FF80102030405060708\r"},
	}
	for _, c := range cases {
		b, err := AppendFrame(nil, &c.f)
		if err != nil || string(b) != c.wire {
			t.Errorf("%v: %q %v, want %q", c.f, b, err, c.wire)
			continue
		}
		var g canbus.Frame
		ok, err := ParseFrame(b[:len(b)-1], &g)
		if !ok || err != nil || g.String() != c.f.String() || g.Flags != c.f.Flags {
			t.Errorf("%q: %v %v %v, want %v", b, g, ok, err, c.f)
		}
	}
}

func upperHex(b []byte) string {
	const d = "0123456789ABCDEF"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, d[c>>4], d[c&0xF])
	}
	return string(out)
}

func TestWireSendLimits(t *testing.T) {
	must := mustFrame(t)
	ef := canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrClassBusOff})
	esi := must(canbus.NewFD(1, false, false, []byte{1}))
	esi.Flags |= canbus.FlagESI
	for _, f := range []canbus.Frame{ef, esi} {
		if _, err := AppendFrame(nil, &f); !errors.Is(err, canbus.ErrUnsupported) {
			t.Errorf("%v: %v", f, err)
		}
	}
	bad := canbus.Frame{ID: 0x800, Len: 1}
	if _, err := AppendFrame(nil, &bad); err == nil {
		t.Error("invalid frame accepted")
	}
	// A classical DLC 9..15 goes out as 8.
	raw := must(canbus.NewStandard(1, []byte{1, 2, 3, 4, 5, 6, 7, 8}))
	raw.Len8DLC = 12
	if b, _ := AppendFrame(nil, &raw); string(b) != "t00180102030405060708\r" {
		t.Errorf("%q", b)
	}
}

func TestParseFrameVariants(t *testing.T) {
	for _, c := range []struct {
		line, want string
		ok         bool
	}{
		{"t1232A1B2", "123#A1B2", true},
		{"t1232a1b2", "123#A1B2", true},
		{"t1232A1B2EA60", "123#A1B2", true}, // Z1 timestamp
		{"r1232", "123#R2", true},
		{"r1232EA60", "123#R2", true},
		{"T000000010", "00000001#", true},
		{"tFFF0", "", false},
		{"", "", false},
		{"z", "", false},
		{"Z", "", false},
		{"O", "", false},
		{"S6", "", false},
		{"V1013", "", false},
		{"F00", "", false},
		{"t012C0102030405060708", "012#0102030405060708_C", true},
		{"d123F" + upperHex(make([]byte, 64)), "123##0" + upperHex(make([]byte, 64)), true},
	} {
		var f canbus.Frame
		ok, err := ParseFrame([]byte(c.line), &f)
		if c.want == "" && c.ok == false && c.line == "tFFF0" {
			if err == nil {
				t.Errorf("%q: accepted", c.line)
			}
			continue
		}
		if err != nil || ok != c.ok || ok && f.String() != c.want {
			t.Errorf("%q: %v %v %v, want %q", c.line, f.String(), ok, err, c.want)
		}
	}
	for _, bad := range []string{
		"t12", "tXYZ0", "t123", "t123G", "t1232A1", "t1232A1B2C", "t1232A1G2", "t1232A1B2EA6G",
		"T1234567", "T2000000000", "r1239", "d1239" + upperHex(make([]byte, 11)), "rXYZ1",
	} {
		var f canbus.Frame
		if ok, err := ParseFrame([]byte(bad), &f); err == nil {
			t.Errorf("%q: accepted (%v %v)", bad, ok, f)
		}
	}
}

func FuzzParseFrame(f *testing.F) {
	for _, s := range []string{"t1232A1B2", "T18DAF1103021003", "r4563", "R1FFFFFFF8", "d0109010203040506070809", "B00000010F", "t1232A1B2EA60", "z", "S6"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		var fr canbus.Frame
		ok, err := ParseFrame(line, &fr)
		if !ok || err != nil {
			return
		}
		if err := fr.Validate(); err != nil {
			t.Fatalf("%q: invalid frame %+v: %v", line, fr, err)
		}
		b, err := AppendFrame(nil, &fr)
		if err != nil {
			t.Fatalf("%q: cannot write back %v: %v", line, fr, err)
		}
		var g canbus.Frame
		if ok, err := ParseFrame(b[:len(b)-1], &g); !ok || err != nil || g.ID != fr.ID || g.Flags != fr.Flags || string(g.Payload()) != string(fr.Payload()) {
			t.Fatalf("%q → %q → %v (%v %v), want %v", line, b, g, ok, err, fr)
		}
	})
}
