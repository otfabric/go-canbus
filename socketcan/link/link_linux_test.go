// SPDX-License-Identifier: MIT

//go:build linux

package link

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/socketcan"
)

func TestListFindsVCAN(t *testing.T) {
	name := os.Getenv("CANBUS_TEST_IFACE")
	if name == "" {
		name = "vcan0"
	}
	it, err := Get(name)
	if err != nil {
		if os.Getenv("CANBUS_REQUIRE_VCAN") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	if it.Kind != "vcan" || !it.Virtual() || it.Name != name || it.Index == 0 {
		t.Fatalf("%+v", it)
	}
	all, err := List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range all {
		found = found || i.Name == name
	}
	if !found {
		t.Fatalf("%s not in %+v", name, all)
	}
	if _, err := Get("lo"); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal("lo:", err)
	}
	if _, err := Get("nosuchif9"); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal(err)
	}
}

// TestAdmin creates, configures and deletes interfaces. It needs
// CAP_NET_ADMIN and runs only with CANBUS_TEST_ADMIN=1 (sudo -E go test).
func TestAdmin(t *testing.T) {
	if os.Getenv("CANBUS_TEST_ADMIN") != "1" {
		t.Skip("set CANBUS_TEST_ADMIN=1 and run as root")
	}
	const name = "gotestvcan0"
	_ = Delete(name)
	if err := AddVCAN(name, false); err != nil {
		t.Fatal(err)
	}
	defer Delete(name)
	it, err := Get(name)
	if err != nil || it.Up || it.MTU != 16 {
		t.Fatalf("%+v %v", it, err)
	}
	if err := SetMTU(name, 72); err != nil {
		t.Fatal(err)
	}
	if err := SetUp(name); err != nil {
		t.Fatal(err)
	}
	if it, _ = Get(name); !it.Up || !it.FD() {
		t.Fatalf("after up: %+v", it)
	}
	if err := Configure(name, Config{Bitrate: 500000}); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal("bitrate on vcan:", err)
	}
	if err := Configure(name, Config{}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(name, Config{Set: ModeFD, Clear: ModeFD}); !errors.Is(err, canbus.ErrInvalidOption) {
		t.Fatal(err)
	}
	if err := Restart(name); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal("restart on vcan:", err)
	}
	// Taking the interface down ends a blocked Receive with
	// ErrInterfaceDown, and the socket works again after up.
	c, err := socketcan.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	errc := make(chan error, 1)
	go func() { errc <- c.Receive(context.Background(), &canbus.Frame{}) }()
	time.Sleep(50 * time.Millisecond)
	if err := SetDown(name); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal("receive while going down:", err)
	}
	f, _ := canbus.NewStandard(1, nil)
	if err := c.Send(context.Background(), &f); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal("send while down:", err)
	}
	if err := SetUp(name); err != nil {
		t.Fatal(err)
	}
	if err := AddVCAN(name, false); err == nil {
		t.Fatal("duplicate interface created")
	}

	// vxcan: frames cross from one end to the other.
	_ = Delete("gotestvx0")
	if err := AddVXCAN("gotestvx0", "gotestvx1"); err != nil {
		t.Fatal(err)
	}
	defer Delete("gotestvx0")
	for _, n := range []string{"gotestvx0", "gotestvx1"} {
		if err := SetUp(n); err != nil {
			t.Fatal(err)
		}
	}
	if it, _ := Get("gotestvx1"); it.Kind != "vxcan" {
		t.Fatalf("%+v", it)
	}
	a, _ := socketcan.Open("gotestvx0")
	defer a.Close()
	b, _ := socketcan.Open("gotestvx1")
	defer b.Close()
	// The kernel activates the transmit queue of a link that just came up
	// asynchronously (linkwatch): early frames may be dropped. Send until
	// one arrives.
	f, _ = canbus.NewExtended(0x1234, []byte{1, 2})
	var g canbus.Frame
	var rerr error
	for range 40 {
		if err := a.Send(context.Background(), &f); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		rerr = b.Receive(ctx, &g)
		cancel()
		if rerr == nil {
			break
		}
	}
	if rerr != nil || g.ID != 0x1234 || g.Interface != "gotestvx1" {
		t.Fatal(g, rerr)
	}
	if err := Delete("gotestvx0"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("gotestvx0"); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal(err)
	}
}

func TestUnprivilegedChangeFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	if err := AddVCAN("gotestnope0", false); err == nil {
		Delete("gotestnope0")
		t.Fatal("unprivileged create succeeded")
	}
}

// TestArgumentChecks: invalid arguments fail before any netlink request,
// so no privilege is needed.
func TestArgumentChecks(t *testing.T) {
	for name, err := range map[string]error{
		"SetMTU -1":          SetMTU("vcan0", -1),
		"SetMTU 0":           SetMTU("vcan0", 0),
		"AddVCAN empty":      AddVCAN("", false),
		"AddVCAN 16 bytes":   AddVCAN("abcdefghijklmnop", true),
		"AddVXCAN empty":     AddVXCAN("", "peer0"),
		"AddVXCAN peer long": AddVXCAN("vx0", "abcdefghijklmnop"),
	} {
		if !errors.Is(err, canbus.ErrInvalidOption) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAckError(t *testing.T) {
	if err := ackError([]byte{0, 0, 0, 0}); err != nil {
		t.Errorf("ACK: %v", err)
	}
	if err := ackError([]byte{1}); err == nil || errors.Is(err, canbus.ErrInvalidOption) {
		t.Errorf("truncated error message: %v", err)
	}
	eperm := []byte{0xFF, 0xFF, 0xFF, 0xFF} // -1 in either byte order
	if err := ackError(eperm); err == nil {
		t.Error("EPERM reported as ACK")
	}
}
