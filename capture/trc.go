// SPDX-License-Identifier: MIT

package capture

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/otfabric/go-canbus"
)

// TRC is PEAK-System's text trace format (*.trc) of PCAN-View, PCAN-Explorer
// and the PCAN-Basic tracer. File versions 1.0, 1.1, 1.2, 1.3, 2.0 and 2.1
// are read; 2.1 is written.
//
// Records in 1.x files are "<n>) <offset ms> [<bus>] [Rx|Tx] <id> [-] <dlc>
// <data>|RTR" (the bus column from 1.2, the reserved "-" from 1.3). In 2.x
// files the ";$COLUMNS=" header orders the columns (N number, O offset ms,
// T type, B bus, I id, d direction, R reserved, l data length, L DLC, D
// data) and T is DT (data), RR (remote), FD, FB (bit rate switch), FE
// (error state indicator), BI (both) or ER (error frame); status and event
// records (ST, EC, EV, ...) are skipped. Identifiers of more than four hex
// digits are extended.
//
// ";$STARTTIME=" is the start in days since 1899-12-30 (an OLE date) in
// local time without a zone, read and written in time.Local like ASC and
// BLF; offsets are milliseconds from it. The bus number becomes the frame's
// Interface ("1", "2", ...); Tx records are read as echoes and echoes are
// written as Tx.
//
// Error frames: an ER record holds five bytes (error type 0 bit, 1 form, 2
// stuff, 3 other; direction 0 transmit, 1 receive; bit position; receive
// and transmit error counters). They are read as a Linux protocol error
// with the counters, and a Linux error frame is written from its protocol
// violation and counters; other error classes are lost. An ER record that
// does not have that layout is read as a protocol error without detail.

// oleEpoch is day 0 of an OLE date, as a wall-clock time in UTC.
var oleEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

// oleToTime converts an OLE date to a local time, rounded to the
// microsecond. A float64 OLE date of today resolves about 0.6 µs, so a
// time within 2 µs of a whole millisecond is taken as that millisecond
// (the writer writes whole milliseconds).
func oleToTime(days float64) time.Time {
	d := time.Duration(math.Round(days*24*float64(time.Hour)/1e3) * 1e3)
	if ms := d.Round(time.Millisecond); (d-ms).Abs() <= 2*time.Microsecond {
		d = ms
	}
	wall := oleEpoch.Add(d)
	return time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), time.Local)
}

// timeToOLE converts a time to an OLE date of its local wall clock.
func timeToOLE(t time.Time) float64 {
	t = t.In(time.Local)
	wall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	return float64(wall.Sub(oleEpoch)) / float64(24*time.Hour)
}

// TRCReader reads PEAK TRC traces.
type TRCReader struct {
	s       *bufio.Scanner
	line    int
	version int // 10, 11, 12, 13, 20, 21
	start   time.Time
	cols    map[byte]int // 2.x column positions
	ncols   int
}

// NewTRCReader returns a reader of TRC traces.
func NewTRCReader(r io.Reader) *TRCReader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 4096), 1<<20)
	return &TRCReader{s: s, version: 10, start: time.Unix(0, 0)}
}

func (r *TRCReader) fail(format string, args ...any) error {
	return &ParseError{Format: FormatTRC, Line: r.line, Msg: fmt.Sprintf(format, args...)}
}

// Version returns the file version read so far, for example "2.1".
func (r *TRCReader) Version() string {
	return strconv.Itoa(r.version/10) + "." + strconv.Itoa(r.version%10)
}

func (r *TRCReader) header(line string) error {
	key, val, ok := strings.Cut(strings.TrimPrefix(line, ";$"), "=")
	if !ok {
		return nil
	}
	val = strings.TrimSpace(val)
	switch strings.ToUpper(strings.TrimSpace(key)) {
	case "FILEVERSION":
		switch val {
		case "1.1":
			r.version = 11
		case "1.2":
			r.version = 12
		case "1.3":
			r.version = 13
		case "2.0":
			r.version = 20
		case "2.1":
			r.version = 21
		default:
			return r.fail("unsupported file version %q", val)
		}
		if r.version >= 20 && r.cols == nil {
			// 2.0 files may omit $COLUMNS: these are its fixed columns.
			r.setColumns("N,O,T,I,d,l,D")
		}
	case "STARTTIME":
		days, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return r.fail("bad start time %q", val)
		}
		r.start = oleToTime(days)
	case "COLUMNS":
		r.setColumns(val)
	}
	return nil
}

