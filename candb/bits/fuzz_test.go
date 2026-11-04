// SPDX-License-Identifier: MIT

package bits

import (
	"math"
	"testing"
)

// FuzzInsertExtract: inserting v into any field of any payload and
// extracting it returns v's low n bits, agrees with the math/big
// reference, and leaves every bit outside the field unchanged.
func FuzzInsertExtract(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0}, uint16(0), uint8(64), uint64(math.MaxUint64), false)
	f.Add([]byte{0xFF, 0, 0xFF, 0, 0xFF, 0, 0xFF, 0}, uint16(7), uint8(64), uint64(0x8000000000000001), true)
	f.Add(make([]byte, 64), uint16(511), uint8(1), uint64(1), false)
	f.Add(make([]byte, 64), uint16(448), uint8(64), uint64(0xDEADBEEFCAFEF00D), true)
	f.Fuzz(func(t *testing.T, d []byte, start uint16, length uint8, v uint64, big bool) {
		if len(d) == 0 || len(d) > 64 {
			return
		}
		n := int(length%64) + 1
		pos := int(start) % (8 * len(d))
		if pos+n > 8*len(d) {
			return
		}
		out := append([]byte(nil), d...)
		var got, ref uint64
		if big {
			InsertBE(out, pos, n, v)
			got, ref = ExtractBE(out, pos, n), refBE(out, pos, n)
		} else {
			InsertLE(out, pos, n, v)
			got, ref = ExtractLE(out, pos, n), refLE(out, pos, n)
		}
		if want := Truncate(v, n); got != want || ref != want {
			t.Fatalf("n=%d pos=%d big=%v: extract %#x, reference %#x, want %#x", n, pos, big, got, ref, want)
		}
		// Clearing the field in both payloads must make them equal.
		a, b := append([]byte(nil), d...), out
		if big {
			InsertBE(a, pos, n, 0)
			InsertBE(b, pos, n, 0)
		} else {
			InsertLE(a, pos, n, 0)
			InsertLE(b, pos, n, 0)
		}
		if string(a) != string(b) {
			t.Fatalf("n=%d pos=%d big=%v: bits outside the field changed", n, pos, big)
		}
		// Sign extension inverts truncation of the two's complement.
		s := SignExtend(v, n)
		if !FitsSigned(s, n) || Truncate(uint64(s), n) != Truncate(v, n) {
			t.Fatalf("SignExtend(%#x, %d) = %d", v, n, s)
		}
	})
}

// TestReinterpretation: asInt64 is the two's complement reinterpretation
// at every boundary.
func TestReinterpretation(t *testing.T) {
	for _, c := range []struct {
		u uint64
		s int64
	}{
		{0, 0},
		{1, 1},
		{math.MaxInt64, math.MaxInt64},
		{1 << 63, math.MinInt64},
		{1<<63 + 1, math.MinInt64 + 1},
		{math.MaxUint64, -1},
		{math.MaxUint64 - 1, -2},
	} {
		if got := asInt64(c.u); got != c.s {
			t.Errorf("asInt64(%#x) = %d, want %d", c.u, got, c.s)
		}
		if got := SignExtend(c.u, 64); got != c.s {
			t.Errorf("SignExtend(%#x, 64) = %d, want %d", c.u, got, c.s)
		}
		if got := SignExtend(c.u, 100); got != c.s {
			t.Errorf("SignExtend(%#x, 100) = %d, want %d", c.u, got, c.s)
		}
	}
	for n := 1; n < 64; n++ {
		top := uint64(1) << uint(n-1)
		if got := SignExtend(top, n); got != -int64(top) {
			t.Errorf("SignExtend(min, %d) = %d", n, got)
		}
		if got := SignExtend(top-1, n); got != int64(top-1) {
			t.Errorf("SignExtend(max, %d) = %d", n, got)
		}
		if got := SignExtend(top|^(top<<1-1), n); got != -int64(top) {
			t.Errorf("SignExtend ignores bits above %d: %d", n, got)
		}
	}
}
