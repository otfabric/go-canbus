// SPDX-License-Identifier: MIT

package capture

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/otfabric/go-canbus"
)

// BLF is Vector's Binary Logging Format (*.blf): a file header followed by
// objects, usually inside zlib-compressed LOG_CONTAINER objects. The
// reader understands CAN_MESSAGE (1), CAN_MESSAGE2 (86), CAN_FD_MESSAGE
// (100), CAN_FD_MESSAGE_64 (101) and CAN_ERROR_EXT (73) objects and skips
// every other object type. The writer writes CAN_MESSAGE, CAN_FD_MESSAGE_64
// and CAN_ERROR_EXT objects in compressed containers, as python-can does.
//
// The channel number becomes the frame's Interface ("1", "2", ...).
// Layouts follow the public descriptions of the format (Vector's binlog
// headers as reproduced by python-can and others); they are checked against
// python-can in the interop suite.

const (
	blfFileSig  = "LOGG"
	blfObjSig   = "LOBJ"
	blfHdrSize  = 144
	blfObjBase  = 16
	blfObjV1    = 16
	blfObjV2    = 24
	blfContType = 10

	blfCANMessage   = 1
	blfCANErrorExt  = 73
	blfCANMessage2  = 86
	blfCANFDMessage = 100
	blfCANFD64      = 101

	blfFlagTenMicros = 1
	blfFlagOneNanos  = 2

	blfCompressNone = 0
	blfCompressZlib = 2

	blfContainerMax = 128 * 1024
	blfExtID        = 0x80000000
)

// blfSystemTime decodes a Windows SYSTEMTIME (8 little-endian uint16).
func blfSystemTime(b []byte) time.Time {
	u := func(i int) int { return int(binary.LittleEndian.Uint16(b[2*i:])) }
	if u(0) == 0 {
		return time.Time{}
	}
	return time.Date(u(0), time.Month(u(1)), u(3), u(4), u(5), u(6), u(7)*int(time.Millisecond), time.Local)
}

