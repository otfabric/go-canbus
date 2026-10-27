// SPDX-License-Identifier: MIT

// Package capture reads and writes CAN traces and replays them.
//
// Formats:
//
//   - candump log ([FormatCandump]): the format of can-utils' candump -L
//     and canplayer, one frame per line, "(1700000000.123456) can0
//     123#A1B2". Classical, CAN FD ("##" with the flags nibble), remote
//     ("R"), error frames and the DLC 9..15 suffix ("_F"). Timestamps have
//     microseconds.
//   - JSON Lines ([FormatJSONL]): the lossless format of this package,
//     one JSON object per frame with every [canbus.Frame] field and
//     nanosecond timestamps. Schema: schemas/capture-v1.schema.json.
//   - pcap ([FormatPCAP]) with LINKTYPE_CAN_SOCKETCAN, as written by
//     tcpdump and read by Wireshark; pcapng ([FormatPCAPNG]) is read.
//
// Readers and writers stream: a trace of any size is processed frame by
// frame. [Replay] sends a trace with its original, scaled or no timing.
package capture

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/otfabric/go-canbus"
)

// Format names a trace format.
type Format string

// Trace formats.
const (
	FormatCandump Format = "candump"
	FormatJSONL   Format = "jsonl"
	FormatPCAP    Format = "pcap"
	FormatPCAPNG  Format = "pcapng"
)

// Reader reads the frames of a trace in order. Read returns io.EOF after
// the last frame.
type Reader interface {
	Read(f *canbus.Frame) error
}

// Writer writes frames to a trace. Close flushes buffered output; it does
// not close the underlying writer.
type Writer interface {
	Write(f *canbus.Frame) error
	Close() error
}

// ParseError reports a malformed record.
type ParseError struct {
	Format Format
	Line   int // line (text formats) or record number (binary), from 1
	Msg    string
}

// Error formats the location and problem.
func (e *ParseError) Error() string {
	return fmt.Sprintf("capture: %s record %d: %s", e.Format, e.Line, e.Msg)
}

// ErrUnknownFormat reports input whose format could not be recognised.
var ErrUnknownFormat = errors.New("capture: unknown trace format")

// NewReader detects the format of r from its first bytes and returns a
// reader for it.
func NewReader(r io.Reader) (Reader, Format, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	head, _ := br.Peek(8)
	switch {
	case len(head) >= 4 && (bytes.Equal(head[:4], []byte{0x0A, 0x0D, 0x0D, 0x0A})):
		pr, err := NewPCAPNGReader(br)
		return pr, FormatPCAPNG, err
	case len(head) >= 4 && isPCAPMagic(head[:4]):
		pr, err := NewPCAPReader(br)
		return pr, FormatPCAP, err
	}
	// Text: skip blank lines and comments to the first record.
	for i := 0; ; i++ {
		b, err := br.Peek(i + 1)
		if err != nil || len(b) <= i {
			return nil, "", ErrUnknownFormat
		}
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case '#':
			return NewCandumpReader(br), FormatCandump, nil
		case '(':
			return NewCandumpReader(br), FormatCandump, nil
		case '{':
			return NewJSONLReader(br), FormatJSONL, nil
		}
		return nil, "", ErrUnknownFormat
	}
}

// NewWriter returns a writer of the given format.
func NewWriter(w io.Writer, format Format) (Writer, error) {
	switch format {
	case FormatCandump:
		return NewCandumpWriter(w, ""), nil
	case FormatJSONL:
		return NewJSONLWriter(w), nil
	case FormatPCAP:
		return NewPCAPWriter(w)
	}
	return nil, fmt.Errorf("%w: cannot write %q", ErrUnknownFormat, format)
}
