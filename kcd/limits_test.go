// SPDX-License-Identifier: MIT

package kcd

import (
	"errors"
	"io"
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

// repeated is a reader that never ends: its text, over and over.
type repeated struct {
	text string
	at   int
}

func (r *repeated) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.text[r.at]
		r.at = (r.at + 1) % len(r.text)
	}
	return len(p), nil
}

func refusedAs(err error, text string) bool {
	var pe *ParseError
	return errors.As(err, &pe) && strings.Contains(err.Error(), text)
}

// TestDocumentSizeBound: a document up to the bound is parsed, a document
// of one byte more is a *ParseError; a reader is read up to the bound and
// one byte, however much it holds.
func TestDocumentSizeBound(t *testing.T) {
	if maxInputSize != 64<<20 {
		t.Fatalf("bound %d: SECURITY.md and the package documentation say 64 MiB", maxInputSize)
	}
	const doc = `<NetworkDefinition><Bus><Message id="1" name="M" length="8"/></Bus></NetworkDefinition>`
	n := int64(len(doc))
	for _, c := range []struct {
		limit int64
		ok    bool
	}{{n + 1, true}, {n, true}, {n - 1, false}, {1, false}, {0, false}} {
		db, _, err := parse(strings.NewReader(doc), c.limit)
		if c.ok != (err == nil) || c.ok != (len(db.Messages) == 1) || !c.ok && !refusedAs(err, "document larger than "+strconv.FormatInt(c.limit, 10)+" bytes") {
			t.Errorf("bound %d: %d messages, %v", c.limit, len(db.Messages), err)
		}
	}
	// An empty reader is not a document, whatever the bound.
	if _, _, err := parse(strings.NewReader(""), 0); !refusedAs(err, "XML: EOF") {
		t.Errorf("empty reader, bound 0: %v", err)
	}
	// A reader that fails after the bound's bytes reports its own error.
	const open = "<NetworkDefinition>"
	broken := io.MultiReader(strings.NewReader(open), iotestErr{})
	if _, _, err := parse(broken, int64(len(open))); !refusedAs(err, "XML: broken") {
		t.Errorf("failing reader: %v", err)
	}
	// A reader without end costs what a document of the bound costs.
	const limit = 1 << 16
	endless := io.MultiReader(strings.NewReader("<NetworkDefinition>"), &repeated{text: "<Node/>"})
	if got := allocated(func() {
		if _, _, err := parse(endless, limit); !refusedAs(err, "document larger than 65536 bytes") {
			t.Errorf("reader without end: %v", err)
		}
	}); got > 512*limit {
		t.Errorf("reader without end, bound %d: %d bytes allocated", limit, got)
	}
}

type iotestErr struct{}

func (iotestErr) Read([]byte) (int, error) { return 0, errors.New("broken") }

// TestMessageLengthBound: a message length no frame carries is a
// *ParseError: more than 64 bytes, or a negative length.
func TestMessageLengthBound(t *testing.T) {
	for _, c := range []struct {
		length string
		ok     bool
	}{
		{"0", true}, {"8", true}, {"9", true}, {"63", true}, {"64", true},
		{"65", false}, {"70", false}, {"2147483647", false}, {"2147483648", false}, {"9223372036854775807", false},
		{"-1", false}, {"-2147483648", false},
	} {
		doc := `<NetworkDefinition><Bus><Message id="1" name="M" length="` + c.length + `"/></Bus></NetworkDefinition>`
		db, diags, err := Parse(strings.NewReader(doc))
		var pe *ParseError
		if c.ok != (err == nil) || !c.ok && !errors.As(err, &pe) {
			t.Errorf("length %s: %v, %v", c.length, diags, err)
		}
		if c.ok {
			if _, err := db.Messages[0].Decode(make([]byte, 64)); err != nil {
				t.Errorf("length %s: %v", c.length, err)
			}
		}
	}
}

// signalDocument returns a KCD document of n one-bit signals without
// overlaps, 512 to a message, and a last message that has none.
func signalDocument(n int) string {
	var b strings.Builder
	b.WriteString("<NetworkDefinition><Bus>")
	for i := 0; i < n; i++ {
		if i%512 == 0 {
			if i > 0 {
				b.WriteString("</Message>")
			}
			b.WriteString(`<Message id="` + strconv.Itoa(i/512+1) + `" name="M` + strconv.Itoa(i/512) + `" length="64">`)
		}
		b.WriteString(`<Signal name="S` + strconv.Itoa(i%512) + `" offset="` + strconv.Itoa(i%512) + `"/>`)
	}
	b.WriteString(`</Message><Message id="2000" name="Last" length="8"/></Bus></NetworkDefinition>`)
	return b.String()
}

// TestSignalBound: a database holds at most maxSignals signals. The
// document that has one more is a *ParseError with the messages read so
// far; the message that reaches the bound and those after it are not
// read.
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
	db, diags, err := Parse(strings.NewReader(signalDocument(maxSignals)))
	if err != nil || count(db) != maxSignals || db.MessageByName("Last") == nil {
		t.Fatalf("at the bound: %d signals, %d diagnostics, %v", count(db), len(diags), err)
	}
	db, diags, err = Parse(strings.NewReader(signalDocument(maxSignals + 1)))
	if !refusedAs(err, "more than 262144 signals in the database: the rest of the file is not read") {
		t.Fatalf("one above the bound: %v", err)
	}
	if count(db) != maxSignals || db.MessageByName("Last") != nil || len(diags) != 1 {
		t.Fatalf("one above the bound: %d signals, %d diagnostics, last message %v", count(db), len(diags), db.MessageByName("Last"))
	}
}

// TestSharedBitSignals: the overlap check of a message whose signals all
// share a bit ends at the bound of candb.Database.Validate, with a
// warning; the document still parses.
func TestSharedBitSignals(t *testing.T) {
	const n = 6000 // n(n-1)/2 pairs: more than the 2^24 steps of the check
	var b strings.Builder
	b.WriteString(`<NetworkDefinition><Bus><Message id="1" name="M" length="8">`)
	for i := range n {
		b.WriteString(`<Signal name="S` + strconv.Itoa(i) + `" offset="0"/>`)
	}
	b.WriteString(`</Message></Bus></NetworkDefinition>`)
	db, diags, err := Parse(strings.NewReader(b.String()))
	if err != nil || len(db.Messages[0].Signals) != n {
		t.Fatalf("%d signals, %v", len(db.Messages[0].Signals), err)
	}
	last := diags[len(diags)-1]
	if len(diags) != 66 || !strings.HasPrefix(last.Message, "overlap check ended after 16777216 steps") {
		t.Fatalf("%d diagnostics, last %v", len(diags), last)
	}
}
