// Package fib programmes the Linux kernel FIB from BGP route updates.
//
// The programmer reads bgp.Route values off the BGP server's channel and
// translates each one into a netlink RTM_NEWROUTE / RTM_DELROUTE message.
// Routes go into a dedicated routing table (per the bootstrap config, so
// tenant routes never collide with weft's own management table) ; ip-rule
// hooks the table to the tenant's veth.
//
// Failure mode : if netlink rejects a route, we log + continue. We don't
// retry — weft-network's next reconcile loop will re-emit any path that
// matters. Pushing a forever-retry queue here would just hide host-level
// issues (e.g. the routing table being misconfigured upstream).
package fib

import (
	"context"
	"log/slog"

	"github.com/openweft/weft-router/internal/bgp"
)

// Programmer drains route updates from a bgp.Server and pushes them
// to the kernel.
type Programmer struct {
	log *slog.Logger
	bgp *bgp.Server
}

// NewProgrammer wires the programmer to a bgp.Server.
func NewProgrammer(log *slog.Logger, b *bgp.Server) *Programmer {
	return &Programmer{log: log, bgp: b}
}

// Run blocks until ctx is cancelled or the BGP route channel closes.
// Each route update produces one netlink call.
//
// TODO: replace the stub log call with the actual netlink push :
//
//	netlink.RouteAdd(&netlink.Route{
//	    Dst: parsedPrefix, Gw: parsedNextHop, Table: cfg.Table,
//	})
//
// — and the symmetric RouteDel for withdraw. vishvananda/netlink only
// works on Linux ; on darwin we'll need a build-tag stub so the package
// still compiles on the dev host. (The cmd builds linux-only for prod.)
func (p *Programmer) Run(ctx context.Context) error {
	routes := p.bgp.Routes()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r, ok := <-routes:
			if !ok {
				p.log.Info("bgp routes channel closed, programmer exiting")
				return nil
			}
			if r.Withdraw {
				p.log.Debug("fib withdraw (stub)", "prefix", r.Prefix)
			} else {
				p.log.Debug("fib install (stub)", "prefix", r.Prefix, "nexthop", r.NextHop)
			}
		}
	}
}
