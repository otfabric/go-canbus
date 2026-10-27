// SPDX-License-Identifier: MIT

package capture

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/otfabric/go-canbus"
)

// ASC is Vector's text trace format (*.asc), as CANoe/CANalyzer and
// python-can write it. Supported events: classical data and remote frames
// ("1  123  Rx  d 3 11 22 33", extended identifiers with an "x" suffix),
// CAN FD frames ("CANFD 1 Rx 123 ... brs esi dlc len data ..."), and error
// frames ("1  ErrorFrame"). Other events (statistics, triggers, comments,
// "Start of measurement") are skipped. Numbers follow the "base hex|dec"
// header; timestamps are seconds relative to the "Begin Triggerblock" date,
// else the "date" header, else 0.
//
// The channel number becomes the frame's Interface ("1", "2", ...).

// ascDateLayouts are the date header layouts written by Vector tools and
// python-can.
var ascDateLayouts = []string{
	"Mon Jan 2 03:04:05.000 pm 2006",
	"Mon Jan 2 15:04:05.000 2006",
	"Mon Jan 2 03:04:05 pm 2006",
	"Mon Jan 2 15:04:05 2006",
}

func parseASCDate(s string) (time.Time, bool) {
	s = strings.Join(strings.Fields(s), " ")
	for _, l := range ascDateLayouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t, true
		}
	}
	// Some locales write "am"/"pm" in upper case.
	for _, l := range ascDateLayouts {
		if t, err := time.ParseInLocation(l, strings.ToLower(s), time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ASCReader reads Vector ASC traces.
type ASCReader struct {
	s     *bufio.Scanner
	line  int
	base  int // 16 or 10
	start time.Time
}

// NewASCReader returns a reader of ASC traces.
func NewASCReader(r io.Reader) *ASCReader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 4096), 1<<20)
	return &ASCReader{s: s, base: 16, start: time.Unix(0, 0)}
}

func (r *ASCReader) fail(format string, args ...any) error {
	return &ParseError{Format: FormatASC, Line: r.line, Msg: fmt.Sprintf(format, args...)}
}

// Read reads the next frame.
func (r *ASCReader) Read(f *canbus.Frame) error {
	for r.s.Scan() {
		r.line++
		line := strings.TrimSpace(r.s.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		fields := strings.Fields(line)
		switch strings.ToLower(fields[0]) {
		case "date":
			if t, ok := parseASCDate(strings.TrimSpace(line[4:])); ok {
				r.start = t
			}
			continue
		case "base":
			if len(fields) > 1 && fields[1] == "dec" {
				r.base = 10
			} else {
				r.base = 16
			}
			continue
		case "begin":
			// "Begin Triggerblock <date>" is the start of the measurement
			// the offsets count from; python-can writes the time of
			// writing in "date".
			if len(fields) > 2 {
				if t, ok := parseASCDate(strings.Join(fields[2:], " ")); ok {
					r.start = t
				}
			}
			continue
		case "end", "internal", "no":
			continue
		}
		ts, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue // an event this reader does not know
		}
		ok, err := r.event(fields[1:], f)
		if err != nil {
			return r.fail("%v", err)
		}
		if !ok {
			continue
		}
		f.Timestamp = r.start.Add(time.Duration(ts * float64(time.Second)).Round(time.Microsecond))
		return nil
	}
	if err := r.s.Err(); err != nil {
		return err
	}
	return io.EOF
}

func (r *ASCReader) num(s string) (uint64, error) { return strconv.ParseUint(s, r.base, 32) }

func (r *ASCReader) id(s string, f *canbus.Frame) error {
	ext := strings.HasSuffix(s, "x") || strings.HasSuffix(s, "X")
	s = strings.TrimRight(s, "xX")
	v, err := r.num(s)
	if err != nil {
		return fmt.Errorf("bad identifier %q", s)
	}
	f.ID = canbus.ID(v)
	if ext {
		f.Flags |= canbus.FlagExtended
		if f.ID > canbus.MaxExtendedID {
			return fmt.Errorf("identifier %q exceeds 29 bits", s)
		}
	} else if f.ID > canbus.MaxStandardID {
		return fmt.Errorf("identifier %q exceeds 11 bits (no x suffix)", s)
	}
	return nil
}

