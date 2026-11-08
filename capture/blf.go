// SPDX-License-Identifier: MIT

package capture

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math"
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
// The channel number becomes the frame's Interface ("1", "2", ...); the
// writer numbers channels as described at [vectorChannel].
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

	// blfObjectMax bounds an object's announced size and a container's
	// uncompressed size: the reader never holds more than an incomplete
	// object plus one container (32 MiB), whatever the file announces.
	blfObjectMax = 16 << 20
)

// blfSystemTime decodes a Windows SYSTEMTIME (8 little-endian uint16).
func blfSystemTime(b []byte) time.Time {
	u := func(i int) int { return int(binary.LittleEndian.Uint16(b[2*i:])) }
	if u(0) == 0 {
		return time.Time{}
	}
	return time.Date(u(0), time.Month(u(1)), u(3), u(4), u(5), u(6), u(7)*int(time.Millisecond), time.Local)
}

// blfPutSystemTime encodes t as a SYSTEMTIME, or zeros for the zero time.
// A SYSTEMTIME holds the years 1601 to 30827: others are an
// [ErrUnrepresentable] error.
func blfPutSystemTime(b []byte, t time.Time) error {
	if t.IsZero() {
		clear(b[:16])
		return nil
	}
	if y := t.Year(); y < 1601 || y > 30827 {
		return fmt.Errorf("%w: BLF time %v outside the SYSTEMTIME years 1601..30827", ErrUnrepresentable, t)
	}
	vals := []int{t.Year(), int(t.Month()), int(t.Weekday()), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond() / int(time.Millisecond)}
	for i, v := range vals {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v&0xFFFF)) // each field is at most 30827
	}
	return nil
}

