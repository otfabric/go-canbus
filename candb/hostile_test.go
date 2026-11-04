// SPDX-License-Identifier: MIT

package candb

import (
	"errors"
	"math"
	"testing"
)

// TestHostileLayout: layouts no parser should produce, but a database
// built by hand or parsed with error diagnostics can hold, fail with
// ErrLayout instead of panicking. Before, a start bit near MaxInt
// overflowed the payload bound check and indexed out of range, a negative
// message length panicked in Decode and Encode, and a CAN FD message
// longer than 64 bytes panicked in Encode.
func TestHostileLayout(t *testing.T) {
	sig := func(start, n int, bo ByteOrder) *Signal {
		return &Signal{Name: "S", StartBit: start, Length: n, ByteOrder: bo, Factor: 1}
	}
	for _, s := range []*Signal{
		sig(math.MaxInt, 8, LittleEndian),
		sig(math.MaxInt-7, 8, LittleEndian),
		sig(math.MaxInt, 64, BigEndian),
		sig(math.MaxInt-64, 16, BigEndian),
		sig(maxStartBit+1, 1, LittleEndian),
		sig(-1, 8, BigEndian),
		sig(0, 0, LittleEndian),
		sig(0, 65, LittleEndian),
		sig(0, math.MinInt, LittleEndian),
	} {
		d := make([]byte, 64)
		if _, err := s.Extract(d); !errors.Is(err, ErrLayout) {
			t.Errorf("Extract(start %d, %d bits): %v", s.StartBit, s.Length, err)
		}
		if err := s.Insert(d, 1); !errors.Is(err, ErrLayout) {
			t.Errorf("Insert(start %d, %d bits): %v", s.StartBit, s.Length, err)
		}
		if s.Span() != math.MaxInt32 {
			t.Errorf("Span(start %d, %d bits) = %d", s.StartBit, s.Length, s.Span())
		}
	}
	// The largest start bit is accepted but needs a payload that long.
	if _, err := sig(maxStartBit, 8, LittleEndian).Extract(make([]byte, 64)); !errors.Is(err, ErrShortPayload) {
		t.Error(err)
	}

	for _, m := range []*Message{
		{Name: "Neg", Length: -1, Signals: []*Signal{sig(0, 8, LittleEndian)}},
		{Name: "MinInt", Length: math.MinInt},
		{Name: "Huge", Length: math.MaxInt},
		{Name: "FD70", Length: 70, FD: true, Signals: []*Signal{sig(552, 8, LittleEndian)}},
		{Name: "XL", Length: 2049},
	} {
		if _, err := m.EncodeWith(map[string]Input{}, EncodeOptions{FillMissing: true}); !errors.Is(err, ErrLayout) {
			t.Errorf("%s: Encode: %v", m.Name, err)
		}
		if m.Length < 0 {
			if _, err := m.Decode(make([]byte, 8)); !errors.Is(err, ErrLayout) {
				t.Errorf("%s: Decode: %v", m.Name, err)
			}
		}
	}
	// The boundaries still encode.
	for _, m := range []*Message{
		{Name: "Empty", Length: 0},
		{Name: "FD64", Length: 64, FD: true, Signals: []*Signal{sig(504, 8, LittleEndian)}},
		{Name: "Classical2048", Length: 2048, Signals: []*Signal{sig(16376, 8, LittleEndian)}},
	} {
		d, err := m.EncodeWith(map[string]Input{}, EncodeOptions{FillMissing: true})
		if err != nil || len(d) != m.Length {
			t.Errorf("%s: %d bytes, %v", m.Name, len(d), err)
		}
	}
}

