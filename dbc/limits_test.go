// SPDX-License-Identifier: MIT

package dbc

import (
	"bytes"
	"errors"
	"math"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus/candb"
)

// allocated returns the bytes allocated while f runs.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// endless is a reader that never ends: every read is filled with its byte.
type endless byte

func (e endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(e)
	}
	return len(p), nil
}

func refusedAs(err error, text string) bool {
	var pe *ParseError
	return errors.As(err, &pe) && strings.Contains(err.Error(), text)
}

// TestSourceSizeBound: source up to the bound is parsed, source of one
// byte more is a *ParseError, from memory and from a reader; a reader is
// read up to the bound and one byte, however much it holds.
func TestSourceSizeBound(t *testing.T) {
	src := []byte("BU_: A\nBO_ 1 M: 8 A\n")
	n := int64(len(src))
	for _, c := range []struct {
		limit int64
		ok    bool
	}{{n + 1, true}, {n, true}, {n - 1, false}, {1, false}, {0, false}} {
		db, _, err := parseBytes(src, c.limit)
		if c.ok != (err == nil) || c.ok != (len(db.Messages) == 1) || !c.ok && !refusedAs(err, "source larger than "+strconv.FormatInt(c.limit, 10)+" bytes") {
			t.Errorf("parseBytes, bound %d: %d messages, %v", c.limit, len(db.Messages), err)
		}
		db, _, err = parse(bytes.NewReader(src), c.limit)
		if c.ok != (err == nil) || c.ok != (len(db.Messages) == 1) || !c.ok && !refusedAs(err, "source larger than") {
			t.Errorf("parse, bound %d: %d messages, %v", c.limit, len(db.Messages), err)
		}
	}
	if _, _, err := parseBytes(nil, 0); err != nil {
		t.Errorf("empty source, bound 0: %v", err)
	}
	// A reader without end costs the bound and its copy, not more.
	const limit = 1 << 20
	if got := allocated(func() {
		if _, _, err := parse(endless('\n'), limit); !refusedAs(err, "source larger than 1048576 bytes") {
			t.Errorf("reader without end: %v", err)
		}
	}); got > 8*limit {
		t.Errorf("reader without end, bound %d: %d bytes allocated", limit, got)
	}
}

// TestSourceSizeBoundOfPackage: the package's bound is 64 MiB. Source of
// one byte more, valid DBC or not, is a *ParseError and costs no memory.
func TestSourceSizeBoundOfPackage(t *testing.T) {
	if maxInputSize != 64<<20 {
		t.Fatalf("bound %d: SECURITY.md and the package documentation say 64 MiB", maxInputSize)
	}
	src := bytes.Repeat([]byte{'\n'}, 64<<20+1)
	if got := allocated(func() {
		db, diags, err := ParseBytes(src)
		if !refusedAs(err, "source larger than 67108864 bytes") || len(diags) != 1 || db == nil {
			t.Errorf("64 MiB + 1: %v, %v", diags, err)
		}
	}); got > 1<<20 {
		t.Errorf("64 MiB + 1: %d bytes allocated", got)
	}
}

// TestTokenMemory: parsing keeps the source, not its tokens. Source made
// of the shortest tokens allocates a small multiple of its size.
func TestTokenMemory(t *testing.T) {
	for name, unit := range map[string]string{"numbers": "0 ", "punctuation": ";", "identifiers": "a\n"} {
		src := []byte(strings.Repeat(unit, (2<<20)/len(unit)))
		if got := allocated(func() { _, _, _ = ParseBytes(src) }); got > 4*uint64(len(src)) {
			t.Errorf("%s: %d bytes of source, %d bytes allocated (%.1f times)", name, len(src), got, float64(got)/float64(len(src)))
		}
	}
}

// signalSource returns DBC source of n one-bit signals without overlaps,
// 512 to a message, and a last message that has none.
func signalSource(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i%512 == 0 {
			b.WriteString("BO_ " + strconv.Itoa(i/512+1) + " M" + strconv.Itoa(i/512) + ": 64 A\n")
		}
		b.WriteString(" SG_ S" + strconv.Itoa(i%512) + " : " + strconv.Itoa(i%512) + "|1@1+ (1,0) [0|0] \"\" A\n")
	}
	b.WriteString("BO_ 2000 Last: 8 A\n")
	return b.String()
}

