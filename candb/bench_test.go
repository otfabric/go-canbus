// SPDX-License-Identifier: MIT

package candb

import "testing"

var benchSink uint64

// BenchmarkSignalCodec decodes and re-inserts an Intel and a Motorola
// signal: the hot path of Message.Decode and Encode.
func BenchmarkSignalCodec(b *testing.B) {
	le := &Signal{Name: "LE", StartBit: 3, Length: 37, Signed: true, Factor: 1}
	be := &Signal{Name: "BE", StartBit: 7, Length: 64, ByteOrder: BigEndian, Factor: 1}
	f := &Signal{Name: "F", StartBit: 0, Length: 64, ValueType: Float64, Factor: 1}
	d := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xF0, 0x0D}
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range []*Signal{le, be, f} {
			v, err := s.Decode(d)
			if err != nil {
				b.Fatal(err)
			}
			if err := s.Insert(d, v.Raw); err != nil {
				b.Fatal(err)
			}
			benchSink += uint64(v.Signed) ^ v.Raw
		}
	}
}