func (r *TRCReader) setColumns(spec string) {
	r.cols = map[byte]int{}
	names := strings.Split(spec, ",")
	for i, c := range names {
		if c = strings.TrimSpace(c); len(c) == 1 {
			r.cols[c[0]] = i
		}
	}
	r.ncols = len(names)
}

// Read reads the next frame.
func (r *TRCReader) Read(f *canbus.Frame) error {
	for r.s.Scan() {
		r.line++
		line := strings.TrimSpace(r.s.Text())
		if line == "" {
			continue
		}
		if line[0] == ';' {
			if strings.HasPrefix(line, ";$") {
				if err := r.header(line); err != nil {
					return err
				}
			}
			continue
		}
		*f = canbus.Frame{}
		var ok bool
		var err error
		if r.version >= 20 {
			ok, err = r.record2(line, f)
		} else {
			ok, err = r.record1(line, f)
		}
		if err != nil {
			return r.fail("%v", err)
		}
		if ok {
			return nil
		}
	}
	if err := r.s.Err(); err != nil {
		return err
	}
	return io.EOF
}

func (r *TRCReader) offset(s string, f *canbus.Frame) error {
	ms, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("bad time offset %q", s)
	}
	f.Timestamp = r.start.Add(time.Duration(math.Round(ms*1e3)) * time.Microsecond)
	return nil
}

func trcID(s string, f *canbus.Frame) error {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return fmt.Errorf("bad identifier %q", s)
	}
	if len(s) > 4 {
		if v > 0x1FFFFFFF {
			return fmt.Errorf("identifier %q exceeds 29 bits", s)
		}
		f.Flags |= canbus.FlagExtended
	} else if v > 0x7FF {
		return fmt.Errorf("identifier %q exceeds 11 bits", s)
	}
	f.ID = canbus.ID(v)
	return nil
}

func trcData(fields []string, n int, f *canbus.Frame) error {
	if len(fields) < n {
		return fmt.Errorf("%d data bytes announced, %d present", n, len(fields))
	}
	for i := 0; i < n; i++ {
		b, err := strconv.ParseUint(fields[i], 16, 8)
		if err != nil {
			return fmt.Errorf("bad data byte %q", fields[i])
		}
		f.Data[i] = byte(b)
	}
	return nil
}

// record1 parses a 1.x record.
func (r *TRCReader) record1(line string, f *canbus.Frame) (bool, error) {
	fields := strings.Fields(line)
	if len(fields) < 4 || !strings.HasSuffix(fields[0], ")") {
		return false, fmt.Errorf("not a TRC %s record", r.Version())
	}
	if err := r.offset(fields[1], f); err != nil {
		return false, err
	}
	rest := fields[2:]
	f.Interface = "1"
	if r.version >= 12 {
		if _, err := strconv.Atoi(rest[0]); err != nil {
			return false, fmt.Errorf("bad bus %q", rest[0])
		}
		f.Interface, rest = rest[0], rest[1:]
	}
	if r.version >= 11 {
		if len(rest) == 0 {
			return false, fmt.Errorf("short record")
		}
		switch rest[0] {
		case "Rx":
		case "Tx":
			f.Echo = true
		default:
			return false, nil // Warng, Error, ... records carry no frame
		}
		rest = rest[1:]
	}
	if len(rest) < 2 {
		return false, fmt.Errorf("short record")
	}
	if rest[0] == "FFFFFFFF" {
		return false, nil // 1.0 bus status line
	}
	if err := trcID(rest[0], f); err != nil {
		return false, err
	}
	rest = rest[1:]
	if r.version >= 13 {
		rest = rest[1:] // reserved "-"
		if len(rest) == 0 {
			return false, fmt.Errorf("short record")
		}
	}
	dlc, err := strconv.ParseUint(rest[0], 10, 8)
	if err != nil || dlc > 15 {
		return false, fmt.Errorf("bad DLC %q", rest[0])
	}
	f.Len = uint8(canbus.DLCToLen(uint8(dlc), false))
	if dlc > 8 {
		f.Len8DLC = uint8(dlc)
	}
	rest = rest[1:]
	if len(rest) > 0 && rest[0] == "RTR" {
		f.Flags |= canbus.FlagRemote
		f.Len8DLC = 0
		return true, nil
	}
	return true, trcData(rest, int(f.Len), f)
}

