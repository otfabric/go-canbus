// SPDX-License-Identifier: MIT

//go:build !linux

package link

import (
	"fmt"
	"runtime"

	"github.com/otfabric/go-canbus"
)

func unsupported() error {
	return fmt.Errorf("%w: CAN interface administration needs Linux rtnetlink (this is %s)", canbus.ErrUnsupported, runtime.GOOS)
}

// List reports [canbus.ErrUnsupported].
func List() ([]Interface, error) { return nil, unsupported() }

// Get reports [canbus.ErrUnsupported].
func Get(string) (Interface, error) { return Interface{}, unsupported() }

// SetUp reports [canbus.ErrUnsupported].
func SetUp(string) error { return unsupported() }

// SetDown reports [canbus.ErrUnsupported].
func SetDown(string) error { return unsupported() }

// SetMTU reports [canbus.ErrUnsupported].
func SetMTU(string, int) error { return unsupported() }

// Configure reports [canbus.ErrUnsupported].
func Configure(string, Config) error { return unsupported() }

// Restart reports [canbus.ErrUnsupported].
func Restart(string) error { return unsupported() }

// AddVCAN reports [canbus.ErrUnsupported].
func AddVCAN(string, bool) error { return unsupported() }

// AddVXCAN reports [canbus.ErrUnsupported].
func AddVXCAN(string, string) error { return unsupported() }

// Delete reports [canbus.ErrUnsupported].
func Delete(string) error { return unsupported() }
