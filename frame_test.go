// SPDX-License-Identifier: MIT

package canbus

import (
	"errors"
	"strings"
	"testing"
)

func TestDLCMapping(t *testing.T) {
	wantFD := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 12, 16, 20, 24, 32, 48, 64}
	for dlc := uint8(0); dlc < 16; dlc++ {
		if got := DLCToLen(dlc, true); got != wantFD[dlc] {
			t.Errorf("DLCToLen(%d, fd) = %d, want %d", dlc, got, wantFD[dlc])
		}
		want := int(dlc)
		if want > 8 {
			want = 8
		}
		if got := DLCToLen(dlc, false); got != want {
			t.Errorf("DLCToLen(%d, classical) = %d, want %d", dlc, got, want)
		}
		if got := LenToDLC(wantFD[dlc]); got != dlc {
			t.Errorf("LenToDLC(%d) = %d, want %d", wantFD[dlc], got, dlc)
		}
	}
	if DLCToLen(0x1F, true) != 64 {
		t.Error("high bits of the DLC must be ignored")
	}
	for n, want := range map[int]uint8{-1: 0, 9: 9, 12: 9, 13: 10, 33: 14, 49: 15, 64: 15, 65: 15} {
		if got := LenToDLC(n); got != want {
			t.Errorf("LenToDLC(%d) = %d, want %d", n, got, want)
		}
	}
	valid := map[int]bool{}
	for _, n := range wantFD {
		valid[n] = true
	}
	for n := -1; n <= 65; n++ {
		if ValidFDLen(n) != valid[n] {
			t.Errorf("ValidFDLen(%d) = %v", n, !valid[n])
		}
	}
	if RoundUpFDLen(13) != 16 || RoundUpFDLen(64) != 64 || RoundUpFDLen(100) != 64 {
		t.Error("RoundUpFDLen")
	}
}

func TestPadFD(t *testing.T) {
	in := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	out := PadFD(in, 0xCC)
	if len(out) != 12 || out[8] != 9 || out[9] != 0xCC || out[11] != 0xCC {
		t.Fatalf("PadFD = % x", out)
	}
	if got := PadFD(in[:8], 0); len(got) != 8 {
		t.Fatal("a valid length must not be padded")
	}
}

func TestConstructors(t *testing.T) {
	f, err := NewStandard(0x123, []byte{1, 2, 3})
	if err != nil || f.ID != 0x123 || f.Len != 3 || f.IsExtended() || f.IsFD() || f.DLC() != 3 {
		t.Fatalf("NewStandard = %+v, %v", f, err)
	}
	if _, err := NewStandard(0x800, nil); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("11-bit overflow: %v", err)
	}
	f, err = NewExtended(0x1FFFFFFF, make([]byte, 8))
	if err != nil || !f.IsExtended() {
		t.Fatalf("NewExtended: %v", err)
	}
	if _, err := NewExtended(0x20000000, nil); err == nil {
		t.Fatal("29-bit overflow accepted")
	}
	if _, err := NewStandard(1, make([]byte, 9)); err == nil {
		t.Fatal("classical 9 bytes accepted")
	}
	if _, err := NewStandard(1, make([]byte, 65)); err == nil {
		t.Fatal("65 bytes accepted")
	}
	f, err = NewFD(0x10, true, true, make([]byte, 64))
	if err != nil || !f.IsFD() || f.Flags&FlagBRS == 0 || f.DLC() != 15 {
		t.Fatalf("NewFD: %+v %v", f, err)
	}
	if _, err := NewFD(0x10, false, false, make([]byte, 9)); err == nil {
		t.Fatal("FD length 9 accepted")
	}
	f, err = NewRemote(0x7FF, false, 4)
	if err != nil || !f.IsRemote() || len(f.Payload()) != 0 || f.DLC() != 4 {
		t.Fatalf("NewRemote: %+v %v", f, err)
	}
	if _, err := NewRemote(1, false, 9); err == nil {
		t.Fatal("remote DLC 9 accepted")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		f    Frame
		ok   bool
	}{
		{"fd remote", Frame{Flags: FlagFD | FlagRemote}, false},
		{"brs without fd", Frame{Flags: FlagBRS}, false},
		{"esi without fd", Frame{Flags: FlagESI}, false},
		{"unknown flag", Frame{Flags: 0x80}, false},
		{"len8dlc ok", Frame{Len: 8, Len8DLC: 15}, true},
		{"len8dlc wrong len", Frame{Len: 7, Len8DLC: 9}, false},
		{"len8dlc out of range", Frame{Len: 8, Len8DLC: 16}, false},
		{"len8dlc on fd", Frame{Flags: FlagFD, Len: 8, Len8DLC: 9}, false},
		{"error frame", Frame{Flags: FlagError, ID: 0x40, Len: 8}, true},
		{"error frame too long", Frame{Flags: FlagError, Len: 9}, false},
		{"fd esi", Frame{Flags: FlagFD | FlagESI, Len: 12}, true},
	}
	for _, c := range cases {
		err := c.f.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: Validate = %v", c.name, err)
		}
	}
	f := Frame{Len: 2}
	f.Data[2] = 1
	if err := f.Validate(); err == nil {
		t.Error("garbage beyond Len accepted")
	}
	r := Frame{Flags: FlagRemote, Len: 2}
	r.Data[0] = 1
	if err := r.Validate(); err == nil {
		t.Error("data in a remote frame accepted")
	}
	var fe *FrameError
	if err := (&Frame{ID: 0x800}).Validate(); !errors.As(err, &fe) || !strings.Contains(err.Error(), "11 bits") {
		t.Errorf("FrameError: %v", err)
	}
}

