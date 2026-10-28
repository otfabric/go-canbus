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
	pcapSnapLen    = xlHeaderLen + canbus.MaxXLLen
	xlHeaderLen    = 12   // the CAN XL header of LINKTYPE_CAN_SOCKETCAN
	xlFormatFlag   = 0x80 // CANXL_XLF in the flags byte
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

// encodeSocketCANXL encodes the LINKTYPE_CAN_SOCKETCAN packet of a CAN
// XL frame, as libpcap writes it on Linux: the priority and VCID word in
// network byte order, the payload length and acceptance field
// little-endian.
func encodeSocketCANXL(x *canbus.XLFrame, b []byte) int {
	binary.BigEndian.PutUint32(b[0:], canbus.XLKernelID(x))
	b[4] = uint8(x.Flags) | xlFormatFlag
	b[5] = x.SDT
	binary.LittleEndian.PutUint16(b[6:], uint16(len(x.Data)))
	binary.LittleEndian.PutUint32(b[8:], x.AF)
	copy(b[xlHeaderLen:], x.Data)
	return xlHeaderLen + len(x.Data)
}

// isSocketCANXL reports whether a LINKTYPE_CAN_SOCKETCAN packet holds a
// CAN XL frame: the XL format bit is set where CAN and CAN FD frames have
// their length (at most 64).
func isSocketCANXL(b []byte) bool { return len(b) > 4 && b[4]&xlFormatFlag != 0 }

// decodeSocketCANXL decodes a CAN XL packet into x, reusing x.Data's
// storage. Payload bytes beyond the captured length are zero.
func decodeSocketCANXL(b []byte, x *canbus.XLFrame) error {
	if len(b) < xlHeaderLen {
		return fmt.Errorf("packet of %d bytes is shorter than the CAN XL header", len(b))
	}
	id := binary.BigEndian.Uint32(b[0:])
	if id&^0x00FF07FF != 0 {
		return fmt.Errorf("reserved bits in the CAN XL priority field %08X", id)
	}
	n := int(binary.LittleEndian.Uint16(b[6:]))
	if n < canbus.MinXLLen || n > canbus.MaxXLLen {
		return fmt.Errorf("CAN XL length %d", n)
	}
	data := x.Data[:0]
	if cap(data) < n {
		data = make([]byte, 0, n)
	}
	data = append(data, b[xlHeaderLen:min(len(b), xlHeaderLen+n)]...)
	for len(data) < n {
		data = append(data, 0)
	}
	*x = canbus.XLFrame{
		Priority: uint16(id & canbus.MaxXLPriority),
		VCID:     uint8(id >> 16),
		Flags:    canbus.XLFlags(b[4] &^ xlFormatFlag),
		SDT:      b[5],
		AF:       binary.LittleEndian.Uint32(b[8:]),
		Data:     data,
	}
	return x.Validate()
}

// decodeSocketCANAny decodes a packet into f, or into x (xl true) when it
// holds a CAN XL frame; with x nil a CAN XL frame is reported undecoded.
func decodeSocketCANAny(b []byte, f *canbus.Frame, x *canbus.XLFrame) (xl bool, err error) {
	if !isSocketCANXL(b) {
		return false, decodeSocketCAN(b, f)
	}
	if x == nil {
		return true, nil
	}
	return true, decodeSocketCANXL(b, x)
}

// decodeSocketCAN decodes a LINKTYPE_CAN_SOCKETCAN packet of a CAN or CAN
// FD frame.
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

// PCAPReader reads pcap files of link type LINKTYPE_CAN_SOCKETCAN. It
// implements [XLReader].
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

// Read reads the next CAN or CAN FD frame. CAN XL frames are skipped: use
// [PCAPReader.ReadAny] to get them.
func (r *PCAPReader) Read(f *canbus.Frame) error {
	for {
		xl, err := r.ReadAny(f, nil)
		if err != nil || !xl {
			return err
		}
	}
}

