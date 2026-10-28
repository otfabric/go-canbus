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

// CandumpReader reads candump log files (candump -L, canplayer input).
// Blank lines and lines starting with '#' are skipped. A trailing
// direction token ("R" or "T", written by some tools) is accepted; "T"
// marks the frame as [canbus.Frame.Echo].
type CandumpReader struct {
	s    *bufio.Scanner
	line int
}

// NewCandumpReader returns a reader of candump log lines.
func NewCandumpReader(r io.Reader) *CandumpReader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 4096), 1<<20)
	return &CandumpReader{s: s}
}

// Read reads the next CAN or CAN FD frame. CAN XL lines are skipped: use
// [CandumpReader.ReadAny] to get them.
func (r *CandumpReader) Read(f *canbus.Frame) error {
	for {
		xl, err := r.ReadAny(f, nil)
		if err != nil || !xl {
			return err
		}
	}
}

// ReadAny reads the next frame of any kind: a CAN or CAN FD frame into f
// (xl false) or a CAN XL frame into x (xl true). With x nil a CAN XL line
// is reported without being decoded.
func (r *CandumpReader) ReadAny(f *canbus.Frame, x *canbus.XLFrame) (xl bool, err error) {
	for r.s.Scan() {
		r.line++
		line := strings.TrimSpace(r.s.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		if fields := strings.Fields(line); len(fields) >= 3 && canbus.IsXLText(fields[2]) {
			if x == nil {
				return true, nil
			}
			if err := ParseCandumpXLLine(line, x); err != nil {
				return true, &ParseError{Format: FormatCandump, Line: r.line, Msg: err.Error()}
			}
			return true, nil
		}
		if err := ParseCandumpLine(line, f); err != nil {
			return false, &ParseError{Format: FormatCandump, Line: r.line, Msg: err.Error()}
		}
		return false, nil
	}
	if err := r.s.Err(); err != nil {
		return false, err
	}
	return false, io.EOF
}

// ParseCandumpXLLine parses one log line holding a CAN XL frame,
// "(<seconds>.<fraction>) <iface> <vcid><prio>#<flags>:<sdt>:<af>#<data>",
// into x.
func ParseCandumpXLLine(line string, x *canbus.XLFrame) error {
	fields := strings.Fields(line)
	if len(fields) < 3 || len(fields) > 4 {
		return fmt.Errorf("want \"(<time>) <interface> <frame>\", got %q", line)
	}
	ts := fields[0]
	if len(ts) < 3 || ts[0] != '(' || ts[len(ts)-1] != ')' {
		return fmt.Errorf("bad timestamp %q", ts)
	}
	t, err := parseTimestamp(ts[1 : len(ts)-1])
	if err != nil {
		return err
	}
	g, err := canbus.ParseXLFrame(fields[2])
	if err != nil {
		return err
	}
	g.Timestamp, g.Interface = t, fields[1]
	if len(fields) == 4 {
		switch fields[3] {
		case "T":
			g.Echo = true
		case "R":
		default:
			return fmt.Errorf("unexpected trailing field %q", fields[3])
		}
	}
	*x = g
	return nil
}

// ParseCandumpLine parses one log line "(<seconds>.<fraction>) <iface>
// <frame>" into f.
func ParseCandumpLine(line string, f *canbus.Frame) error {
	fields := strings.Fields(line)
	if len(fields) < 3 || len(fields) > 4 {
		return fmt.Errorf("want \"(<time>) <interface> <frame>\", got %q", line)
	}
	ts := fields[0]
	if len(ts) < 3 || ts[0] != '(' || ts[len(ts)-1] != ')' {
		return fmt.Errorf("bad timestamp %q", ts)
	}
	t, err := parseTimestamp(ts[1 : len(ts)-1])
	if err != nil {
		return err
	}
	if err := ParseFrame(fields[2], f); err != nil {
		return err
	}
	f.Timestamp = t
	f.Interface = fields[1]
	f.Echo = false
	if len(fields) == 4 {
		switch fields[3] {
		case "T":
			f.Echo = true
		case "R":
		default:
			return fmt.Errorf("unexpected trailing field %q", fields[3])
		}
	}
	return nil
}

// parseTimestamp parses decimal seconds with up to 9 fractional digits
// without going through floating point.
func parseTimestamp(s string) (time.Time, error) {
	secText, fracText, _ := strings.Cut(s, ".")
	sec, err := strconv.ParseInt(secText, 10, 64)
	if err != nil || sec < 0 {
		return time.Time{}, fmt.Errorf("bad timestamp %q", s)
	}
	if len(fracText) > 9 {
		return time.Time{}, fmt.Errorf("timestamp %q has more than 9 fractional digits", s)
	}
	var ns int64
	if fracText != "" {
		v, err := strconv.ParseUint(fracText, 10, 32)
		if err != nil {
			return time.Time{}, fmt.Errorf("bad timestamp %q", s)
		}
		ns = int64(v)
		for i := len(fracText); i < 9; i++ {
			ns *= 10
		}
	}
	return time.Unix(sec, ns), nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// ParseFrame parses the compact frame notation of cansend and candump:
//
//	<id>#<data>            classical, data as hex pairs, '.' separators allowed
//	<id>#<data>_<dlc>      classical with 8 bytes and a raw DLC of 9..F
//	<id>#R[<len>]          remote frame, optional DLC 0..8
//	<id>##<flags><data>    CAN FD, flags nibble: 1 = BRS, 2 = ESI, 4 = FDF (ignored)
//
// <id> has 3 hex digits for a standard and 8 for an extended identifier;
// an 8-digit identifier with bit 29 (0x20000000) set is an error frame.
// CAN FD data whose length is not a CAN FD length is padded with zeros
// to the next one, as the kernel transmits it.
func ParseFrame(s string, f *canbus.Frame) error {
	*f = canbus.Frame{}
	idText, rest, ok := strings.Cut(s, "#")
	if !ok {
		return fmt.Errorf("frame %q has no '#'", s)
	}
	id, err := strconv.ParseUint(idText, 16, 32)
	if err != nil {
		return fmt.Errorf("frame %q: bad identifier", s)
	}
	switch len(idText) {
	case 3:
		if id > uint64(canbus.MaxStandardID) {
			return fmt.Errorf("frame %q: standard identifier exceeds 0x7FF", s)
		}
	case 8:
		switch {
		case id&0x20000000 != 0:
			f.Flags = canbus.FlagError
			id &= uint64(canbus.ErrClassAll)
		case id&0xC0000000 != 0:
			return fmt.Errorf("frame %q: extended identifier exceeds 29 bits", s)
		default:
			f.Flags = canbus.FlagExtended
		}
	default:
		return fmt.Errorf("frame %q: identifier needs 3 or 8 hex digits", s)
	}
	f.ID = canbus.ID(id)
	if strings.HasPrefix(rest, "##") {
		return fmt.Errorf("frame %q: CAN XL frames are not supported", s)
	}
	max := 8
	switch {
	case strings.HasPrefix(rest, "#"):
		if f.Flags&canbus.FlagError != 0 {
			return fmt.Errorf("frame %q: error frames are classical", s)
		}
		if len(rest) < 2 {
			return fmt.Errorf("frame %q: CAN FD flags nibble missing", s)
		}
		// The nibble is canfd_frame.flags: BRS 1, ESI 2 and, as can-utils
		// since 2023 writes it, FDF 4 (implied by "##").
		nib, ok := hexNibble(rest[1])
		if !ok {
			return fmt.Errorf("frame %q: bad CAN FD flags %q", s, rest[1])
		}
		f.Flags |= canbus.FlagFD
		if nib&1 != 0 {
			f.Flags |= canbus.FlagBRS
		}
		if nib&2 != 0 {
			f.Flags |= canbus.FlagESI
		}
		rest = rest[2:]
		max = 64
	case len(rest) > 0 && (rest[0] == 'R' || rest[0] == 'r'):
		if f.Flags&canbus.FlagError != 0 {
			return fmt.Errorf("frame %q: an error frame cannot be remote", s)
		}
		f.Flags |= canbus.FlagRemote
		switch len(rest) {
		case 1:
		case 2:
			n, ok := hexNibble(rest[1])
			if !ok || n > 8 {
				return fmt.Errorf("frame %q: bad remote DLC", s)
			}
			f.Len = n
		default:
			return fmt.Errorf("frame %q: data after R", s)
		}
		return nil
	}
	var dlcText string
	if f.Flags&canbus.FlagFD == 0 {
		rest, dlcText, _ = strings.Cut(rest, "_")
	}
	n := 0
	for i := 0; i < len(rest); {
		if rest[i] == '.' {
			i++
			continue
		}
		if i+1 >= len(rest) {
			return fmt.Errorf("frame %q: odd number of hex digits", s)
		}
		hi, ok1 := hexNibble(rest[i])
		lo, ok2 := hexNibble(rest[i+1])
		if !ok1 || !ok2 {
			return fmt.Errorf("frame %q: bad hex data", s)
		}
		if n == max {
			return fmt.Errorf("frame %q: more than %d data bytes", s, max)
		}
		f.Data[n] = hi<<4 | lo
		n++
		i += 2
	}
	if f.Flags&canbus.FlagFD != 0 {
		n = canbus.RoundUpFDLen(n)
	}
	f.Len = uint8(n)
	if dlcText != "" {
		d, ok := hexNibble(dlcText[0])
		if len(dlcText) != 1 || !ok || d < 9 || n != 8 {
			return fmt.Errorf("frame %q: a DLC suffix needs 8 data bytes and one digit 9..F", s)
		}
		f.Len8DLC = d
	}
	return nil
}

// FormatFrame formats a frame in the notation [ParseFrame] reads (the same
// as [canbus.Frame.String]).
func FormatFrame(f *canbus.Frame) string { return f.String() }

// CandumpWriter writes candump log lines.
type CandumpWriter struct {
	w     *bufio.Writer
	iface string
}

// NewCandumpWriter returns a writer of candump log lines. iface replaces
// a frame's empty Interface field (default "can0").
func NewCandumpWriter(w io.Writer, iface string) *CandumpWriter {
	if iface == "" {
		iface = "can0"
	}
	return &CandumpWriter{w: bufio.NewWriter(w), iface: iface}
}

// Write writes one line. The timestamp is truncated to microseconds, as
// candump writes it; frames without timestamp are written at time 0.
func (w *CandumpWriter) Write(f *canbus.Frame) error {
	iface := f.Interface
	if iface == "" {
		iface = w.iface
	}
	var sec, usec int64
	if !f.Timestamp.IsZero() {
		sec = f.Timestamp.Unix()
		usec = int64(f.Timestamp.Nanosecond() / 1000)
	}
	_, err := fmt.Fprintf(w.w, "(%010d.%06d) %s %s\n", sec, usec, iface, f.String())
	return err
}

// WriteXL writes one CAN XL line in can-utils notation.
func (w *CandumpWriter) WriteXL(x *canbus.XLFrame) error {
	iface := x.Interface
	if iface == "" {
		iface = w.iface
	}
	var sec, usec int64
	if !x.Timestamp.IsZero() {
		sec = x.Timestamp.Unix()
		usec = int64(x.Timestamp.Nanosecond() / 1000)
	}
	_, err := fmt.Fprintf(w.w, "(%010d.%06d) %s %s\n", sec, usec, iface, x.String())
	return err
}

// Close flushes the output.
func (w *CandumpWriter) Close() error { return w.w.Flush() }
