// SPDX-License-Identifier: MIT

package slcan

import (
	"fmt"

	"github.com/otfabric/go-canbus"
)

// Linux CAN_ERR_PROT_LOC_* codes used by error records.
const (
	locCRCSeq = 0x08 // CRC sequence
	locACK    = 0x19 // ACK slot
)

// ParseErrorRecord parses an "e" record, in which an adapter reports bus
// errors: "e", the number of error codes (0-8) and one letter per error —
// a (ACK), b (bit 0), B (bit 1), c (CRC), f (form), o (RX overrun), O (TX
// overrun) or s (stuff). For example "e3bcO" reports a bit 0 error, a CRC
// error and a TX overrun. It fills f with the error frame the Linux slcan
// driver delivers for the record. It reports false, without error, for a
// line that is not an "e" record. As in Linux, characters after the codes
// are ignored and a record has at least three characters.
func ParseErrorRecord(line []byte, f *canbus.Frame) (bool, error) {
	if len(line) == 0 || line[0] != 'e' {
		return false, nil
	}
	if len(line) < 3 || line[1] < '0' || line[1] > '8' {
		return false, fmt.Errorf("slcan: bad error count in %q", line)
	}
	n := int(line[1] - '0')
	if len(line) < 2+n {
		return false, fmt.Errorf("slcan: %d error codes announced in %q", n, line)
	}
	info := canbus.ErrorInfo{Class: canbus.ErrClassProtocol | canbus.ErrClassBusError}
	for _, c := range line[2 : 2+n] {
		switch c {
		case 'a':
			info.Class |= canbus.ErrClassNoAck
			info.ProtocolLocation = locACK
		case 'b':
			info.Protocol |= canbus.ProtBit0
		case 'B':
			info.Protocol |= canbus.ProtBit1
		case 'c':
			info.Protocol |= canbus.ProtBit
			info.ProtocolLocation = locCRCSeq
		case 'f':
			info.Protocol |= canbus.ProtForm
		case 'o':
			info.Class |= canbus.ErrClassController
			info.Controller = canbus.CtrlRxOverflow
		case 'O':
			info.Class |= canbus.ErrClassController
			info.Controller = canbus.CtrlTxOverflow
		case 's':
			info.Protocol |= canbus.ProtStuff
		default:
			return false, fmt.Errorf("slcan: unknown error code %q in %q", c, line)
		}
	}
	*f = canbus.NewErrorFrame(info)
	return true, nil
}

// ParseStateRecord parses an "s" record, in which an adapter reports its
// controller state and error counters: "s", the state — a (error
// active), w (warning), p (passive) or b (bus off) — and the receive and
// transmit error counters as three decimal digits each. For example
// "sp130012" reports error passive with 130 receive and 12 transmit
// errors. It reports false, without error, for a line that is not an "s"
// record. The record has exactly eight characters, as recent Linux
// versions require (older ones ignore characters after the counters).
func ParseStateRecord(line []byte) (st canbus.State, txErrors, rxErrors int, ok bool, err error) {
	if len(line) == 0 || line[0] != 's' {
		return 0, 0, 0, false, nil
	}
	if len(line) != 8 {
		return 0, 0, 0, false, fmt.Errorf("slcan: state record %q is not 8 characters", line)
	}
	switch line[1] {
	case 'a':
		st = canbus.StateActive
	case 'w':
		st = canbus.StateWarning
	case 'p':
		st = canbus.StatePassive
	case 'b':
		st = canbus.StateBusOff
	default:
		return 0, 0, 0, false, fmt.Errorf("slcan: unknown state in %q", line)
	}
	rx, ok1 := decimal(line[2:5])
	tx, ok2 := decimal(line[5:8])
	if !ok1 || !ok2 {
		return 0, 0, 0, false, fmt.Errorf("slcan: bad error counters in %q", line)
	}
	return st, tx, rx, true, nil
}

func decimal(s []byte) (int, bool) {
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int(c-'0')
	}
	return v, true
}

var (
	txStatus = map[canbus.State]canbus.ControllerStatus{canbus.StateActive: canbus.CtrlActive,
		canbus.StateWarning: canbus.CtrlTxWarning, canbus.StatePassive: canbus.CtrlTxPassive}
	rxStatus = map[canbus.State]canbus.ControllerStatus{canbus.StateActive: canbus.CtrlActive,
		canbus.StateWarning: canbus.CtrlRxWarning, canbus.StatePassive: canbus.CtrlRxPassive}
)

// StateFrame returns the error frame the Linux slcan driver delivers when
// a state record moves the controller from prev to st, with the
// record's error counters. It reports false when the state did not
// change: Linux then delivers nothing, even if the counters did. Counters
// above 255 do not fit the frame and wrap, as in Linux.
func StateFrame(prev, st canbus.State, txErrors, rxErrors int) (canbus.Frame, bool) {
	if st == prev {
		return canbus.Frame{}, false
	}
	if st == canbus.StateBusOff {
		return canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrClassBusOff}), true
	}
	// Linux attributes the state to the side with more errors (both on a
	// tie); the other side stays error active.
	txState, rxState := canbus.StateActive, canbus.StateActive
	if txErrors >= rxErrors {
		txState = st
	}
	if txErrors <= rxErrors {
		rxState = st
	}
	var ctrl canbus.ControllerStatus
	if txState >= rxState {
		ctrl |= txStatus[txState]
	}
	if txState <= rxState {
		ctrl |= rxStatus[rxState]
	}
	return canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrClassController, Controller: ctrl,
		TxErrors: uint8(txErrors), RxErrors: uint8(rxErrors), CountersValid: true}), true
}