// BLFReader reads BLF files, streaming through their containers. Objects
// split across containers (python-can splits at 128 KiB) are joined.
type BLFReader struct {
	r      *bufio.Reader
	start  time.Time
	obj    int
	cont   []byte       // unread bytes of the decompressed containers
	skip   int          // padding of the last object still to skip in cont
	store  []byte       // backing array of cont
	raw    bytes.Buffer // body of the last object read from the file
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
// base header, valid until the next call) from the containers or the
// file. Containers inside containers are returned like any other object
// (and skipped by Read).
func (r *BLFReader) next() (uint32, []byte, error) {
	for {
		if k := min(r.skip, len(r.cont)); k > 0 {
			r.cont, r.skip = r.cont[k:], r.skip-k
		}
		if r.skip == 0 && len(r.cont) >= blfObjBase {
			h := r.cont[:blfObjBase]
			size, typ, err := r.objectHeader(h)
			if err != nil {
				return 0, nil, err
			}
			if len(r.cont) >= size {
				r.obj++
				copy(r.header[:], h)
				body := r.cont[blfObjBase:size]
				// Each object is followed by size%4 padding bytes (the
				// binlog convention python-can follows; not "to the next
				// multiple of 4"); they may be in the next container.
				r.cont, r.skip = r.cont[size:], size%4
				return typ, body, nil
			}
			// The object continues in the next container.
		}
		typ, body, err := r.fileObject()
		if err != nil {
			return 0, nil, err
		}
		if typ != blfContType {
			return typ, body, nil
		}
		if err := r.container(body); err != nil {
			return 0, nil, err
		}
	}
}

// objectHeader checks a base object header and returns the object's size
// and type.
func (r *BLFReader) objectHeader(h []byte) (int, uint32, error) {
	if string(h[:4]) != blfObjSig {
		return 0, 0, r.fail("bad object signature %q", h[:4])
	}
	size := binary.LittleEndian.Uint32(h[8:])
	if size < blfObjBase || size > blfObjectMax {
		return 0, 0, r.fail("object size %d", size)
	}
	return int(size), binary.LittleEndian.Uint32(h[12:]), nil
}

// fileObject reads the next object of the file.
func (r *BLFReader) fileObject() (uint32, []byte, error) {
	if _, err := io.ReadFull(r.r, r.header[:]); err != nil {
		switch {
		case err == io.EOF && len(r.cont) > 0:
			return 0, nil, r.fail("truncated object header in container")
		case err == io.EOF:
			return 0, nil, io.EOF
		}
		return 0, nil, r.fail("truncated object header")
	}
	r.obj++
	size, typ, err := r.objectHeader(r.header[:])
	if err != nil {
		return 0, nil, err
	}
	// Read through a LimitReader so that a truncated file announcing a
	// large object costs no more memory than the file holds.
	r.raw.Reset()
	if n, _ := r.raw.ReadFrom(io.LimitReader(r.r, int64(size-blfObjBase))); n != int64(size-blfObjBase) {
		return 0, nil, r.fail("truncated object")
	}
	if pad := size % 4; pad != 0 {
		var skip [3]byte
		if _, err := io.ReadFull(r.r, skip[:pad]); err != nil && err != io.EOF {
			return 0, nil, r.fail("truncated padding")
		}
	}
	return typ, r.raw.Bytes(), nil
}

// container appends the uncompressed data of a LOG_CONTAINER body to the
// bytes left from the previous container.
func (r *BLFReader) container(body []byte) error {
	// compression (u16), reserved (6), uncompressed size (u32), reserved (4), data.
	if len(body) < 16 {
		return r.fail("short container")
	}
	method := binary.LittleEndian.Uint16(body[0:])
	usize := binary.LittleEndian.Uint32(body[8:])
	data := body[16:]
	var add int // bytes this container adds
	switch method {
	case blfCompressNone:
		add = len(data)
	case blfCompressZlib:
		if usize > blfObjectMax {
			return r.fail("container of %d bytes", usize)
		}
		add = int(usize)
	default:
		return r.fail("compression method %d", method)
	}
	// Keep the bytes left from the previous container (an incomplete
	// object, less than blfObjectMax) and make room for this one's.
	keep, need := len(r.cont), len(r.cont)+add
	if cap(r.store) < need {
		grown := make([]byte, keep, need)
		copy(grown, r.cont)
		r.store = grown
	} else {
		r.store = append(r.store[:0], r.cont...) // overlapping copy is a memmove
	}
	r.cont, r.store = r.store[:keep], r.store[:need]
	if method == blfCompressNone {
		copy(r.store[keep:], data)
		r.cont = r.store
		return nil
	}
	zr, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return r.fail("container: %v", err)
	}
	// Exactly usize bytes are inflated: a stream that holds more is an
	// error, however much more it holds.
	if n, err := io.ReadFull(zr, r.store[keep:]); err != nil {
		return r.fail("container: decompressed %d of %d bytes (%v)", n, usize, err)
	}
	var more [1]byte
	if n, _ := io.ReadFull(zr, more[:]); n != 0 {
		return r.fail("container: more than the %d bytes announced", usize)
	}
	r.cont = r.store
	return nil
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
		unit, limit := 10*time.Microsecond, uint64(math.MaxInt64/int64(10*time.Microsecond))
		if flags&blfFlagOneNanos != 0 {
			unit, limit = 1, math.MaxInt64
		}
		if ts > limit {
			return r.fail("timestamp %d out of range", ts)
		}
		d := time.Duration(ts&math.MaxInt64) * unit // ts <= limit: no overflow
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
		f.Len = lenByte(n)
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
			f.Len = lenByte(n)
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
		f.Len = lenByte(n)
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
		f.Len = lenByte(n)
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

func (w *BLFWriter) fileHeader(stop time.Time) ([]byte, error) {
	h := make([]byte, blfHdrSize)
	copy(h[0:], blfFileSig)
	binary.LittleEndian.PutUint32(h[4:], blfHdrSize)
	h[8] = 5 // application: "other" (as python-can)
	h[9], h[10], h[11] = 0, 1, 0
	h[12], h[13], h[14], h[15] = 4, 6, 1, 0 // binary log version
	binary.LittleEndian.PutUint64(h[16:], blfHdrSize+w.size)
	binary.LittleEndian.PutUint64(h[24:], blfHdrSize+w.usize)
	binary.LittleEndian.PutUint32(h[32:], w.count)
	if err := blfPutSystemTime(h[40:], w.start); err != nil {
		return nil, err
	}
	if err := blfPutSystemTime(h[56:], stop); err != nil {
		return nil, err
	}
	return h, nil
}

// object buffers an object with a v1 header. Its timestamp is in
// nanoseconds after the start (frames before the start are written at the
// start); more than 292 years after it is an [ErrUnrepresentable] error.
func (w *BLFWriter) object(typ uint32, ts time.Time, body []byte) error {
	le := binary.LittleEndian
	var rel time.Duration
	if !ts.IsZero() {
		rel = ts.Sub(w.start)
		if rel == math.MaxInt64 { // Sub saturates
			return fmt.Errorf("%w: BLF timestamp %v more than 292 years after the start %v", ErrUnrepresentable, ts, w.start)
		}
		rel = max(rel, 0)
	}
	size := blfObjBase + blfObjV1 + len(body)
	var h [blfObjBase + blfObjV1]byte
	copy(h[0:], blfObjSig)
	le.PutUint16(h[4:], blfObjBase+blfObjV1)
	le.PutUint16(h[6:], 1)
	le.PutUint32(h[8:], uint32(max(min(size, blfObjectMax), 0))) // bodies are at most 104 bytes
	le.PutUint32(h[12:], typ)
	le.PutUint32(h[16:], blfFlagOneNanos)
	if !ts.IsZero() {
		w.last = ts
	}
	le.PutUint64(h[24:], uint64(rel))
	w.buf.Write(h[:])
	w.buf.Write(body)
	for i := 0; i < size%4; i++ { // binlog padding: size%4 bytes
		w.buf.WriteByte(0)
	}
	w.count++
	return nil
}

// begin writes the provisional file header for a trace starting at start
// (the epoch when zero).
func (w *BLFWriter) begin(start time.Time) error {
	if start.IsZero() {
		start = time.Unix(0, 0)
	}
	w.start = start.Truncate(time.Millisecond) // SYSTEMTIME has milliseconds
	h, err := w.fileHeader(time.Time{})
	if err != nil {
		return err
	}
	w.started = true
	_, err = w.out.Write(h)
	return err
}

// Write writes one frame.
func (w *BLFWriter) Write(f *canbus.Frame) error {
	le := binary.LittleEndian
	if !w.started {
		if err := w.begin(f.Timestamp); err != nil {
			return err
		}
	}
	c := vectorChannel(f.Interface)
	if c < 1 || c > math.MaxUint16 {
		return fmt.Errorf("%w: BLF channel %d", ErrUnrepresentable, c)
	}
	ch := uint16(c)
	var err error
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
		err = w.object(blfCANErrorExt, f.Timestamp, b)
	case f.IsFD():
		if ch > math.MaxUint8 { // CAN_FD_MESSAGE_64 has an 8-bit channel
			return fmt.Errorf("%w: BLF CAN FD channel %d", ErrUnrepresentable, ch)
		}
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
		err = w.object(blfCANFD64, f.Timestamp, b[:40+len(f.Payload())])
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
		err = w.object(blfCANMessage, f.Timestamp, b)
	}
	if err != nil {
		return err
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
	// A container holds blfContainerMax bytes plus one object, compressed
	// to at most a little more: the clamps are no-ops.
	size := blfObjBase + 16 + z.Len()
	usize := uint32(max(min(w.buf.Len(), blfObjectMax), 0))
	h := make([]byte, blfObjBase+16)
	copy(h[0:], blfObjSig)
	le.PutUint16(h[4:], blfObjBase)
	le.PutUint16(h[6:], 1)
	le.PutUint32(h[8:], uint32(max(min(size, blfObjectMax), 0)))
	le.PutUint32(h[12:], blfContType)
	le.PutUint16(h[16:], blfCompressZlib)
	le.PutUint32(h[24:], usize)
	pad := size % 4
	out := append(h, z.Bytes()...)
	out = append(out, make([]byte, pad)...)
	if _, err := w.out.Write(out); err != nil {
		return err
	}
	w.size += uint64(uint32(max(min(len(out), blfObjectMax), 0)))
	w.usize += uint64(blfObjBase+16) + uint64(usize)
	w.buf.Reset()
	return nil
}

// Close writes the last container and, on a seekable writer, the final
// header. It does not close the underlying writer.
func (w *BLFWriter) Close() error {
	if !w.started {
		if err := w.begin(time.Time{}); err != nil {
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
	h, err := w.fileHeader(stop)
	if err != nil {
		return err
	}
	if _, err := w.w.Write(h); err != nil {
		return err
	}
	_, err = w.w.Seek(end, io.SeekStart)
	return err
}
