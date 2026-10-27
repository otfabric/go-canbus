// SPDX-License-Identifier: MIT

// Package link reads and changes the configuration of Linux CAN network
// interfaces through rtnetlink, the interface ip-link(8) uses: listing CAN
// interfaces, their state, bit timing, CAN FD data bit timing, control
// modes, error counters and statistics; bringing them up and down; setting
// bit rates, CAN FD mode, restart-ms and MTU; restarting a bus-off
// controller; creating and deleting vcan and vxcan interfaces.
//
// It is an administrative API, kept apart from the transports on purpose:
// opening a socket never reconfigures an interface. Reading needs no
// privileges; changes need CAP_NET_ADMIN, and the kernel rejects bit timing
// changes while an interface is up (EBUSY) and on virtual interfaces, which
// have no bit timing (EOPNOTSUPP).
//
// On systems other than Linux every function returns
// [canbus.ErrUnsupported].
package link

import (
	"fmt"
	"strings"
)

// ControllerState is the CAN controller state (IFLA_CAN_STATE).
type ControllerState uint32

// Controller states (enum can_state).
const (
	StateErrorActive ControllerState = iota
	StateErrorWarning
	StateErrorPassive
	StateBusOff
	StateStopped
	StateSleeping
)

// String names the state as ip-link does.
func (s ControllerState) String() string {
	names := []string{"ERROR-ACTIVE", "ERROR-WARNING", "ERROR-PASSIVE", "BUS-OFF", "STOPPED", "SLEEPING"}
	if int(s) < len(names) {
		return names[s]
	}
	return fmt.Sprintf("STATE-%d", uint32(s))
}

// CtrlMode is a set of CAN controller modes (CAN_CTRLMODE_*).
type CtrlMode uint32

// Controller modes.
const (
	ModeLoopback      CtrlMode = 0x001
	ModeListenOnly    CtrlMode = 0x002
	ModeTripleSample  CtrlMode = 0x004
	ModeOneShot       CtrlMode = 0x008
	ModeBerrReporting CtrlMode = 0x010
	ModeFD            CtrlMode = 0x020
	ModePresumeAck    CtrlMode = 0x040
	ModeFDNonISO      CtrlMode = 0x080
	ModeCCLen8DLC     CtrlMode = 0x100
	ModeTDCAuto       CtrlMode = 0x200
	ModeTDCManual     CtrlMode = 0x400
)

var modeNames = []string{"LOOPBACK", "LISTEN-ONLY", "TRIPLE-SAMPLING", "ONE-SHOT", "BERR-REPORTING",
	"FD", "PRESUME-ACK", "FD-NON-ISO", "CC-LEN8-DLC", "TDC-AUTO", "TDC-MANUAL"}

// String lists the modes, for example "FD,BERR-REPORTING".
func (m CtrlMode) String() string {
	var parts []string
	for i, n := range modeNames {
		if m&(1<<i) != 0 {
			parts = append(parts, n)
		}
	}
	if rest := m &^ (1<<len(modeNames) - 1); rest != 0 {
		parts = append(parts, fmt.Sprintf("0x%x", uint32(rest)))
	}
	return strings.Join(parts, ",")
}

// BitTiming is struct can_bittiming. SamplePoint is in tenths of a percent
// (875 = 87.5 %), as the kernel reports it.
type BitTiming struct {
	Bitrate     uint32 `json:"bitrate"`
	SamplePoint uint32 `json:"samplePoint"`
	TQ          uint32 `json:"tq"`
	PropSeg     uint32 `json:"propSeg"`
	PhaseSeg1   uint32 `json:"phaseSeg1"`
	PhaseSeg2   uint32 `json:"phaseSeg2"`
	SJW         uint32 `json:"sjw"`
	BRP         uint32 `json:"brp"`
}

// ErrorCounters is struct can_berr_counter.
type ErrorCounters struct {
	TX uint16 `json:"tx"`
	RX uint16 `json:"rx"`
}

// DeviceStats is struct can_device_stats (IFLA_INFO_XSTATS of kind can).
type DeviceStats struct {
	BusError        uint32 `json:"busError"`
	ErrorWarning    uint32 `json:"errorWarning"`
	ErrorPassive    uint32 `json:"errorPassive"`
	BusOff          uint32 `json:"busOff"`
	ArbitrationLost uint32 `json:"arbitrationLost"`
	Restarts        uint32 `json:"restarts"`
}

// Stats are the generic interface counters (IFLA_STATS64).
type Stats struct {
	RxPackets uint64 `json:"rxPackets"`
	TxPackets uint64 `json:"txPackets"`
	RxBytes   uint64 `json:"rxBytes"`
	TxBytes   uint64 `json:"txBytes"`
	RxErrors  uint64 `json:"rxErrors"`
	TxErrors  uint64 `json:"txErrors"`
	RxDropped uint64 `json:"rxDropped"`
	TxDropped uint64 `json:"txDropped"`
}

// Interface describes a CAN network interface.
type Interface struct {
	Name       string `json:"name"`
	Index      int    `json:"index"`
	Kind       string `json:"kind"` // "can" (a controller driver), "vcan", "vxcan", or "" when the kernel does not say
	Up         bool   `json:"up"`
	OperState  string `json:"operState"`
	MTU        int    `json:"mtu"` // 16 classical, 72 CAN FD, 2060 CAN XL (Linux ≥ 6.2)
	TxQueueLen int    `json:"txQueueLen"`
	Stats      Stats  `json:"stats"`

	// Controller details, for kind "can" only.
	State             *ControllerState `json:"state,omitempty"`
	BitTiming         *BitTiming       `json:"bitTiming,omitempty"`
	DataBitTiming     *BitTiming       `json:"dataBitTiming,omitempty"`
	ClockHz           uint32           `json:"clockHz,omitempty"`
	CtrlModeSupported CtrlMode         `json:"ctrlModeSupported,omitempty"`
	CtrlMode          CtrlMode         `json:"ctrlMode,omitempty"`
	RestartMs         *uint32          `json:"restartMs,omitempty"`
	ErrorCounters     *ErrorCounters   `json:"errorCounters,omitempty"`
	DeviceStats       *DeviceStats     `json:"deviceStats,omitempty"`
}

// FD reports whether the interface carries CAN FD frames: its MTU is the
// CAN FD MTU (72) or the CAN XL MTU (2060).
func (i *Interface) FD() bool { return i.MTU >= 72 }

// Virtual reports a vcan or vxcan interface.
func (i *Interface) Virtual() bool { return i.Kind == "vcan" || i.Kind == "vxcan" }

// Config is a set of changes for [Configure]. Nil and zero fields are left
// unchanged.
type Config struct {
	Bitrate         uint32 // nominal bit rate in bit/s
	SamplePoint     uint32 // nominal sample point in tenths of a percent (0: driver default)
	DataBitrate     uint32 // CAN FD data bit rate in bit/s
	DataSamplePoint uint32
	// Modes to switch on (Set) and off (Clear), for example ModeFD.
	Set, Clear CtrlMode
	RestartMs  *uint32
}

// Empty reports a Config without changes.
func (c *Config) Empty() bool {
	return c.Bitrate == 0 && c.DataBitrate == 0 && c.Set == 0 && c.Clear == 0 && c.RestartMs == nil
}
