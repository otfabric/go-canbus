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

// XLRecord is one CAN XL line of a JSON Lines capture (type "xlframe").
type XLRecord struct {
	Type      string `json:"type"` // "xlframe"
	Timestamp string `json:"timestamp,omitempty"`
	Interface string `json:"interface,omitempty"`
	Priority  uint16 `json:"priority"`
	VCID      uint8  `json:"vcid,omitempty"`
	SEC       bool   `json:"sec,omitempty"`
	RRS       bool   `json:"rrs,omitempty"`
	SDT       uint8  `json:"sdt"`
	AF        uint32 `json:"af"`
	Len       int    `json:"len"`
	Data      string `json:"data"` // lower-case hex of the payload
	Echo      bool   `json:"echo,omitempty"`
}

// ToXLRecord converts a CAN XL frame.
func ToXLRecord(x *canbus.XLFrame) XLRecord {
	r := XLRecord{Type: "xlframe", Interface: x.Interface, Priority: x.Priority, VCID: x.VCID,
		SEC: x.Flags&canbus.XLSEC != 0, RRS: x.Flags&canbus.XLRRS != 0, SDT: x.SDT, AF: x.AF,
		Len: len(x.Data), Data: hex.EncodeToString(x.Data), Echo: x.Echo}
	if !x.Timestamp.IsZero() {
		r.Timestamp = fmt.Sprintf("%d.%09d", x.Timestamp.Unix(), x.Timestamp.Nanosecond())
	}
	return r
}

// XLFrame converts a record back to a CAN XL frame and validates it.
func (r *XLRecord) XLFrame(x *canbus.XLFrame) error {
	g := canbus.XLFrame{Priority: r.Priority, VCID: r.VCID, SDT: r.SDT, AF: r.AF, Interface: r.Interface, Echo: r.Echo}
	if r.SEC {
		g.Flags |= canbus.XLSEC
	}
	if r.RRS {
		g.Flags |= canbus.XLRRS
	}
	if r.Timestamp != "" {
		t, err := parseTimestamp(r.Timestamp)
		if err != nil {
			return err
		}
		g.Timestamp = t
	}
	data, err := hex.DecodeString(r.Data)
	if err != nil {
		return fmt.Errorf("bad data: %v", err)
	}
	if len(data) != r.Len {
		return fmt.Errorf("data has %d bytes, len says %d", len(data), r.Len)
	}
	g.Data = data
	if err := g.Validate(); err != nil {
		return err
	}
	*x = g
	return nil
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

// Read reads the next CAN or CAN FD frame, skipping blank lines, the
// header and CAN XL records (use [JSONLReader.ReadAny] for those).
func (r *JSONLReader) Read(f *canbus.Frame) error {
	for {
		xl, err := r.ReadAny(f, nil)
		if err != nil || !xl {
			return err
		}
	}
}

// ReadAny reads the next frame of any kind: a "frame" record into f (xl
// false) or an "xlframe" record into x (xl true). With x nil a CAN XL
// record is reported without being decoded.
func (r *JSONLReader) ReadAny(f *canbus.Frame, x *canbus.XLFrame) (xl bool, err error) {
	for r.s.Scan() {
		r.line++
		line := strings.TrimSpace(r.s.Text())
		if line == "" {
			continue
		}
		perr := func(msg string) error { return &ParseError{Format: FormatJSONL, Line: r.line, Msg: msg} }
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &kind); err != nil {
			return false, perr(err.Error())
		}
		if kind.Type == "xlframe" {
			if x == nil {
				return true, nil
			}
			var rec XLRecord
			dec := json.NewDecoder(strings.NewReader(line))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&rec); err != nil {
				return true, perr(err.Error())
			}
			if err := rec.XLFrame(x); err != nil {
				return true, perr(err.Error())
			}
			return true, nil
		}
		var rec Record
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rec); err != nil {
			return false, perr(err.Error())
		}
		switch rec.Type {
		case "header":
			if rec.Version != SchemaVersion {
				return false, perr(fmt.Sprintf("unsupported version %d", rec.Version))
			}
			continue
		case "frame":
		default:
			return false, perr(fmt.Sprintf("unknown record type %q", rec.Type))
		}
		if err := rec.Frame(f); err != nil {
			return false, perr(err.Error())
		}
		return false, nil
	}
	if err := r.s.Err(); err != nil {
		return false, err
	}
	return false, io.EOF
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
	if err := w.writeHeader(); err != nil {
		return err
	}
	return w.enc.Encode(ToRecord(f))
}

// WriteXL writes one CAN XL record.
func (w *JSONLWriter) WriteXL(x *canbus.XLFrame) error {
	if err := w.writeHeader(); err != nil {
		return err
	}
	return w.enc.Encode(ToXLRecord(x))
}

func (w *JSONLWriter) writeHeader() error {
	if w.header {
		return nil
	}
	w.header = true
	hdr := struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
	}{"header", SchemaVersion}
	return w.enc.Encode(hdr)
}

// Close flushes the output.
func (w *JSONLWriter) Close() error { return w.w.Flush() }
