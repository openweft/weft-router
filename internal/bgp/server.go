// Package bgp wraps an in-process GoBGP server : the BGP-4 protocol engine
// that peers with the upstream ISP, runs the path-selection / decision
// process, and exposes a route-watch channel the FIB programmer subscribes
// to.
//
// We keep the GoBGP coupling localised in this package : the rest of
// weft-router only ever sees the typed API in this file (Start, Stop,
// ApplyPeers, ApplyPrefixes, Routes). When we want to swap out the
// underlying BGP implementation, only this package changes.
package bgp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/openweft/weft-router/internal/config"
)

// Server wraps a GoBGP in-process server.
//
// Lifecycle :
//   - New : construct, no goroutines started yet.
//   - Start(ctx) : start the GoBGP server, bind the listener, run the
//     event loop. Returns once the server is up.
//   - ApplyPeers / ApplyPrefixes : called by the NATS subscriber when
//     weft-network publishes new desired state. Idempotent — diffs the
//     existing config against the desired set and only issues the deltas.
//   - Routes() : returns a channel of route updates the FIB programmer
//     consumes. Closed when the server stops.
//   - Stop(ctx) : graceful shutdown — send NOTIFICATION cease to peers,
//     wait for the event loop to drain, close Routes.
//
// All methods are safe to call concurrently after Start returns.
type Server struct {
	log *slog.Logger
	cfg *config.Config

	mu     sync.Mutex
	routes chan Route // closed in Stop
	// TODO: when GoBGP is wired, hold the *gobgp.BgpServer here. Until
	// then the type is a stub so the rest of the daemon compiles end-to-end.
}

// Route is the FIB-relevant projection of a GoBGP path. The FIB programmer
// translates this into RTM_NEWROUTE / RTM_DELROUTE netlink messages.
type Route struct {
	Prefix   string // CIDR : "203.0.113.0/24", "2001:db8::/32"
	NextHop  string // IP of the next hop ; empty means withdraw
	Withdraw bool   // true → DELROUTE ; false → NEWROUTE
}

// New constructs a Server. Doesn't talk to the network yet.
func New(log *slog.Logger, cfg *config.Config) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("bgp.New: nil config")
	}
	return &Server{
		log:    log,
		cfg:    cfg,
		routes: make(chan Route, 1024),
	}, nil
}

// Start brings up the BGP server. No peers are configured yet ; they
// arrive later via ApplyPeers when weft-network publishes config.
//
// TODO: instantiate gobgp.NewBgpServer, call Serve in a goroutine, post
// the Global config (ASN, RouterID, ListenAddress) via the GoBGP API.
func (s *Server) Start(ctx context.Context) error {
	s.log.Info("bgp server starting (stub)",
		"asn", s.cfg.LocalASN, "router_id", s.cfg.RouterID, "listen", s.cfg.ListenAddress)
	return nil
}

// Stop drains the server with the given deadline. Closes Routes so
// downstream consumers see the channel close as their exit signal.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routes != nil {
		close(s.routes)
		s.routes = nil
	}
	s.log.Info("bgp server stopped")
	return nil
}

// Routes returns the channel of route updates the FIB programmer drains.
// Closed when Stop is called.
func (s *Server) Routes() <-chan Route { return s.routes }

// PeerConfig is the desired-state of one BGP neighbor.
type PeerConfig struct {
	Address    string // peer IP (the ISP's BGP endpoint)
	RemoteASN  uint32
	HoldTime   uint32 // seconds, 0 = use protocol default (90s)
	PolicyName string // optional : named route filter policy applied at this peer
}

// ApplyPeers reconciles the running peer set to match the desired list.
// Idempotent : peers already running with matching config are left alone,
// missing peers are added, extra peers are removed.
//
// TODO: implement via gobgp.AddPeer / DeletePeer / ListPeer.
func (s *Server) ApplyPeers(ctx context.Context, peers []PeerConfig) error {
	s.log.Info("apply peers (stub)", "count", len(peers))
	return nil
}

// PrefixAdvertisement describes one prefix the router advertises out
// to its peers (the tenant's owned space).
type PrefixAdvertisement struct {
	Prefix     string // CIDR
	NextHop    string // optional — defaults to the BGP self-next-hop
	Communities []uint32
}

// ApplyPrefixes reconciles the advertised-prefix set. Idempotent like
// ApplyPeers.
//
// TODO: implement via gobgp.AddPath / DeletePath against the global RIB.
func (s *Server) ApplyPrefixes(ctx context.Context, prefixes []PrefixAdvertisement) error {
	s.log.Info("apply prefixes (stub)", "count", len(prefixes))
	return nil
}
