// SPDX-License-Identifier: MIT

package socketcan

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-canbus"
)

// Sizes of struct can_frame and struct canfd_frame (CAN_MTU, CANFD_MTU),
// the header of struct canxl_frame (CANXL_HDR_SIZE) and the interface
// MTUs of CAN XL devices (CANXL_MIN_MTU .. CANXL_MTU).
const (
	canMTU      = 16
	canFDMTU    = 72
	canXLHdr    = 12
	canXLMinMTU = canXLHdr + 64
	canXLMTU    = canXLHdr + canbus.MaxXLLen
)

// canxl_frame bits.
const (
	xlXLF       = 0x80
	xlPrioMask  = 0x7FF
	xlVCIDShift = 16
)

// can_id flag bits and canfd_frame.flags bits.
const (
	effFlag = 0x80000000
	rtrFlag = 0x40000000
	errFlag = 0x20000000
	effMask = 0x1FFFFFFF
	sffMask = 0x000007FF
	errMask = 0x1FFFFFFF

	fdBRS = 0x01
	fdESI = 0x02
	fdFDF = 0x04
)

// marshal writes f as a struct can_frame or canfd_frame in host byte order
// and returns its size.
func marshal(f *canbus.Frame, buf *[canFDMTU]byte) int {
	*buf = [canFDMTU]byte{}
	id := canbus.KernelID(f)
	binary.NativeEndian.PutUint32(buf[0:4], id)
	buf[4] = f.Len
	if f.Flags&canbus.FlagFD != 0 {
		fl := byte(fdFDF)
		if f.Flags&canbus.FlagBRS != 0 {
			fl |= fdBRS
		}
		if f.Flags&canbus.FlagESI != 0 {
			fl |= fdESI
		}
		buf[5] = fl
		copy(buf[8:], f.Payload())
		return canFDMTU
	}
	buf[7] = f.Len8DLC
	copy(buf[8:16], f.Payload())
	return canMTU
}

// unmarshal decodes n bytes of a struct can_frame (n = 16) or canfd_frame
// (n = 72) into f, leaving f's metadata fields alone.
func unmarshal(buf []byte, f *canbus.Frame) error {
	n := len(buf)
	if n != canMTU && n != canFDMTU {
		return fmt.Errorf("%w: kernel frame of %d bytes", canbus.ErrInvalidFrame, n)
	}
	id := binary.NativeEndian.Uint32(buf[0:4])
	f.Flags = 0
	f.Len8DLC = 0
	switch {
	case id&errFlag != 0:
		f.Flags = canbus.FlagError
		f.ID = canbus.ID(id & errMask)
	case id&effFlag != 0:
		f.Flags = canbus.FlagExtended
		f.ID = canbus.ID(id & effMask)
	default:
		f.ID = canbus.ID(id & sffMask)
	}
	if id&rtrFlag != 0 && id&errFlag == 0 {
		f.Flags |= canbus.FlagRemote
	}
	l := buf[4]
	max := 8
	if n == canFDMTU {
		max = 64
		f.Flags |= canbus.FlagFD
		f.Flags &^= canbus.FlagRemote // FD frames have no RTR
		if buf[5]&fdBRS != 0 {
			f.Flags |= canbus.FlagBRS
		}
		if buf[5]&fdESI != 0 {
			f.Flags |= canbus.FlagESI
		}
	} else if f.Flags&canbus.FlagError == 0 && l == 8 && buf[7] > 8 && buf[7] <= 15 {
		f.Len8DLC = buf[7]
	}
	if int(l) > max {
		return fmt.Errorf("%w: kernel frame length %d", canbus.ErrInvalidFrame, l)
	}
	if n == canFDMTU && !canbus.ValidFDLen(int(l)) {
		// The kernel rounds lengths up when sending; a raw socket may
		// still deliver what another socket wrote. Keep the data and
		// round up, as the bus would have transmitted it.
		l = uint8(canbus.RoundUpFDLen(int(l)))
	}
	f.Len = l
	f.Data = [canbus.MaxFDLen]byte{}
	if f.Flags&canbus.FlagRemote == 0 {
		copy(f.Data[:l], buf[8:8+int(l)])
	}
	return nil
}

// marshalXL writes f as a struct canxl_frame in host byte order and
// returns its size (header and payload: the kernel requires the exact
// length).
func marshalXL(f *canbus.XLFrame, buf []byte) int {
	clear(buf[:canXLHdr])
	binary.NativeEndian.PutUint32(buf[0:4], uint32(f.Priority)&xlPrioMask|uint32(f.VCID)<<xlVCIDShift)
	buf[4] = byte(f.Flags) | xlXLF
	buf[5] = f.SDT
	binary.NativeEndian.PutUint16(buf[6:8], uint16(len(f.Data)))
	binary.NativeEndian.PutUint32(buf[8:12], f.AF)
	return canXLHdr + copy(buf[canXLHdr:], f.Data)
}

// isXL reports whether a received buffer holds a struct canxl_frame: the
// XL format bit is set (it sits where can_frame keeps its length, which
// never exceeds 64) and the size is not that of a CAN or CAN FD frame.
func isXL(buf []byte) bool {
	return len(buf) >= canXLHdr+canbus.MinXLLen && buf[4]&xlXLF != 0
}

// unmarshalXL decodes a struct canxl_frame into f, reusing f.Data's
// storage, and leaves f's metadata fields alone.
func unmarshalXL(buf []byte, f *canbus.XLFrame) error {
	if !isXL(buf) {
		return fmt.Errorf("%w: kernel frame of %d bytes is not CAN XL", canbus.ErrInvalidFrame, len(buf))
	}
	l := int(binary.NativeEndian.Uint16(buf[6:8]))
	if l < canbus.MinXLLen || l > canbus.MaxXLLen || canXLHdr+l != len(buf) {
		return fmt.Errorf("%w: CAN XL length %d in a %d-byte frame", canbus.ErrInvalidFrame, l, len(buf))
	}
	prio := binary.NativeEndian.Uint32(buf[0:4])
	f.Priority = uint16(prio & xlPrioMask)
	f.VCID = uint8(prio >> xlVCIDShift)
	f.Flags = canbus.XLFlags(buf[4] &^ xlXLF)
	f.SDT = buf[5]
	f.AF = binary.NativeEndian.Uint32(buf[8:12])
	f.Data = append(f.Data[:0], buf[canXLHdr:]...)
	return nil
}

// kernelFilters encodes a filter set as an array of struct can_filter.
func kernelFilters(fs canbus.FilterSet) []byte {
	if len(fs.Filters) == 0 {
		return make([]byte, 8) // one filter 0/0: everything
	}
	b := make([]byte, 8*len(fs.Filters))
	for i, f := range fs.Filters {
		id, mask := f.Kernel()
		binary.NativeEndian.PutUint32(b[8*i:], id)
		binary.NativeEndian.PutUint32(b[8*i+4:], mask)
	}
	return b
}
