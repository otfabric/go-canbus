// SPDX-License-Identifier: MIT

package candb

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus/candb/bits"
)

// randomSignal returns a signal that fits in size bytes.
func randomSignal(r *rand.Rand, size int) *Signal {
	s := &Signal{Name: "S", Length: 1 + r.Intn(64), Factor: 1}
	if s.Length > 8*size {
		s.Length = 8 * size
	}
	s.Signed = r.Intn(2) == 0
	pos := r.Intn(8*size - s.Length + 1)
	if r.Intn(2) == 0 {
		s.ByteOrder = BigEndian
		s.StartBit = bits.MotorolaDBCStart(pos)
	} else {
		s.StartBit = pos
	}
	return s
}

// TestRawRoundTrip: for random layouts and values, encoding a raw value
// and decoding it gives the same value, and only the signal's bits change.
func TestRawRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 20000; i++ {
		size := []int{1, 8, 12, 64}[r.Intn(4)]
		s := randomSignal(r, size)
		m := &Message{Name: "M", Length: size, FD: size > 8, Signals: []*Signal{s}}
		raw := bits.Truncate(r.Uint64(), s.Length)
		data, err := m.Encode(map[string]Input{"S": Raw(raw)})
		if err != nil {
			t.Fatalf("%+v: %v", s, err)
		}
		sigs, err := m.Decode(data)
		if err != nil || sigs[0].Raw != raw {
			t.Fatalf("%+v: raw %x decoded %+v (%v)", s, raw, sigs, err)
		}
		if s.Signed && sigs[0].Signed != bits.SignExtend(raw, s.Length) {
			t.Fatalf("sign extension %+v", sigs[0])
		}
		ones := 0
		for _, b := range data {
			for ; b != 0; b &= b - 1 {
				ones++
			}
		}
		want := 0
		for v := raw; v != 0; v &= v - 1 {
			want++
		}
		if ones != want {
			t.Fatalf("%d bits set in the payload, %d in the value", ones, want)
		}
		if s.Signed {
			if _, err := s.RawFromInt(sigs[0].Signed); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPhysicalRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	for i := 0; i < 5000; i++ {
		s := randomSignal(r, 8)
		if s.Length > 48 {
			// Physical values are float64: raw × factor must stay well
			// within its 53-bit mantissa. Raw values have their own exact API.
			s.Length = 48
		}
		s.Factor = []float64{1, 0.1, 0.125, 3, 1e-5}[r.Intn(5)]
		s.Offset = []float64{0, -40, 1000.5}[r.Intn(3)]
		raw := bits.Truncate(r.Uint64(), s.Length)
		v := s.Value(raw)
		back, err := s.RawFromPhysical(v.Physical)
		if err != nil || back != raw {
			t.Fatalf("%+v raw %d phys %v back %d %v", s, raw, v.Physical, back, err)
		}
	}
}

func TestFloatsAndErrors(t *testing.T) {
	f32 := &Signal{Name: "F", Length: 32, ValueType: Float32, Factor: 1}
	raw, _ := f32.RawFromPhysical(1.25)
	if f32.Value(raw).Physical != 1.25 {
		t.Fatal("float32")
	}
	if raw, _ := f32.RawFromPhysical(math.NaN()); !math.IsNaN(f32.Value(raw).Physical) {
		t.Fatal("NaN")
	}
	if _, err := f32.RawFromInt(1); !errors.Is(err, ErrRange) {
		t.Fatal(err)
	}
	if _, err := (&Signal{Length: 8, Factor: 1}).RawFromPhysical(math.Inf(1)); !errors.Is(err, ErrRange) {
		t.Fatal(err)
	}
	if _, err := (&Signal{Length: 64, Signed: true, Factor: 1}).RawFromPhysical(1e30); !errors.Is(err, ErrRange) {
		t.Fatal(err)
	}
	if _, err := (&Signal{Length: 8, Factor: 1}).RawFromPhysical(-1); !errors.Is(err, ErrRange) {
		t.Fatal(err)
	}
	zero := &Signal{Length: 8}
	if r, _ := zero.RawFromPhysical(5); r != 5 {
		t.Fatal("factor 0 treated as 1")
	}
	bad := &Signal{Name: "B", Length: 0}
	if _, err := bad.Extract([]byte{0}); !errors.Is(err, ErrLayout) {
		t.Fatal(err)
	}
	if err := bad.Insert([]byte{0}, 0); !errors.Is(err, ErrLayout) {
		t.Fatal(err)
	}
	if bad.Span() != math.MaxInt32 {
		t.Fatal("Span")
	}
	if _, err := (&Signal{Length: 8, StartBit: -1}).layout(); !errors.Is(err, ErrLayout) {
		t.Fatal(err)
	}
	s := &Signal{Name: "S", Length: 16, Factor: 1}
	if err := s.Insert([]byte{0}, 1); !errors.Is(err, ErrShortPayload) {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	db := &Database{Messages: []*Message{
		{Name: "A", ID: 0x800, Length: 9, BRS: true, Signals: []*Signal{
			{Name: "X", Length: 8, Factor: 0},
			{Name: "X", Length: 70},
			{Name: "F", Length: 16, ValueType: Float32, Factor: 1},
			{Name: "Y", Length: 8, Factor: 1, Multiplexer: "Q"},
			{Name: "Z", Length: 8, Factor: 1, Multiplexer: "X", MuxValues: []MuxRange{{1, 1}}},
		}},
		{Name: "A", ID: 0x800, Length: 8},
		{Name: "C", ID: 1, Length: 13, FD: true},
		{Name: "D", ID: 2, Length: 8, Signals: []*Signal{
			{Name: "Sw", Length: 4, Factor: 1, IsMultiplexer: true},
			{Name: "P", StartBit: 8, Length: 8, Factor: 1, Multiplexer: "Sw", MuxValues: []MuxRange{{1, 1}}},
			{Name: "Q", StartBit: 8, Length: 8, Factor: 1, Multiplexer: "Sw", MuxValues: []MuxRange{{2, 2}}}, // exclusive: no overlap
			{Name: "R", StartBit: 12, Length: 8, Factor: 1},                                                  // overlaps both
			{Name: "Self", StartBit: 32, Length: 4, Factor: 1, IsMultiplexer: true, Multiplexer: "Self", MuxValues: []MuxRange{{0, 0}}},
		}},
	}}
	var all []string
	ds := db.Validate()
	for _, d := range ds {
		all = append(all, d.String())
	}
	text := strings.Join(all, "\n")
	for _, want := range []string{
		"error: message A: duplicate message name", "identifier 0x800 also used by A", "identifier 0x800 out of range",
		"length 9 is invalid for a classical frame", "bit rate switching on a classical frame", "length 13 is not a CAN FD length",
		"signal A.X: duplicate signal name", "signal A.X: candb: invalid signal layout: X has 70 bits", "float32 signal of 16 bits",
		"factor 0", "multiplexer \"Q\" does not exist", "multiplexed signal without multiplexer values",
		"multiplexer \"X\" is not a multiplexer switch", "signal D.R: overlaps signal P", "signal D.R: overlaps signal Q", "multiplexed by itself",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "D.Q: overlaps signal P") {
		t.Error("exclusive multiplexed signals reported as overlapping")
	}
	if !HasErrors(ds) || HasErrors(nil) {
		t.Fatal("HasErrors")
	}
	if (Diagnostic{Severity: Info, Line: 3, Message: "x"}).String() != "line 3: info: x" {
		t.Fatal("String")
	}
	// The cycle is also reported by the codec.
	if _, err := db.Messages[3].Decode(make([]byte, 8)); !errors.Is(err, ErrLayout) {
		t.Fatal(err)
	}
}

func TestJSONAndNames(t *testing.T) {
	ini := int64(-3)
	db := &Database{Messages: []*Message{{Name: "M", ID: 1, Length: 8, Signals: []*Signal{
		{Name: "S", Length: 8, ByteOrder: BigEndian, ValueType: Float64, Initial: &ini, Values: []ValueDescription{{1, "one"}}},
	}}}}
	b, err := json.Marshal(db)
	if err != nil {
		t.Fatal(err)
	}
	var back Database
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	s := back.Messages[0].Signals[0]
	if s.ByteOrder != BigEndian || s.ValueType != Float64 || *s.Initial != -3 || !strings.Contains(string(b), `"byteOrder":"big_endian"`) {
		t.Fatal(string(b))
	}
	var bo ByteOrder
	var vt ValueType
	if bo.UnmarshalText([]byte("x")) == nil || vt.UnmarshalText([]byte("x")) == nil {
		t.Fatal("bad names accepted")
	}
	if Severity(9).String() != "error" || LittleEndian.String() != "little_endian" || Float32.String() != "float32" {
		t.Fatal("names")
	}
	if b, _ := Warning.MarshalText(); string(b) != "warning" {
		t.Fatal(string(b))
	}
	m := back.Messages[0]
	if back.Message(1, false) != m || back.Message(1, true) != nil || back.MessageByName("x") != nil || back.Node("n") != nil ||
		back.AttributeDefinition("a", ObjectNode) != nil || m.Signal("x") != nil || m.Multiplexed() {
		t.Fatal("lookups")
	}
	if v, ok := s.LabelValue("one"); !ok || v != 1 {
		t.Fatal("LabelValue")
	}
	if _, ok := s.LabelValue("two"); ok || s.Label(5) != "" {
		t.Fatal("unknown label")
	}
	if s.RawString(Value{Raw: 5}) != "5" {
		t.Fatal("RawString")
	}
}
