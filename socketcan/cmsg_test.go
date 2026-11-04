// SPDX-License-Identifier: MIT

package socketcan

import (
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// cmsg builds one control message as the kernel lays it out on this
// machine's word size.
func cmsg(level, typ uint32, data []byte) []byte {
	l := cmsgHdrLen + len(data)
	b := make([]byte, cmsgAlign(l))
	if wordSize == 8 {
		binary.NativeEndian.PutUint64(b, uint64(l))
	} else {
		binary.NativeEndian.PutUint32(b, uint32(l))
	}
	binary.NativeEndian.PutUint32(b[wordSize:], level)
	binary.NativeEndian.PutUint32(b[wordSize+4:], typ)
	copy(b[cmsgHdrLen:], data)
	return b
}

func ts(sec, nsec uint64) []byte {
	b := make([]byte, 2*wordSize)
	if wordSize == 8 {
		binary.NativeEndian.PutUint64(b, sec)
		binary.NativeEndian.PutUint64(b[8:], nsec)
	} else {
		binary.NativeEndian.PutUint32(b, uint32(sec))
		binary.NativeEndian.PutUint32(b[4:], uint32(nsec))
	}
	return b
}

func TestParseCmsgs(t *testing.T) {
	ovfl := binary.NativeEndian.AppendUint32(nil, 7)
	oob := append(cmsg(linuxSOLSocket, soTimestampNS, ts(1700000000, 123)), cmsg(linuxSOLSocket, soRxqOvfl, ovfl)...)
	got, n, ok := parseCmsgs(oob)
	if !got.Equal(time.Unix(1700000000, 123)) || n != 7 || !ok {
		t.Fatal(got, n, ok)
	}
	// Other levels and types are skipped.
	got, _, ok = parseCmsgs(append(cmsg(0, soTimestampNS, ts(5, 0)), cmsg(linuxSOLSocket, 99, ovfl)...))
	if !got.IsZero() || ok {
		t.Fatal("foreign control message used")
	}
	// Hostile or corrupt control data: the decoder stops, never panics,
	// and drops impossible timestamps.
	bad := [][]byte{
		cmsg(linuxSOLSocket, soTimestampNS, ts(1, 1e9)),                           // nsec out of range
		cmsg(linuxSOLSocket, soTimestampNS, ts(math.MaxUint64, 0)[:2*wordSize-1]), // short timespec
		cmsg(linuxSOLSocket, soRxqOvfl, []byte{1, 2}),                             // short counter
	}
	if wordSize == 8 {
		bad = append(bad, cmsg(linuxSOLSocket, soTimestampNS, ts(math.MaxUint64, 0))) // negative sec
	} else {
		bad = append(bad, cmsg(linuxSOLSocket, soTimestampNS, ts(math.MaxUint32, 0)))
	}
	for i, b := range bad {
		if got, _, ok := parseCmsgs(b); !got.IsZero() || ok {
			t.Errorf("bad control message %d decoded: %v %v", i, got, ok)
		}
	}
	h := cmsg(linuxSOLSocket, soRxqOvfl, ovfl)
	for _, l := range []uint64{0, 3, uint64(len(h)) + 1, 1 << 40, math.MaxUint64} {
		b := append([]byte(nil), h...)
		if wordSize == 8 {
			binary.NativeEndian.PutUint64(b, l)
		} else {
			binary.NativeEndian.PutUint32(b, uint32(l))
		}
		if _, _, ok := parseCmsgs(b); ok {
			t.Errorf("cmsg_len %d accepted", l)
		}
	}
}

func TestControlLenAndSockaddr(t *testing.T) {
	if controlLen(math.MaxUint64) != oobSize || controlLen(oobSize+1) != oobSize || controlLen(20) != 20 {
		t.Fatal("controlLen")
	}
	sa := make([]byte, sockaddrCANLen)
	binary.NativeEndian.PutUint32(sa[4:], 42)
	if sockaddrIndex(sa) != 42 || sockaddrIndex(sa[:7]) != 0 {
		t.Fatal("sockaddrIndex")
	}
	binary.NativeEndian.PutUint32(sa[4:], 0x80000000)
	if sockaddrIndex(sa) != 0 {
		t.Fatal("negative ifindex used")
	}
}

func TestSocketBounds(t *testing.T) {
	for _, d := range []time.Duration{-1, -time.Hour, 127*time.Millisecond + 1, 5 * time.Second, math.MaxInt64} {
		if _, err := forceTxSTminNS(d); !errors.Is(err, canbus.ErrInvalidOption) {
			t.Errorf("ForceTxSTmin %v accepted", d)
		}
	}
	if ns, err := forceTxSTminNS(127 * time.Millisecond); err != nil || ns != 127e6 {
		t.Fatal(ns, err)
	}
	if _, err := ifindex32(-1); !errors.Is(err, canbus.ErrInvalidOption) {
		t.Error("ifindex -1 accepted")
	}
	if strconv.IntSize == 64 {
		big := int64(1) << 32
		if _, err := ifindex32(int(big)); !errors.Is(err, canbus.ErrInvalidOption) {
			t.Error("ifindex 2^32 accepted")
		}
	}
	// marshalXL never writes a length field that disagrees with the
	// payload, even for an unvalidated frame.
	f := canbus.XLFrame{Data: make([]byte, canbus.MaxXLLen+100)}
	buf := make([]byte, canXLMTU)
	if n := marshalXL(&f, buf); n != canXLMTU || binary.NativeEndian.Uint16(buf[6:]) != canbus.MaxXLLen {
		t.Fatal(n)
	}
}

// FuzzParseCmsgs: arbitrary control data from the kernel never panics and
// yields only valid timestamps.
func FuzzParseCmsgs(f *testing.F) {
	f.Add(append(cmsg(linuxSOLSocket, soTimestampNS, ts(1700000000, 1)), cmsg(linuxSOLSocket, soRxqOvfl, []byte{1, 0, 0, 0})...))
	f.Add(cmsg(linuxSOLSocket, soRxqOvfl, nil))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > oobSize {
			b = b[:oobSize]
		}
		got, _, _ := parseCmsgs(b)
		if !got.IsZero() && (got.Unix() < 0 || got.Nanosecond() >= 1e9) {
			t.Fatal(got)
		}
	})
}
