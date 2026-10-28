// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/capture"
	"github.com/otfabric/go-canbus/socketcan"
)

// TestTcpdumpCapturesXL sends CAN, CAN FD and CAN XL frames on vcanxl0
// while tcpdump (libpcap) captures them, then reads the capture with this
// package and, where installed, Wireshark's tshark: the kernel and libpcap
// are the oracle for the pcap layout of CAN XL. It needs root (capture)
// and runs with CANBUS_TEST_ADMIN=1; CANBUS_REQUIRE_XL=1 makes a missing
// prerequisite a failure.
func TestTcpdumpCapturesXL(t *testing.T) {
	need := func(msg string) {
		t.Helper()
		if os.Getenv("CANBUS_REQUIRE_XL") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	if os.Getenv("CANBUS_TEST_ADMIN") != "1" {
		t.Skip("needs root: set CANBUS_TEST_ADMIN=1")
	}
	ifc := os.Getenv("CANBUS_TEST_XL_IFACE")
	if ifc == "" {
		ifc = "vcanxl0"
	}
	if ifi, err := net.InterfaceByName(ifc); err != nil || ifi.Flags&net.FlagUp == 0 || ifi.MTU < 76 {
		need("no usable " + ifc + " (mtu 2060, Linux ≥ 6.2)")
	}
	tcpdump, err := exec.LookPath("tcpdump")
	if err != nil {
		need("tcpdump not installed")
	}
	// libpcap before 1.10.6 rewrites byte 5 of CAN FD and CAN XL packets:
	// 1.10.4 (Ubuntu 24.04) writes the CAN FD flags as 0 (BRS and ESI
	// lost) and clears bit 2 of the CAN XL SDT. 1.10.6 keeps both.
	oldLibpcap := false
	if v, err := exec.Command(tcpdump, "--version").CombinedOutput(); err == nil {
		var a, b, c int
		if i := strings.Index(string(v), "libpcap version "); i >= 0 {
			_, _ = fmt.Sscanf(string(v[i+len("libpcap version "):]), "%d.%d.%d", &a, &b, &c)
		}
		oldLibpcap = a < 1 || a == 1 && (b < 10 || b == 10 && c < 6)
		t.Logf("%s", strings.SplitN(string(v[strings.Index(string(v), "libpcap"):]), "\n", 2)[0])
	}
	path := filepath.Join(t.TempDir(), "xl.pcap")
	// -Z root: the file stays root's (tcpdump otherwise hands it to its
	// own user, which tshark, run as root, may not read).
	cmd := exec.Command(tcpdump, "-i", ifc, "-U", "--immediate-mode", "-Z", "root", "-w", path)
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// tcpdump reports "listening on" once the capture runs.
	ready := make(chan bool, 1)
	var errText bytes.Buffer
	go func() {
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			errText.WriteString(s.Text() + "\n")
			if strings.Contains(s.Text(), "listening on") {
				ready <- true
			}
		}
		close(ready)
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatalf("tcpdump did not start: %s", errText.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tcpdump did not start")
	}

	opts := []socketcan.Option{socketcan.WithXL()}
	vcid := uint8(0)
	if c, err := socketcan.Open(ifc, socketcan.WithVCIDFilter(0, 0)); err == nil { // Linux ≥ 6.9
		c.Close()
		opts = append(opts, socketcan.WithVCIDFilter(0, 0))
		vcid = 0x2A
	}
	tx, err := socketcan.Open(ifc, opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var want []string
	classic, _ := canbus.NewStandard(0x5C1, []byte{0xDE, 0xAD})
	fd, _ := canbus.NewFD(0x5C2, false, true, bytes.Repeat([]byte{0xA5}, 48))
	for _, f := range []*canbus.Frame{&classic, &fd} {
		if err := tx.Send(ctx, f); err != nil {
			t.Fatal(err)
		}
		want = append(want, f.String())
	}
	for _, x := range []canbus.XLFrame{
		{Priority: 0x5C3, VCID: vcid, Flags: canbus.XLSEC, SDT: 0x01, AF: 0x12345678, Data: []byte{1, 2, 3}},
		{Priority: 0x5C4, SDT: 0x07, AF: 0xFFFFFFFF, Data: bytes.Repeat([]byte{0x5A}, 2048)},
	} {
		if err := tx.SendXL(ctx, &x); err != nil {
			t.Fatal(err)
		}
		want = append(want, x.String())
	}
	time.Sleep(300 * time.Millisecond)
	_ = cmd.Process.Signal(syscall.SIGINT)
	_ = cmd.Wait()

	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	r, format, err := capture.NewReader(fh)
	if err != nil || format != capture.FormatPCAP {
		t.Fatalf("%s: %v", format, err)
	}
	var got []string
	for {
		var f canbus.Frame
		var x canbus.XLFrame
		isXL, err := r.(capture.XLReader).ReadAny(&f, &x)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case isXL && x.Priority&0x7F0 == 0x5C0:
			got = append(got, x.String())
		case !isXL && f.ID&0x7F0 == 0x5C0:
			got = append(got, f.String())
		}
	}
	if oldLibpcap {
		// Compare what that libpcap keeps: CAN FD without BRS/ESI, CAN XL
		// SDT without bit 2.
		strip := func(s []string) []string {
			out := make([]string, len(s))
			for i, v := range s {
				if j := strings.Index(v, "##"); j >= 0 {
					v = v[:j+2] + "0" + v[j+3:]
				} else if canbus.IsXLText(v) {
					x, _ := canbus.ParseXLFrame(v)
					x.SDT &^= 0x04
					v = x.String()
				}
				out[i] = v
			}
			return out
		}
		t.Logf("libpcap older than 1.10.6: CAN FD flags and CAN XL SDT bit 2 not compared (libpcap alters them)")
		got, want = strip(got), strip(want)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("libpcap capture read as\n%.300s\nwant\n%.300s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	t.Logf("libpcap capture of %d frames read back (VCID %#x)", len(got), vcid)

	tshark, err := exec.LookPath("tshark")
	if err != nil {
		if os.Getenv("CANBUS_REQUIRE_TSHARK") == "1" {
			t.Fatal("tshark not installed")
		}
		return
	}
	out, err := exec.Command(tshark, "-r", path, "-Y", "canxl", "-T", "fields", "-E", "separator=,",
		"-e", "canxl.priority", "-e", "canxl.vcid", "-e", "canxl.sdu_type", "-e", "canxl.len", "-e", "canxl.acceptance_field").Output()
	if err != nil {
		t.Fatalf("tshark: %v", err)
	}
	var fields []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(l, "147") { // priorities 0x5C3, 0x5C4 = 1475, 1476
			fields = append(fields, l)
		}
	}
	wantFields := []string{"1475," + strconv.Itoa(int(vcid)) + ",0x01,3,305419896", "1476,0,0x07,2048,4294967295"}
	if oldLibpcap {
		wantFields[1] = "1476,0,0x03,2048,4294967295"
	}
	if strings.Join(fields, " ") != strings.Join(wantFields, " ") {
		t.Fatalf("Wireshark dissected %v, want %v", fields, wantFields)
	}
}