// TestReinterpretation: the int64 and uint64 views of a raw value are
// two's complement reinterpretations of each other at every boundary.
func TestReinterpretation(t *testing.T) {
	for _, c := range []struct {
		s int64
		u uint64
	}{
		{0, 0}, {1, 1}, {-1, math.MaxUint64}, {math.MaxInt64, math.MaxInt64},
		{math.MinInt64, 1 << 63}, {math.MinInt64 + 1, 1<<63 + 1}, {-2, math.MaxUint64 - 1},
	} {
		if got := asUint64(c.s); got != c.u {
			t.Errorf("asUint64(%d) = %#x, want %#x", c.s, got, c.u)
		}
	}
	// Unsigned 64-bit raw values beyond MaxInt64 keep their pattern in
	// Value.Signed, and a label or RawInt of that pattern encodes it.
	u := &Signal{Name: "U", Length: 64, Factor: 1, Values: []ValueDescription{{Value: -1, Description: "All"}}}
	v := u.Value(math.MaxUint64)
	if v.Signed != -1 || v.Label != "All" {
		t.Fatalf("%+v", v)
	}
	if r, err := u.rawOf(Label("All"), EncodeOptions{}); err != nil || r != math.MaxUint64 {
		t.Fatal(r, err)
	}
	// A float32 signal ignores raw bits above 32.
	f := &Signal{Name: "F", Length: 32, ValueType: Float32, Factor: 1}
	if v := f.Value(0xFFFFFFFF_3F800000); v.Physical != 1 {
		t.Fatalf("%+v", v)
	}
	if r, err := f.rawOf(RawInt(-1), EncodeOptions{}); err == nil {
		t.Fatalf("float32 pattern %#x accepted", r)
	}
	d := &Signal{Name: "D", Length: 64, ValueType: Float64, Factor: 1}
	if r, err := d.rawOf(RawInt(math.MinInt64), EncodeOptions{}); err != nil || r != 1<<63 {
		t.Fatal(r, err)
	}
	if v := d.Value(1 << 63); v.Signed != math.MinInt64 || v.Physical != 0 {
		t.Fatalf("%+v", v)
	}
}

// FuzzMessageRoundTrip: for any valid layout of up to three signals
// (any byte order, sign, value type and length up to 64 bits), encoding
// the raw values decoded from a payload reproduces the payload's signal
// bits, and decoding the encoding returns the same values.
func FuzzMessageRoundTrip(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0}, uint64(0x0000_0040_0000_0100), uint64(0x0001_0203))
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 1}, uint64(0x3F00_0007_3F01_0020), uint64(0x0304_0506))
	f.Add(make([]byte, 64), uint64(0x0040_01F8_0000_0000), uint64(0xFFFF_FFFF))
	f.Fuzz(func(t *testing.T, data []byte, layout, kinds uint64) {
		if len(data) == 0 || len(data) > 64 {
			return
		}
		m := &Message{Name: "M", Length: len(data)}
		used := make([]bool, 8*len(data))
		for i := range 3 {
			l := layout >> (21 * i)
			k := kinds >> (8 * i)
			s := &Signal{Name: string(rune('A' + i)), StartBit: int(l&0x1FF) % (8 * len(data)), Length: int(l>>9&0x3F) + 1, Factor: 1}
			if l>>15&1 != 0 {
				s.ByteOrder = BigEndian
			}
			s.Signed = k&1 != 0
			switch k >> 1 & 3 {
			case 1:
				s.ValueType, s.Length = Float32, 32
			case 2:
				s.ValueType, s.Length = Float64, 64
			}
			pos, err := s.layout()
			if err != nil || pos+s.Length > 8*len(data) {
				continue
			}
			overlap := false
			for b := pos; b < pos+s.Length; b++ {
				// Linear indexes of both orders map to the same physical bit
				// only within the same byte order; mark physical bits.
				phys := b
				if s.ByteOrder == BigEndian {
					phys = b/8*8 + 7 - b%8
				}
				if used[phys] {
					overlap = true
				}
			}
			if overlap {
				continue
			}
			for b := pos; b < pos+s.Length; b++ {
				phys := b
				if s.ByteOrder == BigEndian {
					phys = b/8*8 + 7 - b%8
				}
				used[phys] = true
			}
			m.Signals = append(m.Signals, s)
		}
		dec, err := m.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		in := map[string]Input{}
		for _, d := range dec {
			if d.Signal.ValueType == Integer && d.Signal.Signed {
				in[d.Signal.Name] = RawInt(d.Value.Signed)
			} else {
				in[d.Signal.Name] = Raw(d.Value.Raw)
			}
		}
		enc, err := m.Encode(in)
		if err != nil {
			t.Fatal(err)
		}
		for i := range data {
			for b := range 8 {
				if used[8*i+b] && (enc[i]^data[i])>>b&1 != 0 {
					t.Fatalf("bit %d of byte %d differs: % x -> % x", b, i, data, enc)
				}
				if !used[8*i+b] && enc[i]>>b&1 != 0 {
					t.Fatalf("unused bit %d of byte %d set: % x", b, i, enc)
				}
			}
		}
		dec2, err := m.Decode(enc)
		if err != nil {
			t.Fatal(err)
		}
		for i := range dec {
			a, b := dec[i].Value, dec2[i].Value
			if a.Raw != b.Raw || a.Signed != b.Signed || math.Float64bits(a.Physical) != math.Float64bits(b.Physical) && !(math.IsNaN(a.Physical) && math.IsNaN(b.Physical)) {
				t.Fatalf("%s: %+v then %+v", dec[i].Signal.Name, a, b)
			}
		}
	})
}
