// SPDX-License-Identifier: MIT

package capture

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/otfabric/go-canbus"
)

// LinkTypeCANSocketCAN is the pcap link type of SocketCAN frames.
const LinkTypeCANSocketCAN = 227

const (
	pcapMagicMicro = 0xA1B2C3D4
	pcapMagicNano  = 0xA1B23C4D
	pcapHeaderLen  = 24
	pcapRecordLen  = 16
	pcapSnapLen    = 8 + canbus.MaxFDLen
	fdFlagBRS      = 0x01
	fdFlagESI      = 0x02
	fdFlagFDF      = 0x04
)

func isPCAPMagic(b []byte) bool {
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		if m := bo.Uint32(b); m == pcapMagicMicro || m == pcapMagicNano {
			return true
		}
	}
	return false
}

// encodeSocketCAN encodes the LINKTYPE_CAN_SOCKETCAN packet of f (CAN ID
// and flags in network byte order).
func encodeSocketCAN(f *canbus.Frame, b []byte) int {
	binary.BigEndian.PutUint32(b[0:], canbus.KernelID(f))
	b[4] = f.Len
	b[5], b[6], b[7] = 0, 0, 0
	if f.Flags&canbus.FlagFD != 0 {
		b[5] = fdFlagFDF
		if f.Flags&canbus.FlagBRS != 0 {
			b[5] |= fdFlagBRS
		}
		if f.Flags&canbus.FlagESI != 0 {
			b[5] |= fdFlagESI
		}
	} else {
		b[7] = f.Len8DLC
	}
	p := f.Payload()
	copy(b[8:], p)
	return 8 + len(p)
}

// decodeSocketCAN decodes a LINKTYPE_CAN_SOCKETCAN packet.
func decodeSocketCAN(b []byte, f *canbus.Frame) error {
	if len(b) < 8 {
		return fmt.Errorf("packet of %d bytes is shorter than the SocketCAN header", len(b))
	}
	id := binary.BigEndian.Uint32(b[0:])
	*f = canbus.Frame{}
	switch {
	case id&0x20000000 != 0:
		f.Flags = canbus.FlagError
		f.ID = canbus.ID(id & 0x1FFFFFFF)
	case id&0x80000000 != 0:
		f.Flags = canbus.FlagExtended
		f.ID = canbus.ID(id & 0x1FFFFFFF)
	default:
		f.ID = canbus.ID(id & 0x7FF)
	}
	n := int(b[4])
	fd := b[5]&fdFlagFDF != 0 || len(b) > 16 // old captures predate FDF
	if fd && f.Flags&canbus.FlagError == 0 {
		f.Flags |= canbus.FlagFD
		if b[5]&fdFlagBRS != 0 {
			f.Flags |= canbus.FlagBRS
		}
		if b[5]&fdFlagESI != 0 {
			f.Flags |= canbus.FlagESI
		}
		if n > canbus.MaxFDLen {
			return fmt.Errorf("CAN FD length %d", n)
		}
		n = canbus.RoundUpFDLen(n)
	} else {
		if n > canbus.MaxClassicalLen {
			return fmt.Errorf("classical length %d", n)
		}
		if id&0x40000000 != 0 && f.Flags&canbus.FlagError == 0 {
			f.Flags |= canbus.FlagRemote
			f.Len = uint8(n)
			return nil
		}
		if n == 8 && b[7] > 8 && b[7] <= 15 {
			f.Len8DLC = b[7]
		}
	}
	f.Len = uint8(n)
	avail := len(b) - 8
	copy(f.Data[:n], b[8:8+min(n, avail)]) // bytes beyond the capture length stay zero
	return nil
}

// PCAPReader reads pcap files of link type LINKTYPE_CAN_SOCKETCAN.
type PCAPReader struct {
	r      io.Reader
	order  binary.ByteOrder
	nano   bool
	record int
	hdr    [pcapRecordLen]byte
	buf    []byte
}

// NewPCAPReader reads the file header and returns a reader.
func NewPCAPReader(r io.Reader) (*PCAPReader, error) {
	var h [pcapHeaderLen]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, &ParseError{Format: FormatPCAP, Line: 0, Msg: "short file header"}
	}
	pr := &PCAPReader{r: r, buf: make([]byte, 0, 256)}
	switch {
	case binary.LittleEndian.Uint32(h[:]) == pcapMagicMicro:
		pr.order = binary.LittleEndian
	case binary.LittleEndian.Uint32(h[:]) == pcapMagicNano:
		pr.order, pr.nano = binary.LittleEndian, true
	case binary.BigEndian.Uint32(h[:]) == pcapMagicMicro:
		pr.order = binary.BigEndian
	case binary.BigEndian.Uint32(h[:]) == pcapMagicNano:
		pr.order, pr.nano = binary.BigEndian, true
	default:
		return nil, &ParseError{Format: FormatPCAP, Msg: "not a pcap file"}
	}
	if lt := pr.order.Uint32(h[20:]) & 0x0FFFFFFF; lt != LinkTypeCANSocketCAN {
		return nil, &ParseError{Format: FormatPCAP, Msg: fmt.Sprintf("link type %d is not LINKTYPE_CAN_SOCKETCAN (227)", lt)}
	}
	return pr, nil
}

