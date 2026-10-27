// SPDX-License-Identifier: MIT

package codegen

import (
	"bytes"
	"errors"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/candb"
	"github.com/otfabric/go-canbus/candb/bits"
	"github.com/otfabric/go-canbus/codegen/internal/golden/full"
	"github.com/otfabric/go-canbus/dbc"
)

func loadFull(t testing.TB) *candb.Database {
	db, _, err := dbc.ParseFile("../dbc/testdata/full.dbc")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// fieldBits returns the bit pattern a generated field holds.
func fieldBits(v reflect.Value, s *candb.Signal) uint64 {
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			return 1
		}
		return 0
	case reflect.Float32:
		// Through Interface, not Float: a float32→float64 conversion
		// would quiet signalling NaNs.
		return uint64(math.Float32bits(v.Interface().(float32)))
	case reflect.Float64:
		return math.Float64bits(v.Float())
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return bits.Truncate(uint64(v.Int()), s.Length)
	}
	return v.Uint()
}

// TestDifferential decodes and encodes random payloads with the generated
// code and with the dynamic codec and requires identical results.
func TestDifferential(t *testing.T) {
	db := loadFull(t)
	r := rand.New(rand.NewSource(3))
	for _, m := range db.Messages {
		for i := 0; i < 2000; i++ {
			data := make([]byte, 64)
			r.Read(data)
			gm := full.New(m.ID, m.Extended)
			if gm == nil || gm.MessageName() != m.Name || gm.MessageID() != m.ID {
				t.Fatalf("New(%s)", m.Name)
			}
			if err := gm.Decode(data); err != nil {
				t.Fatal(err)
			}
			sigs, err := m.Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			rv := reflect.ValueOf(gm).Elem()
			in := map[string]candb.Input{}
			active := map[string]bool{}
			for _, s := range sigs {
				active[s.Signal.Name] = true
				in[s.Signal.Name] = candb.Raw(s.Raw)
				got := fieldBits(rv.FieldByName(ident(s.Signal.Name)), s.Signal)
				if got != s.Raw {
					t.Fatalf("%s.%s: generated %x, dynamic %x", m.Name, s.Signal.Name, got, s.Raw)
				}
				phys := reflect.ValueOf(gm).MethodByName(ident(s.Signal.Name) + "Phys").Call(nil)[0].Float()
				if phys != s.Physical && !(math.IsNaN(phys) && math.IsNaN(s.Physical)) {
					t.Fatalf("%s.%s: physical %v vs %v", m.Name, s.Signal.Name, phys, s.Physical)
				}
			}
			for _, s := range m.Signals {
				if !active[s.Name] && !rv.FieldByName(ident(s.Name)).IsZero() {
					t.Fatalf("%s.%s: inactive signal not zero", m.Name, s.Name)
				}
			}
			genBytes, err := gm.Encode()
			if err != nil {
				t.Fatal(err)
			}
			dynBytes, err := m.Encode(in)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(genBytes, dynBytes) {
				t.Fatalf("%s: generated % x, dynamic % x", m.Name, genBytes, dynBytes)
			}
			f, err := gm.Frame()
			if err != nil || f.ID != m.ID || f.IsExtended() != m.Extended || f.IsFD() != m.FD {
				t.Fatal(f, err)
			}
			back, err := full.Decode(&f)
			if err != nil {
				t.Fatal(err)
			}
			again, _ := back.Encode() // compare encodings: NaN fields are not DeepEqual
			if !bytes.Equal(again, genBytes) {
				t.Fatalf("%s: Decode(Frame()) re-encodes to % x, want % x", m.Name, again, genBytes)
			}
		}
	}
}

