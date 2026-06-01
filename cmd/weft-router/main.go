// Command weft-router is the per-tenant-ASN BGP speaker + FIB programmer
// that runs as a dedicated micro-VM in the openweft data plane.
//
// One weft-router micro-VM per tenant that needs BGP egress (its own public
// ASN). It peers with the upstream ISP using [GoBGP] (Apache 2.0, pure-Go,
// the same engine Calico and Cilium ship), filters received routes per
// tenant policy, and installs them in the Linux kernel FIB via [netlink].
// Tenant prefixes are advertised back to the ISP.
//
// Config flow : weft-network publishes the tenant's BGP config on the NATS
// subject "weft.router.<tenant-uuid>.config" (retained) ; weft-router
// subscribes and reconciles using the same Subscriber+ApplyFunc pattern
// weft-microvm-agent uses for WireGuard mesh and mounts. Static bootstrap
// config (the NATS endpoint, the per-tenant subject) is read from
// /etc/weft-router/config.hcl mounted via virtio-fs / 9p.
//
// VyOS / OPNsense / FRR remain available as classic VMs (via
// "weft instance") for tenants that need multi-protocol routing
// (OSPF / IS-IS / RSVP-TE) or want to bring their own router OS
// config — see the project memory entry "project-router-lb-gonative".
//
// [GoBGP]: https://osrg.github.io/gobgp/
// [netlink]: https://pkg.go.dev/github.com/vishvananda/netlink
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Build-time stamps populated via -ldflags "-X main.version=…". Same scheme
// weft-network uses, so the operator gets the same UX across daemons.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "weft-router",
		Short: "BGP speaker + FIB programmer for a single tenant ASN",
		Long: `weft-router is the Go-native router that ships as a micro-VM in the
openweft data plane. It runs GoBGP for BGP-4 + EVPN + flowspec peering with
the upstream ISP, filters received routes per tenant policy, and installs
them in the Linux kernel FIB via netlink. weft-network publishes the
desired-state config over NATS ; weft-router reconciles.`,
		SilenceUsage: true,
		Version:      fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
	}
	root.AddCommand(newAgentCmd())
	return root
}
