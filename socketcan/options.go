// SPDX-License-Identifier: MIT

package socketcan

import "github.com/otfabric/go-canbus"

type config struct {
	fd          bool
	filters     *canbus.FilterSet
	noLoopback  bool
	recvOwn     bool
	noTimestamp bool
	rcvBuf      int
	sndBuf      int
}

// Option configures [Open].
type Option func(*config)

// WithFD opens the socket for CAN FD (CAN_RAW_FD_FRAMES). The interface
// must have the CAN FD MTU (72); otherwise Open fails with
// [canbus.ErrUnsupported].
func WithFD() Option { return func(c *config) { c.fd = true } }

// WithFilters sets the kernel filters and error mask (CAN_RAW_FILTER,
// CAN_RAW_JOIN_FILTERS, CAN_RAW_ERR_FILTER). Without it the socket
// receives every data and remote frame and no error frame.
func WithFilters(fs canbus.FilterSet) Option {
	return func(c *config) { c.filters = &fs }
}

// WithoutLoopback disables local loopback (CAN_RAW_LOOPBACK 0): other
// sockets on this host do not see this socket's frames.
func WithoutLoopback() Option { return func(c *config) { c.noLoopback = true } }

// WithRecvOwn delivers the socket's own transmissions back to it
// (CAN_RAW_RECV_OWN_MSGS), marked with [canbus.Frame.Echo].
func WithRecvOwn() Option { return func(c *config) { c.recvOwn = true } }

// WithoutTimestamps does not request kernel receive timestamps
// (SO_TIMESTAMPNS); frames then carry the time Receive returned.
func WithoutTimestamps() Option { return func(c *config) { c.noTimestamp = true } }

// WithReceiveBuffer sets SO_RCVBUF in bytes. Larger buffers absorb longer
// bursts before frames are dropped.
func WithReceiveBuffer(n int) Option { return func(c *config) { c.rcvBuf = n } }

// WithSendBuffer sets SO_SNDBUF in bytes.
func WithSendBuffer(n int) Option { return func(c *config) { c.sndBuf = n } }
