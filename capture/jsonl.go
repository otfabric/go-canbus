// SPDX-License-Identifier: MIT

package capture

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/otfabric/go-canbus"
)

// SchemaVersion is the version of the JSON Lines capture format
// (schemas/capture-v1.schema.json).
const SchemaVersion = 1

// Record is one line of a JSON Lines capture.
type Record struct {
	Type      string `json:"type"`                // "frame", or "header" for the optional first line
	Version   int    `json:"version,omitempty"`   // header: SchemaVersion
	Timestamp string `json:"timestamp,omitempty"` // seconds since the epoch with 9 fractional digits; absent when unknown
	Interface string `json:"interface,omitempty"`
	ID        uint32 `json:"id"`
	Extended  bool   `json:"extended,omitempty"`
	FD        bool   `json:"fd,omitempty"`
	BRS       bool   `json:"brs,omitempty"`
	ESI       bool   `json:"esi,omitempty"`
	Remote    bool   `json:"remote,omitempty"`
	Error     bool   `json:"error,omitempty"`
	Len       uint8  `json:"len"`
	Len8DLC   uint8  `json:"len8dlc,omitempty"`
	Data      string `json:"data"` // lower-case hex of the payload (empty for remote frames)
	Echo      bool   `json:"echo,omitempty"`
}

// ToRecord converts a frame.
func ToRecord(f *canbus.Frame) Record {
	r := Record{Type: "frame", Interface: f.Interface, ID: uint32(f.ID), Len: f.Len, Len8DLC: f.Len8DLC,
		Extended: f.Flags&canbus.FlagExtended != 0, FD: f.Flags&canbus.FlagFD != 0,
		BRS: f.Flags&canbus.FlagBRS != 0, ESI: f.Flags&canbus.FlagESI != 0,
		Remote: f.Flags&canbus.FlagRemote != 0, Error: f.Flags&canbus.FlagError != 0,
		Data: hex.EncodeToString(f.Payload()), Echo: f.Echo}
	if !f.Timestamp.IsZero() {
		r.Timestamp = fmt.Sprintf("%d.%09d", f.Timestamp.Unix(), f.Timestamp.Nanosecond())
	}
	return r
}

// Frame converts a record back to a frame and validates it.
func (r *Record) Frame(f *canbus.Frame) error {
	*f = canbus.Frame{ID: canbus.ID(r.ID), Len: r.Len, Len8DLC: r.Len8DLC, Interface: r.Interface, Echo: r.Echo}
	set := func(b bool, fl canbus.Flags) {
		if b {
			f.Flags |= fl
		}
	}
	set(r.Extended, canbus.FlagExtended)
	set(r.FD, canbus.FlagFD)
	set(r.BRS, canbus.FlagBRS)
	set(r.ESI, canbus.FlagESI)
	set(r.Remote, canbus.FlagRemote)
	set(r.Error, canbus.FlagError)
	if r.Timestamp != "" {
		t, err := parseTimestamp(r.Timestamp)
		if err != nil {
			return err
		}
		f.Timestamp = t
	}
	data, err := hex.DecodeString(r.Data)
	if err != nil {
		return fmt.Errorf("bad data: %v", err)
	}
	if len(data) > canbus.MaxFDLen {
		return fmt.Errorf("data of %d bytes", len(data))
	}
	if !r.Remote && len(data) != int(r.Len) {
		return fmt.Errorf("data has %d bytes, len says %d", len(data), r.Len)
	}
	if r.Remote && len(data) != 0 {
		return fmt.Errorf("a remote frame carries no data")
	}
	copy(f.Data[:], data)
	return f.Validate()
}

// JSONLReader reads JSON Lines captures.
type JSONLReader struct {
	s    *bufio.Scanner
	line int
}

// NewJSONLReader returns a reader of JSON Lines captures.
func NewJSONLReader(r io.Reader) *JSONLReader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 4096), 1<<20)
	return &JSONLReader{s: s}
}

// Read reads the next frame, skipping blank lines and the header.
func (r *JSONLReader) Read(f *canbus.Frame) error {
	for r.s.Scan() {
		r.line++
		line := strings.TrimSpace(r.s.Text())
		if line == "" {
			continue
		}
		var rec Record
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rec); err != nil {
			return &ParseError{Format: FormatJSONL, Line: r.line, Msg: err.Error()}
		}
		switch rec.Type {
		case "header":
			if rec.Version != SchemaVersion {
				return &ParseError{Format: FormatJSONL, Line: r.line, Msg: fmt.Sprintf("unsupported version %d", rec.Version)}
			}
			continue
		case "frame":
		default:
			return &ParseError{Format: FormatJSONL, Line: r.line, Msg: fmt.Sprintf("unknown record type %q", rec.Type)}
		}
		if err := rec.Frame(f); err != nil {
			return &ParseError{Format: FormatJSONL, Line: r.line, Msg: err.Error()}
		}
		return nil
	}
	if err := r.s.Err(); err != nil {
		return err
	}
	return io.EOF
}

// JSONLWriter writes JSON Lines captures, starting with a header line.
type JSONLWriter struct {
	w      *bufio.Writer
	enc    *json.Encoder
	header bool
}

// NewJSONLWriter returns a writer of JSON Lines captures.
func NewJSONLWriter(w io.Writer) *JSONLWriter {
	bw := bufio.NewWriter(w)
	return &JSONLWriter{w: bw, enc: json.NewEncoder(bw)}
}

// Write writes one frame record.
func (w *JSONLWriter) Write(f *canbus.Frame) error {
	if !w.header {
		w.header = true
		hdr := struct {
			Type    string `json:"type"`
			Version int    `json:"version"`
		}{"header", SchemaVersion}
		if err := w.enc.Encode(hdr); err != nil {
			return err
		}
	}
	return w.enc.Encode(ToRecord(f))
}

// Close flushes the output.
func (w *JSONLWriter) Close() error { return w.w.Flush() }