// col returns column c of a 2.x record, "" when absent.
func (r *TRCReader) col(fields []string, c byte) string {
	if i, ok := r.cols[c]; ok && i < len(fields) {
		return fields[i]
	}
	return ""
}

// record2 parses a 2.x record.
func (r *TRCReader) record2(line string, f *canbus.Frame) (bool, error) {
	if r.cols == nil {
		return false, fmt.Errorf("no $COLUMNS header")
	}
	fields := strings.Fields(line)
	// The data column is last; it holds the remaining fields.
	var data []string
	if i, ok := r.cols['D']; ok && i < len(fields) {
		data = fields[i:]
		fields = fields[:i]
	}
	typ := r.col(fields, 'T')
	switch typ {
	case "DT", "RR", "FD", "FB", "FE", "BI", "ER":
	case "":
		return false, fmt.Errorf("record without type")
	default:
		return false, nil // status, error counter and event records
	}
	if err := r.offset(r.col(fields, 'O'), f); err != nil {
		return false, err
	}
	f.Interface = "1"
	if b := r.col(fields, 'B'); b != "" && b != "-" {
		if _, err := strconv.Atoi(b); err != nil {
			return false, fmt.Errorf("bad bus %q", b)
		}
		f.Interface = b
	}
	f.Echo = r.col(fields, 'd') == "Tx"
	if typ == "ER" {
		trcErrorFrame(data, f)
		return true, nil
	}
	if err := trcID(r.col(fields, 'I'), f); err != nil {
		return false, err
	}
	fd := typ != "DT" && typ != "RR"
	switch typ {
	case "RR":
		f.Flags |= canbus.FlagRemote
	case "FD":
		f.Flags |= canbus.FlagFD
	case "FB":
		f.Flags |= canbus.FlagFD | canbus.FlagBRS
	case "FE":
		f.Flags |= canbus.FlagFD | canbus.FlagESI
	case "BI":
		f.Flags |= canbus.FlagFD | canbus.FlagBRS | canbus.FlagESI
	}
	switch {
	case r.col(fields, 'l') != "":
		s := r.col(fields, 'l')
		n, err := strconv.ParseUint(s, 10, 8)
		if err != nil || n > 64 || !fd && n > 8 || fd && canbus.DLCToLen(canbus.LenToDLC(int(n)), true) != int(n) {
			return false, fmt.Errorf("bad data length %q", s)
		}
		f.Len = uint8(n)
	case r.col(fields, 'L') != "":
		s := r.col(fields, 'L')
		dlc, err := strconv.ParseUint(s, 10, 8)
		if err != nil || dlc > 15 {
			return false, fmt.Errorf("bad DLC %q", s)
		}
		f.Len = uint8(canbus.DLCToLen(uint8(dlc), fd))
		if !fd && dlc > 8 && typ == "DT" {
			f.Len8DLC = uint8(dlc)
		}
	default:
		return false, fmt.Errorf("record without length or DLC")
	}
	if typ == "RR" {
		return true, nil
	}
	return true, trcData(data, int(f.Len), f)
}

// trcErrorFrame converts the data of an ER record.
func trcErrorFrame(data []string, f *canbus.Frame) {
	var b [5]byte
	ok := len(data) >= 5
	for i := 0; ok && i < 5; i++ {
		v, err := strconv.ParseUint(data[i], 16, 8)
		ok = err == nil
		b[i] = byte(v)
	}
	info := canbus.ErrorInfo{Class: canbus.ErrClassProtocol}
	if ok && b[0] <= 3 && b[1] <= 1 {
		info.Protocol = [4]canbus.ProtocolViolation{canbus.ProtBit, canbus.ProtForm, canbus.ProtStuff, 0}[b[0]]
		if b[1] == 0 {
			info.Protocol |= canbus.ProtTx
		}
		info.RxErrors, info.TxErrors, info.CountersValid = b[3], b[4], true
	}
	ts, iface, echo := f.Timestamp, f.Interface, f.Echo
	*f = canbus.NewErrorFrame(info)
	f.Timestamp, f.Interface, f.Echo = ts, iface, echo
}