// ReadAny reads the next frame of any kind: a CAN or CAN FD frame into f
// (xl false) or a CAN XL frame into x (xl true, reusing x.Data's
// storage). With x nil a CAN XL frame is reported without being decoded.
func (r *PCAPReader) ReadAny(f *canbus.Frame, x *canbus.XLFrame) (xl bool, err error) {
	if _, err := io.ReadFull(r.r, r.hdr[:]); err != nil {
		if err == io.EOF {
			return false, io.EOF
		}
		return false, &ParseError{Format: FormatPCAP, Line: r.record + 1, Msg: "truncated record header"}
	}
	r.record++
	incl := r.order.Uint32(r.hdr[8:])
	if incl > 1<<16 {
		return false, &ParseError{Format: FormatPCAP, Line: r.record, Msg: fmt.Sprintf("record of %d bytes", incl)}
	}
	r.buf = r.buf[:0]
	r.buf = append(r.buf, make([]byte, incl)...)
	if _, err := io.ReadFull(r.r, r.buf); err != nil {
		return false, &ParseError{Format: FormatPCAP, Line: r.record, Msg: "truncated record"}
	}
	xl, err = decodeSocketCANAny(r.buf, f, x)
	if err != nil {
		return xl, &ParseError{Format: FormatPCAP, Line: r.record, Msg: err.Error()}
	}
	sec, frac := int64(r.order.Uint32(r.hdr[0:])), int64(r.order.Uint32(r.hdr[4:]))
	if !r.nano {
		frac *= 1000
	}
	switch {
	case !xl:
		f.Timestamp = time.Unix(sec, frac)
	case x != nil:
		x.Timestamp = time.Unix(sec, frac)
	}
	return xl, nil
}

// PCAPWriter writes pcap files (nanosecond timestamps, little-endian,
// LINKTYPE_CAN_SOCKETCAN), readable by Wireshark and tcpdump. It
// implements [XLWriter]: CAN XL frames are written as libpcap captures
// them on Linux.
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
	return w.record(encodeSocketCAN(f, w.buf[pcapRecordLen:]), f.Timestamp)
}

// WriteXL writes one CAN XL frame. The interface name and Echo mark are
// not represented in pcap.
func (w *PCAPWriter) WriteXL(x *canbus.XLFrame) error {
	if err := x.Validate(); err != nil {
		return err
	}
	return w.record(encodeSocketCANXL(x, w.buf[pcapRecordLen:]), x.Timestamp)
}