func TestPayloadAndDLC(t *testing.T) {
	var f Frame
	f.SetPayload([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Len8DLC = 12
	if f.DLC() != 12 {
		t.Errorf("DLC with Len8DLC = %d", f.DLC())
	}
	f.SetPayload([]byte{9})
	if f.Len != 1 || f.Data[1] != 0 {
		t.Error("SetPayload must clear the rest")
	}
	f.Len = 200
	if len(f.Payload()) != 64 {
		t.Error("Payload must clamp")
	}
}

func TestFrameString(t *testing.T) {
	cases := map[string]Frame{
		"123#A1B2":               {ID: 0x123, Len: 2, Data: [64]byte{0xA1, 0xB2}},
		"1ABCDEF0#":              {ID: 0x1ABCDEF0, Flags: FlagExtended},
		"7FF#R":                  {ID: 0x7FF, Flags: FlagRemote},
		"001#R3":                 {ID: 1, Flags: FlagRemote, Len: 3},
		"010##311":               {ID: 0x10, Flags: FlagFD | FlagBRS | FlagESI, Len: 1, Data: [64]byte{0x11}},
		"010##0":                 {ID: 0x10, Flags: FlagFD},
		"20000040#00":            {ID: 0x40, Flags: FlagError, Len: 1},
		"001#0000000000000000_C": {ID: 1, Len: 8, Len8DLC: 12},
	}
	for want, f := range cases {
		if got := f.String(); got != want {
			t.Errorf("String = %q, want %q", got, want)
		}
	}
}

func TestFlagsString(t *testing.T) {
	if s := (FlagExtended | FlagFD | FlagBRS).String(); s != "EXT|FD|BRS" {
		t.Error(s)
	}
	if Flags(0).String() != "0" || Flags(0xC0).String() != "0xc0" {
		t.Error(Flags(0xC0).String())
	}
}

func TestErrorFrames(t *testing.T) {
	info := ErrorInfo{
		Class:      ErrClassController | ErrClassProtocol | ErrClassLostArbitration | ErrClassTransceiver,
		Controller: CtrlRxPassive, Protocol: ProtStuff | ProtTx, ProtocolLocation: 0x08,
		ArbitrationBit: 5, Transceiver: 0x04, ControllerSpecific: 9,
		TxErrors: 3, RxErrors: 130, CountersValid: true,
	}
	f := NewErrorFrame(info)
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	got, ok := ParseError(&f)
	info.Class |= ErrClassCounters
	if !ok || got != info {
		t.Fatalf("ParseError = %+v, want %+v", got, info)
	}
	s := got.String()
	for _, want := range []string{"controller", "rx-passive", "stuff|tx", "location=0x8", "arbitration-bit=5", "transceiver=0x4", "tx=3 rx=130"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q lacks %q", s, want)
		}
	}
	data, _ := NewStandard(1, nil)
	if _, ok := ParseError(&data); ok {
		t.Error("data frame parsed as error")
	}
	if ErrorClass(0).String() != "none" || ErrorClass(1<<20).String() != "0x100000" {
		t.Error(ErrorClass(1 << 20).String())
	}
	if ControllerStatus(0).String() != "unspecified" || ProtocolViolation(0xFF).String() != "bit|form|stuff|bit0|bit1|overload|active|tx" {
		t.Error("status names")
	}
	if ControllerStatus(0x80).String() != "0x80" {
		t.Error(ControllerStatus(0x80).String())
	}
}

func TestErrorsFormat(t *testing.T) {
	e := &OpError{Op: "send", Interface: "vcan0", Err: ErrInterfaceDown}
	if e.Error() != "canbus: send vcan0: canbus: interface down" || !errors.Is(e, ErrInterfaceDown) {
		t.Error(e.Error())
	}
	e.Interface = ""
	if e.Error() != "canbus: send: canbus: interface down" {
		t.Error(e.Error())
	}
}

func TestEnumStrings(t *testing.T) {
	for r, want := range map[DropReason]string{DropQueueFull: "queue-full", DropKernelOverflow: "kernel-overflow", DropFault: "fault", DropFiltered: "filtered", 0: "unknown"} {
		if r.String() != want {
			t.Errorf("%d: %s", r, r.String())
		}
	}
	if StateBusOff.String() != "bus-off" || State(99).String() != "unknown" {
		t.Error("state names")
	}
}