// TestSignalBound: a database holds at most maxSignals signals. The file
// that has one more is a *ParseError with the signals read so far, and
// the statements after it are not read.
func TestSignalBound(t *testing.T) {
	if testing.Short() {
		t.Skip("large inputs")
	}
	count := func(db *candb.Database) (n int) {
		for _, m := range db.Messages {
			n += len(m.Signals)
		}
		return n
	}
	db, diags, err := ParseBytes([]byte(signalSource(maxSignals)))
	if err != nil || count(db) != maxSignals || db.MessageByName("Last") == nil {
		t.Fatalf("at the bound: %d signals, %d diagnostics, %v", count(db), len(diags), err)
	}
	db, diags, err = ParseBytes([]byte(signalSource(maxSignals + 1)))
	if !refusedAs(err, "more than 262144 signals in the database: the rest of the file is not read") {
		t.Fatalf("one above the bound: %v", err)
	}
	if count(db) != maxSignals || db.MessageByName("Last") != nil || len(diags) != 1 {
		t.Fatalf("one above the bound: %d signals, %d diagnostics, last message %v", count(db), len(diags), db.MessageByName("Last"))
	}
}

// TestSharedBitSignals: the overlap check of a message whose signals all
// share a bit ends at the bound of candb.Database.Validate, with a
// warning; the file still parses.
func TestSharedBitSignals(t *testing.T) {
	const n = 6000 // n(n-1)/2 pairs: more than the 2^24 steps of the check
	var b strings.Builder
	b.WriteString("BO_ 1 M: 8 A\n")
	for i := range n {
		b.WriteString(" SG_ S" + strconv.Itoa(i) + " : 0|1@1+ (1,0) [0|0] \"\" A\n")
	}
	db, diags, err := ParseBytes([]byte(b.String()))
	if err != nil || len(db.Messages[0].Signals) != n {
		t.Fatalf("%d signals, %v", len(db.Messages[0].Signals), err)
	}
	last := diags[len(diags)-1]
	if len(diags) != 66 || !strings.HasPrefix(last.Message, "overlap check ended after 16777216 steps") {
		t.Fatalf("%d diagnostics, last %v", len(diags), last)
	}
}