func blfPutSystemTime(b []byte, t time.Time) {
	vals := []int{t.Year(), int(t.Month()), int(t.Weekday()), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond() / int(time.Millisecond)}
	for i, v := range vals {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
}

// BLFReader reads BLF files, streaming through their containers.
type BLFReader struct {
	r      *bufio.Reader
	start  time.Time
	obj    int
	cur    *bytes.Reader // the decompressed container being read
	header [blfObjBase]byte
}

// NewBLFReader reads the file header and returns a reader.
func NewBLFReader(r io.Reader) (*BLFReader, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	h := make([]byte, blfHdrSize)
	if _, err := io.ReadFull(br, h[:8]); err != nil || string(h[:4]) != blfFileSig {
		return nil, &ParseError{Format: FormatBLF, Msg: "not a BLF file"}
	}
	size := int(binary.LittleEndian.Uint32(h[4:]))
	if size < 72 || size > 4096 {
		return nil, &ParseError{Format: FormatBLF, Msg: fmt.Sprintf("header size %d", size)}
	}
	rest := make([]byte, size-8)
	if _, err := io.ReadFull(br, rest); err != nil {
		return nil, &ParseError{Format: FormatBLF, Msg: "short file header"}
	}
	// signature, size, application (4 bytes), bin log version (4 bytes),
	// file size, uncompressed size, object count, objects read, then the
	// start and stop SYSTEMTIMEs.
	return &BLFReader{r: br, start: blfSystemTime(rest[40-8 : 56-8])}, nil
}

func (r *BLFReader) fail(format string, args ...any) error {
	return &ParseError{Format: FormatBLF, Line: r.obj, Msg: fmt.Sprintf(format, args...)}
}

// next returns the next object (base header type and the bytes after the
// base header) from the current container or the file.
func (r *BLFReader) next() (uint32, []byte, error) {
	for {
		src := io.Reader(r.r)
		inContainer := r.cur != nil && r.cur.Len() > 0
		if inContainer {
			src = r.cur
		}
		if _, err := io.ReadFull(src, r.header[:]); err != nil {
			if !inContainer && err == io.EOF {
				return 0, nil, io.EOF
			}
			if inContainer {
				return 0, nil, r.fail("truncated object header in container")
			}
			return 0, nil, r.fail("truncated object header")
		}
		r.obj++
		h := r.header[:]
		if string(h[:4]) != blfObjSig {
			return 0, nil, r.fail("bad object signature %q", h[:4])
		}
		size := int(binary.LittleEndian.Uint32(h[8:]))
		typ := binary.LittleEndian.Uint32(h[12:])
		if size < blfObjBase || size > 16<<20 {
			return 0, nil, r.fail("object size %d", size)
		}
		body := make([]byte, size-blfObjBase)
		if _, err := io.ReadFull(src, body); err != nil {
			return 0, nil, r.fail("truncated object")
		}
		// Each object is followed by size%4 padding bytes (the binlog
		// convention python-can follows; not "to the next multiple of 4").
		if pad := size % 4; pad != 0 {
			skip := make([]byte, pad)
			if inContainer {
				_, _ = io.ReadFull(src, skip)
			} else if _, err := io.ReadFull(src, skip); err != nil && err != io.EOF {
				return 0, nil, r.fail("truncated padding")
			}
		}
		if typ != blfContType {
			return typ, body, nil
		}
		// LOG_CONTAINER: compression (u16), reserved (6), uncompressed size (u32), reserved (4), data.
		if len(body) < 16 {
			return 0, nil, r.fail("short container")
		}
		method := binary.LittleEndian.Uint16(body[0:])
		usize := int(binary.LittleEndian.Uint32(body[8:]))
		data := body[16:]
		switch method {
		case blfCompressNone:
		case blfCompressZlib:
			if usize > 16<<20 {
				return 0, nil, r.fail("container of %d bytes", usize)
			}
			zr, err := zlib.NewReader(bytes.NewReader(data))
			if err != nil {
				return 0, nil, r.fail("container: %v", err)
			}
			out, err := io.ReadAll(io.LimitReader(zr, int64(usize)+1))
			if err != nil || len(out) != usize {
				return 0, nil, r.fail("container: decompressed %d of %d bytes (%v)", len(out), usize, err)
			}
			data = out
		default:
			return 0, nil, r.fail("compression method %d", method)
		}
		// Bytes left from the previous container continue an object split
		// across containers.
		var prev []byte
		if r.cur != nil {
			prev, _ = io.ReadAll(r.cur)
		}
		r.cur = bytes.NewReader(append(prev, data...))
	}
}

// Read reads the next frame.
func (r *BLFReader) Read(f *canbus.Frame) error {
	for {
		typ, body, err := r.next()
		if err != nil {
			return err
		}
		switch typ {
		case blfCANMessage, blfCANMessage2, blfCANFDMessage, blfCANFD64, blfCANErrorExt:
		default:
			continue
		}
		hv := binary.LittleEndian.Uint16(r.header[6:])
		var hlen int
		var flags uint32
		var ts uint64
		switch hv {
		case 1:
			if len(body) < blfObjV1 {
				return r.fail("short object header")
			}
			flags = binary.LittleEndian.Uint32(body[0:])
			ts = binary.LittleEndian.Uint64(body[8:])
			hlen = blfObjV1
		case 2:
			if len(body) < blfObjV2 {
				return r.fail("short object header")
			}
			flags = binary.LittleEndian.Uint32(body[0:])
			ts = binary.LittleEndian.Uint64(body[8:])
			hlen = blfObjV2
		default:
			return r.fail("object header version %d", hv)
		}
		d := time.Duration(ts) * 10 * time.Microsecond
		if flags&blfFlagOneNanos != 0 {
			d = time.Duration(ts)
		}
		*f = canbus.Frame{}
		start := r.start
		if start.IsZero() {
			start = time.Unix(0, 0)
		}
		f.Timestamp = start.Add(d)
		if err := r.decode(typ, body[hlen:], f); err != nil {
			return r.fail("%v", err)
		}
		return nil
	}
}

func blfID(raw uint32, f *canbus.Frame) {
	if raw&blfExtID != 0 {
		f.Flags |= canbus.FlagExtended
		f.ID = canbus.ID(raw & 0x1FFFFFFF)
	} else {
		f.ID = canbus.ID(raw & 0x7FF)
	}
}

func (r *BLFReader) decode(typ uint32, b []byte, f *canbus.Frame) error {
	le := binary.LittleEndian
	switch typ {
	case blfCANMessage, blfCANMessage2:
		// channel u16, flags u8, dlc u8, id u32, data [8]
		if len(b) < 16 {
			return fmt.Errorf("short CAN message")
		}
		f.Interface = fmt.Sprint(le.Uint16(b[0:]))
		fl, dlc := b[2], b[3]&0x0F
		blfID(le.Uint32(b[4:]), f)
		f.Echo = fl&0x01 != 0
		if fl&0x80 != 0 {
			f.Flags |= canbus.FlagRemote
			f.Len = min(dlc, 8)
			return nil
		}
		n := canbus.DLCToLen(dlc, false)
		f.Len = uint8(n)
		copy(f.Data[:n], b[8:8+n])
		if dlc > 8 {
			f.Len8DLC = dlc
		}
	case blfCANFDMessage:
		// channel u16, flags u8, dlc u8, id u32, frame length u32, bit
		// count u8, fd flags u8, valid bytes u8, reserved [5], data [64]
		if len(b) < 20 {
			return fmt.Errorf("short CAN FD message")
		}
		f.Interface = fmt.Sprint(le.Uint16(b[0:]))
		fl, dlc := b[2], b[3]&0x0F
		blfID(le.Uint32(b[4:]), f)
		f.Echo = fl&0x01 != 0
		fdf := b[13]
		valid := int(b[14])
		data := b[20:]
		if fdf&0x01 != 0 { // EDL
			f.Flags |= canbus.FlagFD
			if fdf&0x02 != 0 {
				f.Flags |= canbus.FlagBRS
			}
			if fdf&0x04 != 0 {
				f.Flags |= canbus.FlagESI
			}
			n := canbus.DLCToLen(dlc, true)
			if valid < n || len(data) < n {
				return fmt.Errorf("CAN FD message with %d of %d bytes", min(valid, len(data)), n)
			}
			f.Len = uint8(n)
			copy(f.Data[:n], data[:n])
			return nil
		}
		if fl&0x80 != 0 {
			f.Flags |= canbus.FlagRemote
			f.Len = min(dlc, 8)
			return nil
		}
		n := canbus.DLCToLen(dlc, false)
		if len(data) < n {
			return fmt.Errorf("short CAN message")
		}
		f.Len = uint8(n)
		copy(f.Data[:n], data[:n])
	case blfCANFD64:
		// channel u8, dlc u8, valid bytes u8, tx count u8, id u32, frame
		// length u32, flags u32, btr arb u32, btr data u32, brs offset u32,
		// crc offset u32, bit count u16, dir u8, ext data offset u8, crc
		// u32, data
		if len(b) < 40 {
			return fmt.Errorf("short CAN FD 64 message")
		}
		f.Interface = fmt.Sprint(b[0])
		dlc := b[1] & 0x0F
		valid := int(b[2])
		blfID(le.Uint32(b[4:]), f)
		fl := le.Uint32(b[12:])
		f.Echo = b[34] == 1
		data := b[40:]
		if fl&0x0010 != 0 { // remote frame
			f.Flags |= canbus.FlagRemote
			f.Len = min(dlc, 8)
			return nil
		}
		fd := fl&0x1000 != 0
		if fd {
			f.Flags |= canbus.FlagFD
			if fl&0x2000 != 0 {
				f.Flags |= canbus.FlagBRS
			}
			if fl&0x4000 != 0 {
				f.Flags |= canbus.FlagESI
			}
		}
		n := canbus.DLCToLen(dlc, fd)
		if valid < n || len(data) < n {
			return fmt.Errorf("CAN FD 64 message with %d of %d bytes", min(valid, len(data)), n)
		}
		f.Len = uint8(n)
		copy(f.Data[:n], data[:n])
	case blfCANErrorExt:
		// channel u16, length u16, flags u32, ecc u8, position u8, dlc u8,
		// reserved u8, frame length u32, id u32, flags ext u16, reserved
		// u16, data [8]
		if len(b) < 20 {
			return fmt.Errorf("short CAN error")
		}
		f.Interface = fmt.Sprint(le.Uint16(b[0:]))
		f.Flags = canbus.FlagError
		f.ID = canbus.ID(canbus.ErrClassProtocol) // BLF error objects carry no Linux class
		f.Len = 8
	}
	return nil
}

// BLFWriter writes BLF files.
type BLFWriter struct {
	w       io.WriteSeeker
	out     io.Writer
	buf     bytes.Buffer // objects of the current container
	start   time.Time
	last    time.Time
	started bool
	count   uint32
	size    uint64 // bytes written after the header
	usize   uint64 // uncompressed size of all objects
}

// NewBLFWriter returns a BLF writer. When w is an io.WriteSeeker (a file),
// Close rewrites the file header with the final sizes, object count and
// stop time; otherwise the header carries zeros there, which readers
// accept.
func NewBLFWriter(w io.Writer) (*BLFWriter, error) {
	bw := &BLFWriter{out: w}
	if ws, ok := w.(io.WriteSeeker); ok {
		bw.w = ws
	}
	return bw, nil
}

func (w *BLFWriter) fileHeader(stop time.Time) []byte {
	h := make([]byte, blfHdrSize)
	copy(h[0:], blfFileSig)
	binary.LittleEndian.PutUint32(h[4:], blfHdrSize)
	h[8] = 5 // application: "other" (as python-can)
	h[9], h[10], h[11] = 0, 1, 0
	h[12], h[13], h[14], h[15] = 4, 6, 1, 0 // binary log version
	binary.LittleEndian.PutUint64(h[16:], blfHdrSize+w.size)
	binary.LittleEndian.PutUint64(h[24:], blfHdrSize+w.usize)
	binary.LittleEndian.PutUint32(h[32:], w.count)
	blfPutSystemTime(h[40:], w.start)
	blfPutSystemTime(h[56:], stop)
	return h
}

func (w *BLFWriter) object(typ uint32, ts time.Time, body []byte) {
	le := binary.LittleEndian
	size := blfObjBase + blfObjV1 + len(body)
	var h [blfObjBase + blfObjV1]byte
	copy(h[0:], blfObjSig)
	le.PutUint16(h[4:], blfObjBase+blfObjV1)
	le.PutUint16(h[6:], 1)
	le.PutUint32(h[8:], uint32(size))
	le.PutUint32(h[12:], typ)
	le.PutUint32(h[16:], blfFlagOneNanos)
	var rel time.Duration
	if !ts.IsZero() {
		rel = ts.Sub(w.start)
		if rel < 0 {
			rel = 0
		}
		w.last = ts
	}
	le.PutUint64(h[24:], uint64(rel))
	w.buf.Write(h[:])
	w.buf.Write(body)
	for i := 0; i < size%4; i++ { // binlog padding: size%4 bytes
		w.buf.WriteByte(0)
	}
	w.count++
}

// Write writes one frame.
func (w *BLFWriter) Write(f *canbus.Frame) error {
	le := binary.LittleEndian
	if !w.started {
		w.started = true
		w.start = f.Timestamp
		if w.start.IsZero() {
			w.start = time.Unix(0, 0)
		}
		w.start = w.start.Truncate(time.Millisecond) // SYSTEMTIME has milliseconds
		if _, err := w.out.Write(w.fileHeader(time.Time{})); err != nil {
			return err
		}
	}
	ch := uint16(1)
	if n, err := fmt.Sscan(f.Interface, &ch); n != 1 || err != nil || ch == 0 {
		ch = 1
	}
	id := uint32(f.ID)
	if f.IsExtended() {
		id |= blfExtID
	}
	switch {
	case f.IsError():
		b := make([]byte, 32)
		le.PutUint16(b[0:], ch)
		le.PutUint16(b[2:], 0)
		b[10] = 8
		w.object(blfCANErrorExt, f.Timestamp, b)
	case f.IsFD():
		b := make([]byte, 40+64)
		b[0] = byte(ch)
		b[1] = f.DLC()
		b[2] = f.Len
		le.PutUint32(b[4:], id)
		fl := uint32(0x1000)
		if f.Flags&canbus.FlagBRS != 0 {
			fl |= 0x2000
		}
		if f.Flags&canbus.FlagESI != 0 {
			fl |= 0x4000
		}
		le.PutUint32(b[12:], fl)
		if f.Echo {
			b[34] = 1
		}
		copy(b[40:], f.Payload())
		w.object(blfCANFD64, f.Timestamp, b[:40+int(f.Len)])
	default:
		b := make([]byte, 16)
		le.PutUint16(b[0:], ch)
		if f.Echo {
			b[2] |= 0x01
		}
		if f.IsRemote() {
			b[2] |= 0x80
		}
		b[3] = f.DLC()
		le.PutUint32(b[4:], id)
		copy(b[8:], f.Payload())
		w.object(blfCANMessage, f.Timestamp, b)
	}
	if w.buf.Len() >= blfContainerMax {
		return w.flush()
	}
	return nil
}

// flush writes the buffered objects as one compressed container.
func (w *BLFWriter) flush() error {
	if w.buf.Len() == 0 {
		return nil
	}
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.DefaultCompression)
	if _, err := zw.Write(w.buf.Bytes()); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	le := binary.LittleEndian
	size := blfObjBase + 16 + z.Len()
	h := make([]byte, blfObjBase+16)
	copy(h[0:], blfObjSig)
	le.PutUint16(h[4:], blfObjBase)
	le.PutUint16(h[6:], 1)
	le.PutUint32(h[8:], uint32(size))
	le.PutUint32(h[12:], blfContType)
	le.PutUint16(h[16:], blfCompressZlib)
	le.PutUint32(h[24:], uint32(w.buf.Len()))
	pad := size % 4
	out := append(h, z.Bytes()...)
	out = append(out, make([]byte, pad)...)
	if _, err := w.out.Write(out); err != nil {
		return err
	}
	w.size += uint64(len(out))
	w.usize += uint64(blfObjBase + 16 + w.buf.Len())
	w.buf.Reset()
	return nil
}

// Close writes the last container and, on a seekable writer, the final
// header. It does not close the underlying writer.
func (w *BLFWriter) Close() error {
	if !w.started {
		w.started = true
		w.start = time.Unix(0, 0)
		if _, err := w.out.Write(w.fileHeader(time.Time{})); err != nil {
			return err
		}
	}
	if err := w.flush(); err != nil {
		return err
	}
	if w.w == nil {
		return nil
	}
	end, err := w.w.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil //nolint:nilerr // not seekable after all: the header keeps zeros
	}
	stop := w.last
	if stop.IsZero() {
		stop = w.start
	}
	if _, err := w.w.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := w.w.Write(w.fileHeader(stop)); err != nil {
		return err
	}
	_, err = w.w.Seek(end, io.SeekStart)
	return err
}