// record writes the record header of the n-byte packet in w.buf and the
// packet.
func (w *PCAPWriter) record(n int, ts time.Time) error {
	var sec, ns int64
	if !ts.IsZero() {
		sec, ns = ts.Unix(), int64(ts.Nanosecond())
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
// description block. Packets of other link types are skipped. It
// implements [XLReader].
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

// Read reads the next CAN or CAN FD frame. CAN XL frames are skipped: use
// [PCAPNGReader.ReadAny] to get them.
func (r *PCAPNGReader) Read(f *canbus.Frame) error {
	for {
		xl, err := r.ReadAny(f, nil)
		if err != nil || !xl {
			return err
		}
	}
}

// ReadAny reads the next frame of any kind: a CAN or CAN FD frame into f
// (xl false) or a CAN XL frame into x (xl true, reusing x.Data's
// storage). With x nil a CAN XL frame is reported without being decoded.
func (r *PCAPNGReader) ReadAny(f *canbus.Frame, x *canbus.XLFrame) (xl bool, err error) {
	data, ifc, ts, hasTS, err := r.next()
	if err != nil {
		return false, err
	}
	xl, err = decodeSocketCANAny(data, f, x)
	if err != nil {
		return xl, r.fail("%v", err)
	}
	var t time.Time
	if hasTS {
		sec := ts / ifc.resol
		frac := ts % ifc.resol
		t = time.Unix(int64(sec), int64(frac*1e9/ifc.resol))
	}
	switch {
	case !xl:
		f.Interface, f.Timestamp = ifc.name, t
	case x != nil:
		x.Interface, x.Timestamp = ifc.name, t
	}
	return xl, nil
}

// next returns the next packet of a LINKTYPE_CAN_SOCKETCAN interface, its
// interface and, from an enhanced packet block, its timestamp in the
// interface's ticks.
func (r *PCAPNGReader) next() (data []byte, ifc *pcapngIface, ts uint64, hasTS bool, err error) {
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r.r, hdr[:]); err != nil {
			if err == io.EOF {
				return nil, nil, 0, false, io.EOF
			}
			return nil, nil, 0, false, r.fail("truncated block header")
		}
		r.block++
		typ := binary.LittleEndian.Uint32(hdr[0:])
		if typ == blockSHB {
			// The byte order comes from the byte-order magic that follows.
			var bom [4]byte
			if _, err := io.ReadFull(r.r, bom[:]); err != nil {
				return nil, nil, 0, false, r.fail("truncated section header")
			}
			switch {
			case binary.LittleEndian.Uint32(bom[:]) == bomMagic:
				r.order = binary.LittleEndian
			case binary.BigEndian.Uint32(bom[:]) == bomMagic:
				r.order = binary.BigEndian
			default:
				return nil, nil, 0, false, r.fail("bad byte-order magic")
			}
			total := r.order.Uint32(hdr[4:])
			if total < 28 || total%4 != 0 || total > 1<<20 {
				return nil, nil, 0, false, r.fail("bad section header length %d", total)
			}
			if _, err := io.CopyN(io.Discard, r.r, int64(total-12)); err != nil {
				return nil, nil, 0, false, r.fail("truncated section header")
			}
			r.ifaces = r.ifaces[:0]
			continue
		}
		if r.order == nil {
			return nil, nil, 0, false, r.fail("block before the section header")
		}
		typ = r.order.Uint32(hdr[0:])
		total := r.order.Uint32(hdr[4:])
		if total < 12 || total%4 != 0 || total > 1<<20 {
			return nil, nil, 0, false, r.fail("bad block length %d", total)
		}
		r.buf = append(r.buf[:0], make([]byte, total-8)...)
		if _, err := io.ReadFull(r.r, r.buf); err != nil {
			return nil, nil, 0, false, r.fail("truncated block")
		}
		body := r.buf[:len(r.buf)-4]
		switch typ {
		case blockIDB:
			if len(body) < 8 {
				return nil, nil, 0, false, r.fail("short interface description")
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
					return nil, nil, 0, false, r.fail("short packet block")
				}
				ifIdx = r.order.Uint32(body[0:])
				ts = uint64(r.order.Uint32(body[4:]))<<32 | uint64(r.order.Uint32(body[8:]))
				capLen = r.order.Uint32(body[12:])
				data = body[20:]
			} else {
				if len(body) < 4 {
					return nil, nil, 0, false, r.fail("short packet block")
				}
				capLen = r.order.Uint32(body[0:])
				data = body[4:]
			}
			if int(capLen) > len(data) {
				return nil, nil, 0, false, r.fail("captured length %d exceeds the block", capLen)
			}
			if int(ifIdx) >= len(r.ifaces) {
				return nil, nil, 0, false, r.fail("packet of undeclared interface %d", ifIdx)
			}
			ifc := &r.ifaces[ifIdx]
			if ifc.linkType != LinkTypeCANSocketCAN {
				continue
			}
			return data[:capLen], ifc, ts, typ == blockEPB, nil
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

// PCAPNGWriter writes pcapng files, readable by Wireshark: one section
// with an interface of link type LINKTYPE_CAN_SOCKETCAN per interface
// name (nanosecond timestamps), and an enhanced packet block per frame.
// Unlike pcap, the frames keep their interface names. It implements
// [XLWriter].
type PCAPNGWriter struct {
	w      *bufio.Writer
	ifaces map[string]uint32
	buf    []byte
}

// NewPCAPNGWriter writes the section header and returns a writer.
func NewPCAPNGWriter(w io.Writer) (*PCAPNGWriter, error) {
	pw := &PCAPNGWriter{w: bufio.NewWriter(w), ifaces: map[string]uint32{}, buf: make([]byte, 0, 64+pcapSnapLen)}
	// Section header block: byte-order magic, version 1.0, unknown
	// section length, no options.
	b := pw.block(blockSHB, 16)
	binary.LittleEndian.PutUint32(b[8:], bomMagic)
	binary.LittleEndian.PutUint16(b[12:], 1)
	binary.LittleEndian.PutUint16(b[14:], 0)
	binary.LittleEndian.PutUint64(b[16:], ^uint64(0))
	if err := pw.flushBlock(); err != nil {
		return nil, err
	}
	return pw, nil
}

// block starts a block of the given type with a body of n bytes in w.buf
// and returns the buffer (header included).
func (w *PCAPNGWriter) block(typ uint32, n int) []byte {
	w.buf = append(w.buf[:0], make([]byte, 8+n)...)
	binary.LittleEndian.PutUint32(w.buf[0:], typ)
	return w.buf
}

// option appends a pcapng option, padded to 32 bits.
func (w *PCAPNGWriter) option(code uint16, v []byte) {
	var h [4]byte
	binary.LittleEndian.PutUint16(h[0:], code)
	binary.LittleEndian.PutUint16(h[2:], uint16(len(v)))
	w.buf = append(w.buf, h[:]...)
	w.buf = append(w.buf, v...)
	for len(w.buf)%4 != 0 {
		w.buf = append(w.buf, 0)
	}
}

// flushBlock completes the block in w.buf with its lengths and writes it.
func (w *PCAPNGWriter) flushBlock() error {
	for len(w.buf)%4 != 0 {
		w.buf = append(w.buf, 0)
	}
	total := uint32(len(w.buf) + 4)
	binary.LittleEndian.PutUint32(w.buf[4:], total)
	w.buf = binary.LittleEndian.AppendUint32(w.buf, total)
	_, err := w.w.Write(w.buf)
	return err
}

// iface returns the index of the interface named name, writing its
// interface description block first if it is new.
func (w *PCAPNGWriter) iface(name string) (uint32, error) {
	if i, ok := w.ifaces[name]; ok {
		return i, nil
	}
	b := w.block(blockIDB, 8)
	binary.LittleEndian.PutUint16(b[8:], LinkTypeCANSocketCAN)
	binary.LittleEndian.PutUint32(b[12:], pcapSnapLen)
	if name != "" {
		w.option(2, []byte(name)) // if_name
	}
	w.option(9, []byte{9}) // if_tsresol: nanoseconds
	w.option(0, nil)       // opt_endofopt
	if err := w.flushBlock(); err != nil {
		return 0, err
	}
	i := uint32(len(w.ifaces))
	w.ifaces[name] = i
	return i, nil
}

// packet writes an enhanced packet block; encode fills the packet bytes
// and returns their number.
func (w *PCAPNGWriter) packet(name string, ts time.Time, encode func([]byte) int) error {
	idx, err := w.iface(name)
	if err != nil {
		return err
	}
	b := w.block(blockEPB, 20+pcapSnapLen)
	n := encode(b[28:])
	var t uint64
	if !ts.IsZero() {
		t = uint64(ts.UnixNano())
	}
	binary.LittleEndian.PutUint32(b[8:], idx)
	binary.LittleEndian.PutUint32(b[12:], uint32(t>>32))
	binary.LittleEndian.PutUint32(b[16:], uint32(t))
	binary.LittleEndian.PutUint32(b[20:], uint32(n))
	binary.LittleEndian.PutUint32(b[24:], uint32(n))
	w.buf = w.buf[:28+n]
	return w.flushBlock()
}

// Write writes one frame. The Echo mark is not represented.
func (w *PCAPNGWriter) Write(f *canbus.Frame) error {
	return w.packet(f.Interface, f.Timestamp, func(b []byte) int { return encodeSocketCAN(f, b) })
}

// WriteXL writes one CAN XL frame. The Echo mark is not represented.
func (w *PCAPNGWriter) WriteXL(x *canbus.XLFrame) error {
	if err := x.Validate(); err != nil {
		return err
	}
	return w.packet(x.Interface, x.Timestamp, func(b []byte) int { return encodeSocketCANXL(x, b) })
}

// Close flushes the output.
func (w *PCAPNGWriter) Close() error { return w.w.Flush() }