func (r *ASCReader) data(fields []string, n int, f *canbus.Frame) error {
	if len(fields) < n {
		return fmt.Errorf("%d data bytes announced, %d present", n, len(fields))
	}
	for i := 0; i < n; i++ {
		b, err := strconv.ParseUint(fields[i], r.base, 8)
		if err != nil {
			return fmt.Errorf("bad data byte %q", fields[i])
		}
		f.Data[i] = byte(b)
	}
	return nil
}

// event decodes the fields after the timestamp. It reports false for
// events that are not frames.
func (r *ASCReader) event(fields []string, f *canbus.Frame) (bool, error) {
	*f = canbus.Frame{}
	if len(fields) == 0 {
		return false, nil
	}
	if strings.EqualFold(fields[0], "CANFD") {
		return r.fd(fields[1:], f)
	}
	ch, err := strconv.Atoi(fields[0])
	if err != nil || len(fields) < 2 {
		return false, nil // "Start of measurement", statistics, ...
	}
	f.Interface = fields[0]
	if strings.EqualFold(fields[1], "ErrorFrame") {
		f.Flags = canbus.FlagError
		f.ID = canbus.ID(canbus.ErrClassProtocol) // ASC carries no class
		f.Len = 8
		return true, nil
	}
	// A frame is "<ch> <id> Rx|Tx d|r ..."; anything else on a channel
	// (statistics, bus load, J1939 or LIN events) is skipped.
	if ch <= 0 || len(fields) < 4 || !strings.EqualFold(fields[2], "Rx") && !strings.EqualFold(fields[2], "Tx") {
		return false, nil
	}
	if err := r.id(fields[1], f); err != nil {
		return false, err
	}
	f.Echo = strings.EqualFold(fields[2], "Tx") // sent by the logging node
	kind := strings.ToLower(fields[3])
	rest := fields[4:]
	switch kind {
	case "r":
		f.Flags |= canbus.FlagRemote
		if len(rest) > 0 {
			if dlc, err := strconv.ParseUint(rest[0], 16, 8); err == nil && dlc <= 8 {
				f.Len = uint8(dlc)
			}
		}
		return true, nil
	case "d":
	default:
		return false, fmt.Errorf("frame type %q", fields[3])
	}
	if len(rest) < 1 {
		return false, fmt.Errorf("DLC missing")
	}
	dlc, err := strconv.ParseUint(rest[0], 16, 8)
	if err != nil || dlc > 15 {
		return false, fmt.Errorf("bad DLC %q", rest[0])
	}
	n := canbus.DLCToLen(uint8(dlc), false)
	if err := r.data(rest[1:], n, f); err != nil {
		return false, err
	}
	f.Len = uint8(n)
	if dlc > 8 {
		f.Len8DLC = uint8(dlc)
	}
	return true, nil
}

// fd decodes "CANFD <ch> <dir> <id> [symbolic name] <brs> <esi> <dlc> <len> <data...> ...".
func (r *ASCReader) fd(fields []string, f *canbus.Frame) (bool, error) {
	if len(fields) < 7 {
		return false, fmt.Errorf("short CANFD event")
	}
	f.Interface = fields[0]
	f.Flags = canbus.FlagFD
	f.Echo = strings.EqualFold(fields[1], "Tx")
	if err := r.id(fields[2], f); err != nil {
		return false, err
	}
	rest := fields[3:]
	// An optional symbolic name precedes the two flag digits.
	if len(rest) > 0 && rest[0] != "0" && rest[0] != "1" {
		rest = rest[1:]
	}
	if len(rest) < 4 {
		return false, fmt.Errorf("short CANFD event")
	}
	if rest[0] == "1" {
		f.Flags |= canbus.FlagBRS
	}
	if rest[1] == "1" {
		f.Flags |= canbus.FlagESI
	}
	dlc, err := strconv.ParseUint(rest[2], 16, 8)
	if err != nil || dlc > 15 {
		return false, fmt.Errorf("bad CAN FD DLC %q", rest[2])
	}
	n, err := strconv.Atoi(rest[3])
	if err != nil || n != canbus.DLCToLen(uint8(dlc), true) {
		return false, fmt.Errorf("data length %q does not match DLC %d", rest[3], dlc)
	}
	if err := r.data(rest[4:], n, f); err != nil {
		return false, err
	}
	f.Len = uint8(n)
	return true, nil
}

