// SPDX-License-Identifier: MIT

package socketcan

import "github.com/otfabric/go-canbus"

var _ canbus.XLTransport = (*Conn)(nil)
