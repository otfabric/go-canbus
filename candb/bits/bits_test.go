// SPDX-License-Identifier: MIT

package bits

import (
	"math/big"
	"math/rand"
	"testing"
)

// Reference implementations through math/big, as the DBC layouts are
// defined: the payload as one little- or big-endian integer.
func refLE(d []byte, start, n int) uint64 {
	rev := make([]byte, len(d))
	for i := range d {
		rev[len(d)-1-i] = d[i]
	}
	x := new(big.Int).SetBytes(rev)
	x.Rsh(x, uint(start))
	x.And(x, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(n)), big.NewInt(1)))
	return x.Uint64()
}

func refBE(d []byte, msb, n int) uint64 {
	x := new(big.Int).SetBytes(d)
	x.Rsh(x, uint(8*len(d)-msb-n))
	x.And(x, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(n)), big.NewInt(1)))
	return x.Uint64()
}

func TestAgainstBigInt(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		size := []int{1, 2, 8, 12, 64}[r.Intn(5)]
		d := make([]byte, size)
		r.Read(d)
		n := 1 + r.Intn(64)
		if n > 8*size {
			n = 8 * size
		}
		start := r.Intn(8*size - n + 1)
		if got, want := ExtractLE(d, start, n), refLE(d, start, n); got != want {
			t.Fatalf("LE % x start %d n %d: %x want %x", d, start, n, got, want)
		}
		if got, want := ExtractBE(d, start, n), refBE(d, start, n); got != want {
			t.Fatalf("BE % x msb %d n %d: %x want %x", d, start, n, got, want)
		}
		v := r.Uint64()
		le := append([]byte(nil), d...)
		InsertLE(le, start, n, v)
		if ExtractLE(le, start, n) != Truncate(v, n) {
			t.Fatalf("InsertLE round trip")
		}
		// Bits outside the field are untouched.
		InsertLE(le, start, n, ExtractLE(d, start, n))
		if string(le) != string(d) {
			t.Fatalf("InsertLE changed other bits")
		}
		be := append([]byte(nil), d...)
		InsertBE(be, start, n, v)
		if ExtractBE(be, start, n) != Truncate(v, n) {
			t.Fatalf("InsertBE round trip % x msb %d n %d", be, start, n)
		}
		InsertBE(be, start, n, ExtractBE(d, start, n))
		if string(be) != string(d) {
			t.Fatalf("InsertBE changed other bits")
		}
	}
}

func TestKnownLayouts(t *testing.T) {
	// A 16-bit Motorola signal with DBC start bit 7 is bytes 0..1 big-endian.
	d := []byte{0x12, 0x34, 0x56}
	if v := ExtractBE(d, MotorolaStart(7), 16); v != 0x1234 {
		t.Fatalf("%x", v)
	}
	// DBC start bit 3, 8 bits Motorola: low nibble of byte 0 then high nibble of byte 1.
	if v := ExtractBE(d, MotorolaStart(3), 8); v != 0x23 {
		t.Fatalf("%x", v)
	}
	if v := ExtractLE(d, 4, 8); v != 0x41 {
		t.Fatalf("%x", v)
	}
	for s := 0; s < 512; s++ {
		if MotorolaDBCStart(MotorolaStart(s)) != s {
			t.Fatal(s)
		}
	}
}

func TestSigned(t *testing.T) {
	if SignExtend(0xF, 4) != -1 || SignExtend(0x7, 4) != 7 || SignExtend(1<<63, 64) != -1<<63 {
		t.Fatal("SignExtend")
	}
	if !FitsSigned(-8, 4) || FitsSigned(-9, 4) || !FitsSigned(7, 4) || FitsSigned(8, 4) || !FitsSigned(-1<<63, 64) {
		t.Fatal("FitsSigned")
	}
	if !FitsUnsigned(15, 4) || FitsUnsigned(16, 4) || !FitsUnsigned(^uint64(0), 64) {
		t.Fatal("FitsUnsigned")
	}
	if Truncate(^uint64(0), 64) != ^uint64(0) || Truncate(0xFF, 4) != 0xF {
		t.Fatal("Truncate")
	}
}

func BenchmarkExtractBE(b *testing.B) {
	d := make([]byte, 8)
	for i := 0; i < b.N; i++ {
		_ = ExtractBE(d, 3, 37)
	}
}
