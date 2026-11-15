// SPDX-License-Identifier: MIT

package candb

import "testing"

var benchSink uint64

// BenchmarkSignalCodec decodes and re-inserts an Intel, a Motorola and a
// float signal with Signal.Decode and Signal.Insert: the per-signal work,
// without what Message.Decode and Message.Encode add around it (see
// BenchmarkMessageCodec).
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

// BenchmarkMessageCodec decodes a message of eight signals, one of them
// multiplexed, with Message.Decode and encodes the result with
// Message.EncodeWith: the calls an application makes per frame. Both
// build their maps of signals on every call, so they allocate.
func BenchmarkMessageCodec(b *testing.B) {
	m := &Message{Name: "M", ID: 1, Length: 8, Signals: []*Signal{
		{Name: "Sw", Length: 4, Factor: 1, IsMultiplexer: true},
		{Name: "A", StartBit: 4, Length: 4, Factor: 1},
		{Name: "B", StartBit: 8, Length: 8, Factor: 0.5, Offset: -10},
		{Name: "C", StartBit: 16, Length: 12, Signed: true, Factor: 1},
		{Name: "D", StartBit: 39, Length: 16, ByteOrder: BigEndian, Factor: 1},
		{Name: "E", StartBit: 48, Length: 8, Factor: 1, Multiplexer: "Sw", MuxValues: []MuxRange{{1, 1}}},
		{Name: "F", StartBit: 48, Length: 8, Factor: 1, Multiplexer: "Sw", MuxValues: []MuxRange{{2, 2}}},
		{Name: "G", StartBit: 56, Length: 8, Factor: 1},
	}}
	d := []byte{0x21, 0xAD, 0xBE, 0x0F, 0xCA, 0xFE, 0xF0, 0x0D}
	values := make(map[string]Input, len(m.Signals))
	b.ReportAllocs()
	for b.Loop() {
		sigs, err := m.Decode(d)
		if err != nil {
			b.Fatal(err)
		}
		clear(values)
		for _, s := range sigs {
			values[s.Signal.Name] = Raw(s.Raw)
			benchSink += s.Raw
		}
		out, err := m.EncodeWith(values, EncodeOptions{Lax: true})
		if err != nil {
			b.Fatal(err)
		}
		benchSink += uint64(out[0])
	}
}
