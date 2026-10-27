// SPDX-License-Identifier: MIT

package canbus

import (
	"strconv"
	"strings"
)

// ErrorClass is the class bit set of an error frame: the identifier of a
// frame with [FlagError], with the values of Linux's can/error.h. It is
// also the mask that selects which error frames a socket receives.
type ErrorClass uint32

// Error classes.
const (
	ErrClassTxTimeout       ErrorClass = 0x001 // TX timeout (by netdevice driver)
	ErrClassLostArbitration ErrorClass = 0x002 // lost arbitration; bit in Data[0]
	ErrClassController      ErrorClass = 0x004 // controller problems; Data[1]
	ErrClassProtocol        ErrorClass = 0x008 // protocol violation; Data[2], Data[3]
	ErrClassTransceiver     ErrorClass = 0x010 // transceiver status; Data[4]
	ErrClassNoAck           ErrorClass = 0x020 // no acknowledgement on transmission
	ErrClassBusOff          ErrorClass = 0x040 // bus off
	ErrClassBusError        ErrorClass = 0x080 // bus error (may flood)
	ErrClassRestarted       ErrorClass = 0x100 // controller restarted
	ErrClassCounters        ErrorClass = 0x200 // TX/RX error counters in Data[6], Data[7]

	// ErrClassAll selects every error class.
	ErrClassAll ErrorClass = 0x1FFFFFFF
)

var errClassNames = []string{"tx-timeout", "lost-arbitration", "controller", "protocol",
	"transceiver", "no-ack", "bus-off", "bus-error", "restarted", "counters"}

// String lists the classes, for example "controller|bus-off".
func (c ErrorClass) String() string {
	if c == 0 {
		return "none"
	}
	var parts []string
	for i, n := range errClassNames {
		if c&(1<<i) != 0 {
			parts = append(parts, n)
		}
	}
	if rest := c &^ (1<<len(errClassNames) - 1); rest != 0 {
		parts = append(parts, "0x"+strconv.FormatUint(uint64(rest), 16))
	}
	return strings.Join(parts, "|")
}

// ControllerStatus is Data[1] of a controller error frame.
type ControllerStatus uint8

// Controller status bits.
const (
	CtrlRxOverflow ControllerStatus = 0x01 // RX buffer overflow
	CtrlTxOverflow ControllerStatus = 0x02 // TX buffer overflow
	CtrlRxWarning  ControllerStatus = 0x04 // reached warning level for RX errors
	CtrlTxWarning  ControllerStatus = 0x08 // reached warning level for TX errors
	CtrlRxPassive  ControllerStatus = 0x10 // reached error passive status RX
	CtrlTxPassive  ControllerStatus = 0x20 // reached error passive status TX
	CtrlActive     ControllerStatus = 0x40 // recovered to error active state
)

var ctrlNames = []string{"rx-overflow", "tx-overflow", "rx-warning", "tx-warning", "rx-passive", "tx-passive", "active"}

// String lists the status bits.
func (s ControllerStatus) String() string { return bitNames(uint8(s), ctrlNames) }

// ProtocolViolation is Data[2] of a protocol error frame.
type ProtocolViolation uint8

// Protocol violation types.
const (
	ProtBit      ProtocolViolation = 0x01 // single bit error
	ProtForm     ProtocolViolation = 0x02 // frame format error
	ProtStuff    ProtocolViolation = 0x04 // bit stuffing error
	ProtBit0     ProtocolViolation = 0x08 // unable to send dominant bit
	ProtBit1     ProtocolViolation = 0x10 // unable to send recessive bit
	ProtOverload ProtocolViolation = 0x20 // bus overload
	ProtActive   ProtocolViolation = 0x40 // active error announcement
	ProtTx       ProtocolViolation = 0x80 // error occurred on transmission
)

var protNames = []string{"bit", "form", "stuff", "bit0", "bit1", "overload", "active", "tx"}

// String lists the violation bits.
func (p ProtocolViolation) String() string { return bitNames(uint8(p), protNames) }

