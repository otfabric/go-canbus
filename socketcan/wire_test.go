// SPDX-License-Identifier: MIT

package socketcan

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/otfabric/go-canbus"
)

func TestWireRoundTrip(t *testing.T) {
	mustF := func(f canbus.Frame, err error) canbus.Frame {
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	frames := []canbus.Frame{
		mustF(canbus.NewStandard(0x123, []byte{1, 2, 3})),
		mustF(canbus.NewExtended(0x1ABCDEF0, []byte{1, 2, 3, 4, 5, 6, 7, 8})),
		mustF(canbus.NewRemote(0x7FF, false, 5)),
		mustF(canbus.NewRemote(0x1234, true, 0)),
		mustF(canbus.NewFD(0x10, false, true, bytes.Repeat([]byte{0xAA}, 64))),
		mustF(canbus.NewFD(0x10, true, false, make([]byte, 12))),
		{ID: 1, Len: 8, Len8DLC: 13},
		{ID: 0x22, Flags: canbus.FlagFD | canbus.FlagESI, Len: 0},
		canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrClassBusOff, TxErrors: 255, CountersValid: true}),
	}
	for _, f := range frames {
		var buf [canFDMTU]byte
		n := marshal(&f, &buf)
		var g canbus.Frame
		if err := unmarshal(buf[:n], &g); err != nil {
			t.Fatalf("%v: %v", f, err)
		}
		if g.ID != f.ID || g.Flags != f.Flags || g.Len != f.Len || g.Data != f.Data || g.Len8DLC != f.Len8DLC {
			t.Errorf("round trip %v -> %v (%+v)", f, g, g)
		}
		wantN := canMTU
		if f.IsFD() {
			wantN = canFDMTU
		}
		if n != wantN {
			t.Errorf("%v: size %d", f, n)
		}
	}
}

func TestWireLayout(t *testing.T) {
	f, _ := canbus.NewFD(0x123, true, true, []byte{9, 8})
	var buf [canFDMTU]byte
	marshal(&f, &buf)
	if binary.NativeEndian.Uint32(buf[:4]) != 0x80000123 || buf[4] != 2 || buf[5] != fdBRS|fdFDF || buf[8] != 9 || buf[9] != 8 {
		t.Fatalf("layout % x", buf[:16])
	}
}

func TestUnmarshalRejects(t *testing.T) {
	var f canbus.Frame
	if err := unmarshal(make([]byte, 10), &f); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Error(err)
	}
	b := make([]byte, canMTU)
	b[4] = 9
	if err := unmarshal(b, &f); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Error("classical length 9 accepted")
	}
	fd := make([]byte, canFDMTU)
	fd[4] = 13 // not a valid FD length: rounded up
	if err := unmarshal(fd, &f); err != nil || f.Len != 16 {
		t.Error(f.Len, err)
	}
	fd[4] = 65
	if err := unmarshal(fd, &f); err == nil {
		t.Error("FD length 65 accepted")
	}
}

func TestKernelFilters(t *testing.T) {
	b := kernelFilters(canbus.FilterSet{})
	if len(b) != 8 || binary.NativeEndian.Uint64(b) != 0 {
		t.Fatal("empty set must be one match-all filter")
	}
	b = kernelFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactExtended(5), canbus.ExactStandard(6)}})
	if len(b) != 16 || binary.NativeEndian.Uint32(b[0:]) != 0x80000005 || binary.NativeEndian.Uint32(b[12:]) != 0x800007FF {
		t.Fatalf("% x", b)
	}
}

func FuzzUnmarshal(f *testing.F) {
	f.Add(make([]byte, canMTU))
	f.Add(make([]byte, canFDMTU))
	f.Fuzz(func(t *testing.T, b []byte) {
		var fr canbus.Frame
		if err := unmarshal(b, &fr); err != nil {
			return
		}
		if fr.Flags&canbus.FlagError == 0 {
			if err := fr.Validate(); err != nil {
				t.Fatalf("decoded an invalid frame %+v: %v", fr, err)
			}
		}
		var buf [canFDMTU]byte
		n := marshal(&fr, &buf)
		var again canbus.Frame
		if err := unmarshal(buf[:n], &again); err != nil || again.ID != fr.ID || again.Flags != fr.Flags || again.Data != fr.Data || again.Len != fr.Len {
			t.Fatalf("re-encode changed %+v to %+v (%v)", fr, again, err)
		}
	})
}

func TestWireXL(t *testing.T) {
	f := canbus.XLFrame{Priority: 0x242, VCID: 0x7F, Flags: canbus.XLSEC, SDT: 3, AF: 0xDEADBEEF, Data: bytes.Repeat([]byte{0xA5}, 300)}
	buf := make([]byte, canXLMTU)
	n := marshalXL(&f, buf)
	if n != canXLHdr+300 || buf[4] != 0x81 || binary.NativeEndian.Uint32(buf[0:4]) != 0x7F0242 || binary.NativeEndian.Uint16(buf[6:8]) != 300 {
		t.Fatalf("% X", buf[:canXLHdr])
	}
	g := canbus.XLFrame{Data: make([]byte, 0, 4)}
	if err := unmarshalXL(buf[:n], &g); err != nil {
		t.Fatal(err)
	}
	if g.String() != f.String() {
		t.Fatalf("%s, want %s", g, f)
	}
	// A CAN or CAN FD frame is not CAN XL; a wrong length is rejected.
	var fd [canFDMTU]byte
	cf, _ := canbus.NewFD(0x123, false, false, []byte{1})
	marshal(&cf, &fd)
	if isXL(fd[:]) || unmarshalXL(fd[:], &g) == nil {
		t.Fatal("CAN FD frame taken for CAN XL")
	}
	for _, cut := range []int{n - 1, canXLHdr} {
		if err := unmarshalXL(buf[:cut], &g); err == nil {
			t.Fatalf("%d bytes accepted", cut)
		}
	}
	binary.NativeEndian.PutUint16(buf[6:8], 0)
	if err := unmarshalXL(buf[:canXLHdr+1], &g); err == nil {
		t.Fatal("length 0 accepted")
	}
}