func TestGeneratedAPI(t *testing.T) {
	var e full.Engine
	if err := e.SetSpeedPhys(582.5); err != nil || e.Speed != 4660 {
		t.Fatal(e.Speed, err)
	}
	if err := e.SetTempPhys(-50); !errors.Is(err, full.ErrRange) {
		t.Fatal("below the declared minimum", err)
	}
	if err := e.SetTempPhys(20); err != nil || e.Temp != 60 || e.TempPhys() != 20 {
		t.Fatal(e.Temp, err)
	}
	e.Mode = full.EngineModeRun
	if e.Mode.String() != "Run" || full.EngineMode(3).String() != "3" {
		t.Fatal(e.Mode.String())
	}
	e.Throttle = 0x1000 // 13 bits in a 12-bit signal
	if _, err := e.Encode(); !errors.Is(err, full.ErrRange) {
		t.Fatal(err)
	}
	if err := e.Decode(make([]byte, 7)); !errors.Is(err, full.ErrLength) {
		t.Fatal(err)
	}
	if err := e.SetFlagPhys(2); !errors.Is(err, full.ErrRange) {
		t.Fatal(err)
	}
	var w full.Wide
	if err := w.SetU64Phys(1e30); !errors.Is(err, full.ErrRange) {
		t.Fatal(err)
	}
	var s full.SignedBE
	if err := s.SetS64Phys(-1e30); !errors.Is(err, full.ErrRange) {
		t.Fatal(err)
	}
	var fl full.Floats
	if err := fl.SetF64Phys(7); err != nil || fl.F64 != 3 {
		t.Fatal(fl.F64, err)
	}
	f, _ := canbus.NewStandard(0x7FF, nil)
	if _, err := full.Decode(&f); !errors.Is(err, full.ErrUnknownMessage) {
		t.Fatal(err)
	}
	short := canbus.Frame{ID: full.EngineID, Len: 1}
	if _, err := full.Decode(&short); !errors.Is(err, full.ErrLength) {
		t.Fatal(err)
	}
}

func TestGenerateErrors(t *testing.T) {
	db := loadFull(t)
	if _, err := Generate(db, Options{Package: "1x"}); err == nil {
		t.Fatal("bad package name")
	}
	bad := &candb.Database{Messages: []*candb.Message{{Name: "M", Length: 9}}}
	if _, err := Generate(bad, Options{Package: "p"}); err == nil {
		t.Fatal("invalid database")
	}
	// Deterministic.
	a, _ := Generate(db, Options{Package: "p"})
	b, _ := Generate(db, Options{Package: "p"})
	if !bytes.Equal(a, b) || !strings.Contains(string(a), "from a CAN database. DO NOT EDIT.") {
		t.Fatal("non-deterministic or wrong header")
	}
}

func TestIdentifiers(t *testing.T) {
	for in, want := range map[string]string{"engine_speed": "Engine_speed", "1st": "X1st", "a-b c": "AB_C"[:0] + "ABC", "": "X", "_x": "X_X", "été": "T"} {
		if got := ident(in); got != want {
			t.Errorf("ident(%q) = %q, want %q", in, got, want)
		}
	}
	// Collisions with methods, other signals and Phys accessors.
	db := &candb.Database{Messages: []*candb.Message{
		{Name: "Encode", ID: 1, Length: 8, Signals: []*candb.Signal{
			{Name: "Encode", Length: 8, Factor: 1},
			{Name: "speed", StartBit: 8, Length: 8, Factor: 1},
			{Name: "Speed", StartBit: 16, Length: 8, Factor: 1},
			{Name: "SpeedPhys", StartBit: 24, Length: 8, Factor: 1},
			{Name: "E", StartBit: 32, Length: 4, Factor: 1, Values: []candb.ValueDescription{{1, "a b"}, {2, "a_b"}, {3, "a b"}, {-1, "neg"}}},
		}},
		{Name: "encode", ID: 2, Length: 8},
	}}
	if _, err := Generate(db, Options{Package: "p"}); err != nil {
		t.Fatal(err)
	}
	js, err := GenerateJSON(db, Options{Source: "x.dbc"})
	if err != nil || !strings.Contains(string(js), `"source": "x.dbc"`) {
		t.Fatal(string(js), err)
	}
}
