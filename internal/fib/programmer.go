// Package fib programmes the Linux kernel FIB from BGP route updates.
//
// The programmer reads bgp.Route values off the BGP server's channel and
// translates each one into a netlink RTM_NEWROUTE / RTM_DELROUTE message.
// Routes go into a dedicated routing table (configurable, default 254 =
// "main" — change per deployment so tenant routes don't collide with
// weft's own management table) ; ip-rule hooks the table to the tenant's
// veth.
//
// Failure mode : if netlink rejects a route, we log + continue. We don't
// retry — weft-network's next reconcile loop will re-emit any path that
// matters. Pushing a forever-retry queue here would just hide host-level
// issues (e.g. the routing table being misconfigured upstream).
//
// Platform : the real netlink push lives in push_linux.go ; non-linux
// builds (the macOS dev host) get the stub in push_other.go which logs
// the call but doesn't touch the kernel. That way the package compiles
// everywhere ; only the cmd binary is meaningful on Linux.
package fib

import (
	"context"
	"log/slog"

	"github.com/openweft/weft-router/internal/bgp"
)

// Programmer drains route updates from a bgp.Server and pushes them
// to the kernel via netlink (on Linux) or a no-op (elsewhere).
type Programmer struct {
	log *slog.Logger
	bgp *bgp.Server
	// Table is the kernel routing table id to install into. 254 = main.
	// Pick a tenant-private id (e.g. 100) when running multiple tenants
	// on the same host network namespace.
	Table int
}

// NewProgrammer wires the programmer to a bgp.Server.
func NewProgrammer(log *slog.Logger, b *bgp.Server) *Programmer {
	return &Programmer{log: log, bgp: b, Table: 254}
}

// Run blocks until ctx is cancelled or the BGP route channel closes.
// Each route update produces one netlink call (RTM_NEWROUTE for install,
// RTM_DELROUTE for withdraw).
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
			if err := p.pushRoute(r); err != nil {
				p.log.Warn("fib push failed", "prefix", r.Prefix, "withdraw", r.Withdraw, "err", err)
				// Continue — weft-network's next reconcile re-emits.
			}
		}
	}
}