// TRCWriter writes PEAK TRC 2.1 traces.
type TRCWriter struct {
	w       *bufio.Writer
	started bool
	start   time.Time
	n       int
}

// NewTRCWriter returns a writer of TRC 2.1 traces.
func NewTRCWriter(w io.Writer) *TRCWriter { return &TRCWriter{w: bufio.NewWriter(w)} }

func (w *TRCWriter) header(t time.Time) error {
	w.started = true
	// The start is written to the millisecond: offsets carry the rest.
	t = t.Truncate(time.Millisecond)
	w.start = t
	_, err := fmt.Fprintf(w.w, `;$FILEVERSION=2.1
;$STARTTIME=%s
;$COLUMNS=N,O,T,B,I,d,R,L,D
;
;   Start time: %s
;   Generated by go-canbus
;-------------------------------------------------------------------------------
;   Message   Time    Type    ID     Rx/Tx
;   Number    Offset  |  Bus  [hex]  |  Reserved
;   |         [ms]    |  |    |      |  |  Data Length Code
;   |         |       |  |    |      |  |  |    Data [hex] ...
;   |         |       |  |    |      |  |  |    |
;---+-- ------+------ +- +- --+----- +- +- +--- +- -- -- -- -- -- -- --
`, strconv.FormatFloat(timeToOLE(t), 'f', -1, 64), t.In(time.Local).Format("02.01.2006 15:04:05.000"))
	return err
}

// Write writes one frame. Frames without timestamp are written at the
// start time.
func (w *TRCWriter) Write(f *canbus.Frame) error {
	ts := f.Timestamp
	if !w.started {
		start := ts
		if start.IsZero() {
			start = time.Unix(0, 0)
		}
		if err := w.header(start); err != nil {
			return err
		}
	}
	off := 0.0
	if !ts.IsZero() {
		off = float64(ts.Sub(w.start).Round(time.Microsecond)) / float64(time.Millisecond)
	}
	w.n++
	bus := ascChannel(f.Interface)
	dir := "Rx"
	if f.Echo {
		dir = "Tx"
	}
	var typ, id, data string
	dlc := f.DLC()
	switch {
	case f.IsError():
		info, _ := canbus.ParseError(f)
		var b [5]byte
		switch {
		case info.Protocol&canbus.ProtBit != 0:
			b[0] = 0
		case info.Protocol&canbus.ProtForm != 0:
			b[0] = 1
		case info.Protocol&canbus.ProtStuff != 0:
			b[0] = 2
		default:
			b[0] = 3
		}
		if info.Protocol&canbus.ProtTx == 0 {
			b[1] = 1
		}
		b[3], b[4] = info.RxErrors, info.TxErrors
		typ, id, dlc, data = "ER", "-", 5, hexBytes(b[:])
	case f.IsFD():
		switch f.Flags & (canbus.FlagBRS | canbus.FlagESI) {
		case 0:
			typ = "FD"
		case canbus.FlagBRS:
			typ = "FB"
		case canbus.FlagESI:
			typ = "FE"
		default:
			typ = "BI"
		}
		data = hexBytes(f.Payload())
	case f.IsRemote():
		typ, dlc = "RR", f.Len
	default:
		typ, data = "DT", hexBytes(f.Payload())
	}
	if id == "" {
		if f.IsExtended() {
			id = fmt.Sprintf("%08X", uint32(f.ID))
		} else {
			id = fmt.Sprintf("%04X", uint32(f.ID))
		}
	}
	_, err := fmt.Fprintf(w.w, "%7d %13.3f %s %2s %8s %2s -  %-4d %s\n", w.n, off, typ, bus, id, dir, dlc, data)
	return err
}

// Close flushes buffered output.
func (w *TRCWriter) Close() error {
	if !w.started {
		if err := w.header(time.Unix(0, 0)); err != nil {
			return err
		}
	}
	return w.w.Flush()
}