// Read reads the next frame.
func (r *PCAPReader) Read(f *canbus.Frame) error {
	if _, err := io.ReadFull(r.r, r.hdr[:]); err != nil {
		if err == io.EOF {
			return io.EOF
		}
		return &ParseError{Format: FormatPCAP, Line: r.record + 1, Msg: "truncated record header"}
	}
	r.record++
	incl := r.order.Uint32(r.hdr[8:])
	if incl > 1<<16 {
		return &ParseError{Format: FormatPCAP, Line: r.record, Msg: fmt.Sprintf("record of %d bytes", incl)}
	}
	r.buf = r.buf[:0]
	r.buf = append(r.buf, make([]byte, incl)...)
	if _, err := io.ReadFull(r.r, r.buf); err != nil {
		return &ParseError{Format: FormatPCAP, Line: r.record, Msg: "truncated record"}
	}
	if err := decodeSocketCAN(r.buf, f); err != nil {
		return &ParseError{Format: FormatPCAP, Line: r.record, Msg: err.Error()}
	}
	sec, frac := int64(r.order.Uint32(r.hdr[0:])), int64(r.order.Uint32(r.hdr[4:]))
	if !r.nano {
		frac *= 1000
	}
	f.Timestamp = time.Unix(sec, frac)
	return nil
}

// PCAPWriter writes pcap files (nanosecond timestamps, little-endian,
// LINKTYPE_CAN_SOCKETCAN), readable by Wireshark and tcpdump.
type PCAPWriter struct {
	w   *bufio.Writer
	buf [pcapRecordLen + pcapSnapLen]byte
}

// NewPCAPWriter writes the file header and returns a writer.
func NewPCAPWriter(w io.Writer) (*PCAPWriter, error) {
	pw := &PCAPWriter{w: bufio.NewWriter(w)}
	var h [pcapHeaderLen]byte
	binary.LittleEndian.PutUint32(h[0:], pcapMagicNano)
	binary.LittleEndian.PutUint16(h[4:], 2)
	binary.LittleEndian.PutUint16(h[6:], 4)
	binary.LittleEndian.PutUint32(h[16:], pcapSnapLen)
	binary.LittleEndian.PutUint32(h[20:], LinkTypeCANSocketCAN)
	if _, err := pw.w.Write(h[:]); err != nil {
		return nil, err
	}
	return pw, nil
}

// Write writes one frame. The interface name and Echo mark are not
// represented in pcap.
func (w *PCAPWriter) Write(f *canbus.Frame) error {
	n := encodeSocketCAN(f, w.buf[pcapRecordLen:])
	var sec, ns int64
	if !f.Timestamp.IsZero() {
		sec, ns = f.Timestamp.Unix(), int64(f.Timestamp.Nanosecond())
	}
	binary.LittleEndian.PutUint32(w.buf[0:], uint32(sec))
	binary.LittleEndian.PutUint32(w.buf[4:], uint32(ns))
	binary.LittleEndian.PutUint32(w.buf[8:], uint32(n))
	binary.LittleEndian.PutUint32(w.buf[12:], uint32(n))
	_, err := w.w.Write(w.buf[:pcapRecordLen+n])
	return err
}

// Close flushes the output.
func (w *PCAPWriter) Close() error { return w.w.Flush() }

// PCAPNGReader reads pcapng files: frames of the interfaces of link type
// LINKTYPE_CAN_SOCKETCAN, with the interface name from the interface
// description block. Packets of other link types are skipped.
type PCAPNGReader struct {
	r      io.Reader
	order  binary.ByteOrder
	ifaces []pcapngIface
	block  int
	buf    []byte
}

type pcapngIface struct {
	linkType uint16
	name     string
	resol    uint64 // ticks per second
}

const (
	blockSHB = 0x0A0D0D0A
	blockIDB = 0x00000001
	blockSPB = 0x00000003
	blockEPB = 0x00000006
	bomMagic = 0x1A2B3C4D
)

// NewPCAPNGReader returns a reader. The section header is read with the
// first frame.
func NewPCAPNGReader(r io.Reader) (*PCAPNGReader, error) {
	return &PCAPNGReader{r: r}, nil
}

