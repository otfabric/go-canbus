// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// readBLFErr reads every frame of a BLF file and returns the first error
// other than io.EOF.
func readBLFErr(t *testing.T, b []byte) error {
	t.Helper()
	r, err := NewBLFReader(bytes.NewReader(b))
	if err != nil {
		return err
	}
	var f canbus.Frame
	for {
		if err := r.Read(&f); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// blfRawObject is a top-level object of the given type and body.
func blfRawObject(typ uint32, body []byte) []byte {
	le := binary.LittleEndian
	o := make([]byte, blfObjBase)
	copy(o, blfObjSig)
	le.PutUint16(o[4:], blfObjBase)
	le.PutUint16(o[6:], 1)
	le.PutUint32(o[8:], uint32(blfObjBase+len(body)))
	le.PutUint32(o[12:], typ)
	return append(o, body...)
}

// TestBLFMalformedContainers: every way a container can be malformed is a
// ParseError naming the problem, never a panic or a silent stop.
func TestBLFMalformedContainers(t *testing.T) {
	pinLocalUTC(t)
	hdr := blfHeader(t)
	badObj := append([]byte("XOBJ"), make([]byte, 12)...) // object header without its signature
	zlibBody := func(data []byte, usize uint32) []byte {
		b := make([]byte, 16)
		binary.LittleEndian.PutUint16(b[0:], blfCompressZlib)
		binary.LittleEndian.PutUint32(b[8:], usize)
		return append(b, data...)
	}
	var z bytes.Buffer
	{
		c := blfContainer([]byte("0123456789"), true, 10)
		z.Write(c[32 : 32+int(binary.LittleEndian.Uint32(c[8:]))-32])
	}
	cases := []struct {
		name, want string
		file       []byte
	}{
		{"object header in container", "", append(hdr, blfContainer(badObj, false, 16)...)},
		{"short container", "short container", append(hdr, blfRawObject(blfContType, make([]byte, 8))...)},
		{"bad zlib header", "container", append(hdr, blfRawObject(blfContType, zlibBody([]byte("not zlib at all"), 15))...)},
		{"zlib shorter than announced", "decompressed", append(hdr, blfRawObject(blfContType, zlibBody(z.Bytes(), 100))...)},
		{"truncated padding", "padding", append(hdr, append(blfRawObject(99, make([]byte, 2)), 0)...)}, // 18 bytes: 2 of padding, 1 present
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := readBLFErr(t, c.file)
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("got %v, want a ParseError", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("%v does not name %q", err, c.want)
			}
		})
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// TestBLFWriterErrors: a failing output and a stop time BLF cannot hold
// are reported, by Write and by Close.
func TestBLFWriterErrors(t *testing.T) {
	pinLocalUTC(t)
	f := canbus.Frame{ID: 1, Len: 1, Timestamp: time.Unix(1700000000, 0)}
	w, err := NewBLFWriter(failWriter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(&f); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("Write to a failing output: %v", err)
	}
	w, _ = NewBLFWriter(failWriter{})
	if err := w.Close(); err == nil {
		t.Error("Close of an empty trace on a failing output succeeded")
	}

	// The last frame lies beyond the SYSTEMTIME year 30827: the final
	// header written by Close cannot hold the stop time.
	out, err := os.Create(filepath.Join(t.TempDir(), "late.blf"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	w, _ = NewBLFWriter(out)
	f.Timestamp = time.Date(30827, 12, 31, 0, 0, 0, 0, time.UTC)
	if err := w.Write(&f); err != nil {
		t.Fatal(err)
	}
	f.Timestamp = f.Timestamp.Add(48 * time.Hour)
	if err := w.Write(&f); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); !errors.Is(err, ErrUnrepresentable) {
		t.Errorf("Close with a stop time in 30828: %v", err)
	}
}

// TestPCAPNGBlockLimits: the reader rejects a block length that is not a
// multiple of 4, and the writer refuses a block beyond the reader's limit.
func TestPCAPNGBlockLimits(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewPCAPNGWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(&canbus.Frame{ID: 1, Len: 1, Timestamp: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	bad := make([]byte, 16)
	binary.LittleEndian.PutUint32(bad[0:], 6) // EPB
	binary.LittleEndian.PutUint32(bad[4:], 13)
	r, err := NewPCAPNGReader(bytes.NewReader(append(buf.Bytes(), bad...)))
	if err != nil {
		t.Fatal(err)
	}
	var f canbus.Frame
	if err := r.Read(&f); err != nil {
		t.Fatal(err)
	}
	if err := r.Read(&f); err == nil || !strings.Contains(err.Error(), "bad block length 13") {
		t.Errorf("block length 13: %v", err)
	}

	w, _ = NewPCAPNGWriter(io.Discard)
	w.buf = make([]byte, pcapngMaxBlock)
	if err := w.flushBlock(); !errors.Is(err, ErrUnrepresentable) {
		t.Errorf("block of %d bytes: %v", pcapngMaxBlock+4, err)
	}
}
