// Real netlink push for Linux : translates a bgp.Route into a RTM_NEWROUTE
// (install) or RTM_DELROUTE (withdraw) message via vishvananda/netlink.
//
// netlink is Linux-only (uses Linux-specific syscall constants), hence
// the _linux.go filename suffix — the Go toolchain treats this as
// //go:build linux automatically. Non-Linux builds get push_other.go
// instead, which is a no-op stub so the package compiles everywhere.

package fib

import (
	"fmt"
	"net"

	"github.com/openweft/weft-router/internal/bgp"
	"github.com/vishvananda/netlink"
)

func (p *Programmer) pushRoute(r bgp.Route) error {
	_, dst, err := net.ParseCIDR(r.Prefix)
	if err != nil {
		return fmt.Errorf("parse prefix %s: %w", r.Prefix, err)
	}
	route := &netlink.Route{
		Dst:   dst,
		Table: p.Table,
	}
	if r.NextHop != "" {
		gw := net.ParseIP(r.NextHop)
		if gw == nil {
			return fmt.Errorf("parse nexthop %s", r.NextHop)
		}
		route.Gw = gw
	}
	if r.Withdraw {
		if err := netlink.RouteDel(route); err != nil {
			return fmt.Errorf("RouteDel %s: %w", r.Prefix, err)
		}
		p.log.Debug("fib withdrew", "prefix", r.Prefix, "table", p.Table)
		return nil
	}
	// RouteReplace is idempotent (no error if the route already exists,
	// updates next-hop if it changed) — exactly what we want for a
	// reconcile-driven path. RouteAdd would fail on dup, requiring a
	// pre-Del that races with the WatchEvent re-delivery.
	if err := netlink.RouteReplace(route); err != nil {
		return fmt.Errorf("RouteReplace %s: %w", r.Prefix, err)
	}
	p.log.Debug("fib installed", "prefix", r.Prefix, "nexthop", r.NextHop, "table", p.Table)
	return nil
}