// ASCWriter writes Vector ASC traces (base hex, absolute timestamps
// relative to the first frame's date header).
type ASCWriter struct {
	w       *bufio.Writer
	started bool
	start   time.Time
}

// NewASCWriter returns a writer of ASC traces.
func NewASCWriter(w io.Writer) *ASCWriter { return &ASCWriter{w: bufio.NewWriter(w)} }

func ascChannel(iface string) string {
	if n, err := strconv.Atoi(iface); err == nil && n > 0 {
		return strconv.Itoa(n)
	}
	return "1"
}

func (w *ASCWriter) header(t time.Time) error {
	w.started = true
	// The date header has milliseconds: offsets carry the rest.
	t = t.Truncate(time.Millisecond)
	w.start = t
	date := t.Format("Mon Jan 2 03:04:05.000 pm 2006")
	_, err := fmt.Fprintf(w.w, "date %s\nbase hex  timestamps absolute\ninternal events logged\n// version 9.0.0\nBegin Triggerblock %s\n   0.000000 Start of measurement\n", date, date)
	return err
}

func hexBytes(b []byte) string {
	var sb strings.Builder
	for i, c := range b {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%02X", c)
	}
	return sb.String()
}

// Write writes one frame. Frames without timestamp are written at the
// start time.
func (w *ASCWriter) Write(f *canbus.Frame) error {
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
	rel := 0.0
	if !ts.IsZero() {
		rel = ts.Sub(w.start).Seconds()
	}
	ch := ascChannel(f.Interface)
	id := strings.ToUpper(strconv.FormatUint(uint64(f.ID), 16))
	if f.IsExtended() {
		id += "x"
	}
	dir := "Rx"
	if f.Echo {
		dir = "Tx"
	}
	var err error
	switch {
	case f.IsError():
		_, err = fmt.Fprintf(w.w, "%11.6f %s  ErrorFrame\n", rel, ch)
	case f.IsFD():
		brs, esi := 0, 0
		if f.Flags&canbus.FlagBRS != 0 {
			brs = 1
		}
		if f.Flags&canbus.FlagESI != 0 {
			esi = 1
		}
		_, err = fmt.Fprintf(w.w, "%11.6f CANFD %3s %-4s %8s %32s %d %d %x %2d %s %8d %4d %8X %8d %8d %8d %8d %8d\n",
			rel, ch, dir, id, "", brs, esi, f.DLC(), f.Len, hexBytes(f.Payload()), 0, 0, 0x1000, 0, 0, 0, 0, 0)
	case f.IsRemote():
		_, err = fmt.Fprintf(w.w, "%11.6f %s  %-15s %-4s r %x\n", rel, ch, id, dir, f.Len)
	default:
		data := hexBytes(f.Payload())
		_, err = fmt.Fprintf(w.w, "%11.6f %s  %-15s %-4s d %x %s\n", rel, ch, id, dir, f.DLC(), data)
	}
	return err
}

// Close writes the trailer and flushes.
func (w *ASCWriter) Close() error {
	if !w.started {
		if err := w.header(time.Unix(0, 0)); err != nil {
			return err
		}
	}
	if _, err := w.w.WriteString("End TriggerBlock\n"); err != nil {
		return err
	}
	return w.w.Flush()
}