// TestAttributeValueRange: an INT, HEX or ENUM attribute value is the
// integer the file states, or it does not match the attribute; a FLOAT
// GenSigStartValue is an initial value when it rounds to an integer of 64
// bits, or the signal has none. No value is converted to another one, on
// any platform.
func TestAttributeValueRange(t *testing.T) {
	head := "BO_ 1 M: 8 A\n SG_ S : 0|64@1- (1,0) [0|0] \"\" A\n"
	type result struct {
		v  int64
		ok bool
	}
	no := result{}
	is := func(v int64) result { return result{v, true} }
	for _, c := range []struct {
		typ, value string
		want       result
	}{
		{"INT", "0", is(0)}, {"INT", "-1", is(-1)}, {"INT", "5.0", is(5)}, {"INT", "1e3", is(1000)}, {"INT", "-2.0", is(-2)},
		{"INT", "9223372036854775807", is(math.MaxInt64)}, {"INT", "-9223372036854775808", is(math.MinInt64)},
		{"INT", "9223372036854775808", no}, {"INT", "-9223372036854775809", no}, {"INT", "18446744073709551616", no},
		{"INT", "1e18", is(1e18)}, {"INT", "-1e18", is(-1e18)}, {"INT", "9.3e18", no}, {"INT", "-9.3e18", no},
		{"INT", "1e19", no}, {"INT", "1e300", no}, {"INT", "1e999", no}, {"INT", "2.5", no},
		{"INT", `"inf"`, no}, {"INT", `"-Inf"`, no}, {"INT", `"NaN"`, no}, {"INT", `"infinity"`, no},
		{"HEX", "0x7FFFFFFFFFFFFFFF", is(math.MaxInt64)}, {"HEX", "0x8000000000000000", no}, {"HEX", "1e19", no}, {"HEX", `"inf"`, no},
		{"ENUM \"a\",\"b\"", "1", is(1)}, {"ENUM \"a\",\"b\"", "9223372036854775808", no}, {"ENUM \"a\",\"b\"", "-9223372036854775809", no},
	} {
		src := head + "BA_DEF_ BO_ \"X\" " + c.typ + func() string {
			if strings.HasPrefix(c.typ, "ENUM") {
				return ""
			}
			return " 0 0"
		}() + ";\nBA_ \"X\" BO_ 1 " + c.value + ";\n"
		db, diags, err := ParseBytes([]byte(src))
		m := db.MessageByName("M")
		switch {
		case c.want.ok && (err != nil || len(m.Attributes) != 1 || m.Attributes[0].Int != c.want.v):
			t.Errorf("%s %s: attributes %+v, %v", c.typ, c.value, m.Attributes, err)
		case !c.want.ok && (!refusedAs(err, "does not match") || len(m.Attributes) != 0):
			t.Errorf("%s %s: attributes %+v, diagnostics %v, %v", c.typ, c.value, m.Attributes, diags, err)
		}
		// The same value as the attribute's default.
		if strings.HasPrefix(c.value, "\"") {
			continue // a string default is an enum label or a string
		}
		src = head + "BA_DEF_ BO_ \"X\" " + c.typ + ";\nBA_DEF_DEF_ \"X\" " + c.value + ";\n"
		db, diags, err = ParseBytes([]byte(src))
		def := db.AttributeDefinitions[0].Default
		switch {
		case err != nil:
			t.Errorf("default %s %s: %v", c.typ, c.value, err)
		case c.want.ok && (def == nil || def.Int != c.want.v):
			t.Errorf("default %s %s: %+v", c.typ, c.value, def)
		case !c.want.ok && (def != nil || len(diags) != 1 || !strings.Contains(diags[0].Message, "does not match its type")):
			t.Errorf("default %s %s: %+v, %v", c.typ, c.value, def, diags)
		}
	}
	// GenSigStartValue of a FLOAT attribute. 2^63-1024 is the largest
	// float64 below 2^63, -2^63-2048 the largest below -2^63.
	for _, c := range []struct {
		value string
		want  result
	}{
		{"0", is(0)}, {"5", is(5)}, {"2.5", is(3)}, {"-2.5", is(-3)}, {"0.4", is(0)},
		{"9223372036854774784", is(math.MaxInt64 - 1023)}, {"9223372036854775807", no}, {"9223372036854775808", no},
		{"-9223372036854775808", is(math.MinInt64)}, {"-9223372036854777856", no},
		{"1.8446744073709552e19", no}, {"1e300", no}, {"-1e300", no},
		{`"NaN"`, no}, {`"inf"`, no}, {`"-inf"`, no}, {`"Infinity"`, no},
	} {
		defs := "BA_DEF_ SG_ \"GenSigStartValue\" FLOAT 0 0;\n"
		for name, src := range map[string]string{
			"value":   head + defs + "BA_ \"GenSigStartValue\" SG_ 1 S " + c.value + ";\n",
			"default": head + defs + "BA_DEF_DEF_ \"GenSigStartValue\" " + strings.Trim(c.value, "\"") + ";\n",
		} {
			db, diags, err := ParseBytes([]byte(src))
			if err != nil {
				t.Errorf("%s %s: %v", name, c.value, err)
				continue
			}
			s := db.MessageByName("M").Signal("S")
			switch {
			case c.want.ok && (s.Initial == nil || *s.Initial != c.want.v || len(diags) != 0):
				t.Errorf("%s %s: initial %v, %v; want %d", name, c.value, s.Initial, diags, c.want.v)
			case !c.want.ok && (s.Initial != nil || len(diags) != 1 || diags[0].Severity != candb.Warning || !strings.Contains(diags[0].Message, "no initial value")):
				t.Errorf("%s %s: initial %v, %v; want none", name, c.value, s.Initial, diags)
			}
			// A database with no initial value encodes 0 for the signal.
			if !c.want.ok {
				if data, err := db.MessageByName("M").EncodeWith(nil, candb.EncodeOptions{FillMissing: true}); err != nil || !bytes.Equal(data, make([]byte, 8)) {
					t.Errorf("%s %s: encoded % x, %v", name, c.value, data, err)
				}
			}
		}
	}
}