func bitNames(v uint8, names []string) string {
	if v == 0 {
		return "unspecified"
	}
	var parts []string
	for i, n := range names {
		if v&(1<<i) != 0 {
			parts = append(parts, n)
		}
	}
	if rest := v &^ (1<<len(names) - 1); rest != 0 {
		parts = append(parts, "0x"+strconv.FormatUint(uint64(rest), 16))
	}
	return strings.Join(parts, "|")
}

// ErrorInfo is the decoded content of an error frame.
type ErrorInfo struct {
	Class ErrorClass
	// ArbitrationBit is the bit in which arbitration was lost (0 =
	// unspecified), for ErrClassLostArbitration.
	ArbitrationBit uint8
	// Controller is the controller status, for ErrClassController.
	Controller ControllerStatus
	// Protocol and ProtocolLocation describe a protocol violation, for
	// ErrClassProtocol. The location is the Linux CAN_ERR_PROT_LOC_*
	// code (for example 0x08 = data section).
	Protocol         ProtocolViolation
	ProtocolLocation uint8
	// Transceiver is the Linux CAN_ERR_TRX_* status, for
	// ErrClassTransceiver.
	Transceiver uint8
	// ControllerSpecific is Data[5], whose meaning depends on the driver.
	ControllerSpecific uint8
	// TxErrors and RxErrors are the error counters, valid when
	// CountersValid (ErrClassCounters) is set.
	TxErrors, RxErrors uint8
	CountersValid      bool
}

// ParseError decodes an error frame. It reports false for a frame without
// [FlagError].
func ParseError(f *Frame) (ErrorInfo, bool) {
	if f.Flags&FlagError == 0 {
		return ErrorInfo{}, false
	}
	d := &f.Data
	info := ErrorInfo{
		Class:              ErrorClass(f.ID) & ErrClassAll,
		ArbitrationBit:     d[0],
		Controller:         ControllerStatus(d[1]),
		Protocol:           ProtocolViolation(d[2]),
		ProtocolLocation:   d[3],
		Transceiver:        d[4],
		ControllerSpecific: d[5],
	}
	if info.Class&ErrClassCounters != 0 {
		info.TxErrors, info.RxErrors, info.CountersValid = d[6], d[7], true
	}
	return info, true
}

// String summarizes the error, for example
// "controller|counters controller=rx-passive tx=0 rx=128".
func (e ErrorInfo) String() string {
	var b strings.Builder
	b.WriteString(e.Class.String())
	if e.Class&ErrClassLostArbitration != 0 {
		b.WriteString(" arbitration-bit=" + strconv.Itoa(int(e.ArbitrationBit)))
	}
	if e.Class&ErrClassController != 0 {
		b.WriteString(" controller=" + e.Controller.String())
	}
	if e.Class&ErrClassProtocol != 0 {
		b.WriteString(" protocol=" + e.Protocol.String() + " location=0x" + strconv.FormatUint(uint64(e.ProtocolLocation), 16))
	}
	if e.Class&ErrClassTransceiver != 0 {
		b.WriteString(" transceiver=0x" + strconv.FormatUint(uint64(e.Transceiver), 16))
	}
	if e.CountersValid {
		b.WriteString(" tx=" + strconv.Itoa(int(e.TxErrors)) + " rx=" + strconv.Itoa(int(e.RxErrors)))
	}
	return b.String()
}

// NewErrorFrame builds an error frame from info, as a driver would report
// it. Virtual transports use it to inject controller errors.
func NewErrorFrame(info ErrorInfo) Frame {
	f := Frame{ID: ID(info.Class & ErrClassAll), Flags: FlagError, Len: 8}
	f.Data[0] = info.ArbitrationBit
	f.Data[1] = uint8(info.Controller)
	f.Data[2] = uint8(info.Protocol)
	f.Data[3] = info.ProtocolLocation
	f.Data[4] = info.Transceiver
	f.Data[5] = info.ControllerSpecific
	if info.CountersValid {
		f.ID |= ID(ErrClassCounters)
		f.Data[6], f.Data[7] = info.TxErrors, info.RxErrors
	}
	return f
}