func (r *PCAPNGReader) fail(msg string, args ...any) error {
	return &ParseError{Format: FormatPCAPNG, Line: r.block, Msg: fmt.Sprintf(msg, args...)}
}

// Read reads the next frame.
func (r *PCAPNGReader) Read(f *canbus.Frame) error {
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r.r, hdr[:]); err != nil {
			if err == io.EOF {
				return io.EOF
			}
			return r.fail("truncated block header")
		}
		r.block++
		typ := binary.LittleEndian.Uint32(hdr[0:])
		if typ == blockSHB {
			// The byte order comes from the byte-order magic that follows.
			var bom [4]byte
			if _, err := io.ReadFull(r.r, bom[:]); err != nil {
				return r.fail("truncated section header")
			}
			switch {
			case binary.LittleEndian.Uint32(bom[:]) == bomMagic:
				r.order = binary.LittleEndian
			case binary.BigEndian.Uint32(bom[:]) == bomMagic:
				r.order = binary.BigEndian
			default:
				return r.fail("bad byte-order magic")
			}
			total := r.order.Uint32(hdr[4:])
			if total < 28 || total%4 != 0 || total > 1<<20 {
				return r.fail("bad section header length %d", total)
			}
			if _, err := io.CopyN(io.Discard, r.r, int64(total-12)); err != nil {
				return r.fail("truncated section header")
			}
			r.ifaces = r.ifaces[:0]
			continue
		}
		if r.order == nil {
			return r.fail("block before the section header")
		}
		typ = r.order.Uint32(hdr[0:])
		total := r.order.Uint32(hdr[4:])
		if total < 12 || total%4 != 0 || total > 1<<20 {
			return r.fail("bad block length %d", total)
		}
		r.buf = append(r.buf[:0], make([]byte, total-8)...)
		if _, err := io.ReadFull(r.r, r.buf); err != nil {
			return r.fail("truncated block")
		}
		body := r.buf[:len(r.buf)-4]
		switch typ {
		case blockIDB:
			if len(body) < 8 {
				return r.fail("short interface description")
			}
			ifc := pcapngIface{linkType: r.order.Uint16(body[0:]), resol: 1000000}
			r.parseIDBOptions(body[8:], &ifc)
			r.ifaces = append(r.ifaces, ifc)
		case blockEPB, blockSPB:
			var ifIdx, capLen uint32
			var data []byte
			var ts uint64
			if typ == blockEPB {
				if len(body) < 20 {
					return r.fail("short packet block")
				}
				ifIdx = r.order.Uint32(body[0:])
				ts = uint64(r.order.Uint32(body[4:]))<<32 | uint64(r.order.Uint32(body[8:]))
				capLen = r.order.Uint32(body[12:])
				data = body[20:]
			} else {
				if len(body) < 4 {
					return r.fail("short packet block")
				}
				capLen = r.order.Uint32(body[0:])
				data = body[4:]
			}
			if int(capLen) > len(data) {
				return r.fail("captured length %d exceeds the block", capLen)
			}
			if int(ifIdx) >= len(r.ifaces) {
				return r.fail("packet of undeclared interface %d", ifIdx)
			}
			ifc := r.ifaces[ifIdx]
			if ifc.linkType != LinkTypeCANSocketCAN {
				continue
			}
			if err := decodeSocketCAN(data[:capLen], f); err != nil {
				return r.fail("%v", err)
			}
			f.Interface = ifc.name
			if typ == blockEPB {
				sec := ts / ifc.resol
				frac := ts % ifc.resol
				f.Timestamp = time.Unix(int64(sec), int64(frac*1e9/ifc.resol))
			}
			return nil
		}
	}
}

func (r *PCAPNGReader) parseIDBOptions(b []byte, ifc *pcapngIface) {
	for len(b) >= 4 {
		code, l := r.order.Uint16(b[0:]), int(r.order.Uint16(b[2:]))
		if code == 0 || 4+l > len(b) {
			return
		}
		v := b[4 : 4+l]
		switch code {
		case 2: // if_name
			ifc.name = cstringOf(v)
		case 9: // if_tsresol
			if l >= 1 {
				e := uint64(v[0] & 0x7F)
				if e > 18 {
					break
				}
				res := uint64(1)
				for i := uint64(0); i < e; i++ {
					if v[0]&0x80 != 0 {
						res *= 2
					} else {
						res *= 10
					}
				}
				ifc.resol = res
			}
		}
		adv := 4 + (l+3)&^3
		if adv > len(b) {
			return
		}
		b = b[adv:]
	}
}

func cstringOf(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
