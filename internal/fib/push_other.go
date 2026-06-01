//go:build !linux

// Stub pushRoute for non-Linux builds (mac/Windows dev hosts). netlink
// is Linux-only ; on the dev host we just log the call so the daemon
// compiles and the lifecycle wiring can be exercised end-to-end. The
// real netlink push lives in push_linux.go.

package fib

import "github.com/openweft/weft-router/internal/bgp"

func (p *Programmer) pushRoute(r bgp.Route) error {
	if r.Withdraw {
		p.log.Debug("fib withdraw (no-op on non-linux)", "prefix", r.Prefix, "table", p.Table)
	} else {
		p.log.Debug("fib install (no-op on non-linux)", "prefix", r.Prefix, "nexthop", r.NextHop, "table", p.Table)
	}
	return nil
}
