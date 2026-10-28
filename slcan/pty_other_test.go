// SPDX-License-Identifier: MIT

//go:build !linux && !darwin

package slcan

import (
	"errors"
	"os"
)

func openPTY() (*os.File, string, error) { return nil, "", errors.New("no pseudo-terminals on this platform") }
