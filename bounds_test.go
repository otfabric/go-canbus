// SPDX-License-Identifier: MIT

package canbus

import (
	"errors"
	"log/slog"
	"testing"
)

// TestFrameLengthBounds: payloads beyond 64 bytes never truncate into
// Len: constructors reject them and SetPayload keeps the first 64.
func TestFrameLengthBounds(t *testing.T) {
	for _, n := range []int{65, 256, 257, 1 << 16} {
		if _, err := NewFD(1, false, false, make([]byte, n)); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("NewFD(%d bytes): %v", n, err)
		}
		if _, err := NewStandard(1, make([]byte, n)); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("NewStandard(%d bytes): %v", n, err)
		}
		var f Frame
		f.Flags = FlagFD
		data := make([]byte, n)
		for i := range data {
			data[i] = byte(i)
		}
		f.SetPayload(data)
		if f.Len != MaxFDLen || len(f.Payload()) != MaxFDLen || f.Data[63] != 63 {
			t.Errorf("SetPayload(%d bytes): Len %d", n, f.Len)
		}
	}
	f, err := NewFD(1, false, false, make([]byte, MaxFDLen))
	if err != nil || f.Len != MaxFDLen {
		t.Fatal(f.Len, err)
	}
	f.SetPayload(nil)
	if f.Len != 0 || f.Data != [MaxFDLen]byte{} {
		t.Fatal("SetPayload(nil) left data")
	}
}

// TestStateRoundTrip: every State survives the bus's atomic storage.
func TestStateRoundTrip(t *testing.T) {
	b := &Bus{metrics: NopMetrics{}, log: slog.New(discardHandler{})}
	for s := StateUnknown; s <= StateClosed; s++ {
		b.setState(s)
		if got := b.State(); got != s {
			t.Fatalf("State() = %v after setState(%v)", got, s)
		}
	}
	b.setState(State(255))
	if got := b.State(); got != State(255) {
		t.Fatalf("State() = %d", got)
	}
}

// TestParseXLFrameHeaderBounds: the CAN XL header fields take their full
// 8, 8 and 32 bit ranges and nothing beyond.
func TestParseXLFrameHeaderBounds(t *testing.T) {
	f, err := ParseXLFrame("7FF#00:FF:FFFFFFFF#00")
	if err != nil {
		t.Fatal(err)
	}
	if f.SDT != 0xFF || f.AF != 0xFFFFFFFF || f.Priority != 0x7FF {
		t.Fatalf("%+v", f)
	}
	for _, s := range []string{
		"7FF#100:FF:FFFFFFFF#00",
		"7FF#FF:100:FFFFFFFF#00",
		"7FF#FF:FF:100000000#00",
		"7FF#-1:FF:FFFFFFFF#00",
		"800#00:00:00000000#00",
	} {
		if _, err := ParseXLFrame(s); err == nil {
			t.Errorf("%s accepted", s)
		}
	}
}
