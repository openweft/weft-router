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
	"net"
	"strconv"
	"sync"

	api "github.com/osrg/gobgp/v3/api"
	bgpserver "github.com/osrg/gobgp/v3/pkg/server"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/openweft/weft-router/internal/config"
)

// Server wraps a GoBGP in-process BgpServer.
//
// Lifecycle :
//   - New : construct, no goroutines started yet.
//   - Start(ctx) : start the GoBGP server (Serve goroutine), bind the
//     listener (or skip it when ListenAddress port is 0/unset, which is
//     the test-friendly mode), kick off the route-watch goroutine.
//   - ApplyPeers / ApplyPrefixes : called by the NATS subscriber when
//     weft-network publishes new desired state. Idempotent — diffs the
//     existing peers against the desired set and only issues the deltas.
//   - Routes() : channel of route updates the FIB programmer consumes.
//     Closed when Stop is called.
//   - Stop(ctx) : graceful shutdown — StopBgp sends NOTIFICATION cease
//     to peers, then Stop drains the event loop. Routes() closes once
//     the watcher returns.
//
// All methods are safe to call concurrently after Start returns.
type Server struct {
	log *slog.Logger
	cfg *config.Config

	mu        sync.Mutex
	bgp       *bgpserver.BgpServer
	routes    chan Route
	stopWatch context.CancelFunc // cancels the WatchEvent loop
	serveDone chan struct{}      // closed when bgp.Serve() returns

	// advertised tracks the prefixes we've pushed to GoBGP via AddPath.
	// Keyed by prefix CIDR ; value is the api.Path object (kept so we
	// can DeletePath against the same NLRI when reconciling a removal).
	// Avoids round-tripping ListPath on every Apply.
	advertised map[string]*api.Path
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
		log:        log,
		cfg:        cfg,
		routes:     make(chan Route, 1024),
		advertised: make(map[string]*api.Path),
	}, nil
}

// Start brings up the BGP server. No peers are configured yet ; they
// arrive via ApplyPeers when weft-network publishes the tenant config.
//
// Binds the BGP listener iff cfg.ListenAddress carries a non-zero port.
// In test / dev contexts (empty or :0), ListenPort is set to -1 which
// disables the listener — GoBGP still accepts outbound peers, just
// won't try to bind privileged port 179.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bgp != nil {
		return fmt.Errorf("bgp: already started")
	}

	s.bgp = bgpserver.NewBgpServer()
	s.serveDone = make(chan struct{})
	go func() {
		s.bgp.Serve()
		close(s.serveDone)
	}()

	listenPort := listenPortFromAddr(s.cfg.ListenAddress)
	if err := s.bgp.StartBgp(ctx, &api.StartBgpRequest{
		Global: &api.Global{
			Asn:        s.cfg.LocalASN,
			RouterId:   s.cfg.RouterID.String(),
			ListenPort: listenPort,
		},
	}); err != nil {
		s.bgp.Stop()
		<-s.serveDone
		s.bgp = nil
		return fmt.Errorf("StartBgp: %w", err)
	}

	// Kick off the path-watch goroutine. WatchEvent blocks until its ctx
	// is cancelled — we own a child context so Stop can tear it down
	// without disturbing the caller's ctx.
	watchCtx, cancel := context.WithCancel(context.Background())
	s.stopWatch = cancel
	go s.watchPaths(watchCtx)

	s.log.Info("bgp server started",
		"asn", s.cfg.LocalASN, "router_id", s.cfg.RouterID, "listen_port", listenPort)
	return nil
}

// Stop drains the server with the given deadline.
//
// Order : stop the watcher first (no more Route emissions), then StopBgp
// (sends NOTIFICATION cease to peers), then Stop the event loop. Routes()
// closes last so downstream consumers see the channel-close as exit.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bgp == nil {
		return nil
	}

	if s.stopWatch != nil {
		s.stopWatch()
	}
	// StopBgp sends NOTIFICATION cease to every peer. Best-effort : log
	// but don't fail the Stop if the global is already torn down.
	if err := s.bgp.StopBgp(ctx, &api.StopBgpRequest{}); err != nil {
		s.log.Warn("StopBgp", "err", err)
	}
	s.bgp.Stop()
	if s.serveDone != nil {
		<-s.serveDone
	}
	if s.routes != nil {
		close(s.routes)
		s.routes = nil
	}
	s.bgp = nil
	s.log.Info("bgp server stopped")
	return nil
}

// Routes returns the channel of route updates the FIB programmer drains.
// Closed when Stop is called.
func (s *Server) Routes() <-chan Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.routes
}

// watchPaths blocks on WatchEvent and forwards each best-path event to
// the Routes channel. Exits cleanly when ctx is cancelled (Stop).
func (s *Server) watchPaths(ctx context.Context) {
	req := &api.WatchEventRequest{
		Table: &api.WatchEventRequest_Table{
			Filters: []*api.WatchEventRequest_Table_Filter{
				{Type: api.WatchEventRequest_Table_Filter_BEST},
			},
		},
	}
	err := s.bgp.WatchEvent(ctx, req, func(r *api.WatchEventResponse) {
		t := r.GetTable()
		if t == nil {
			return
		}
		for _, p := range t.Paths {
			route, ok := routeFromPath(p)
			if !ok {
				continue
			}
			select {
			case s.routes <- route:
			case <-ctx.Done():
				return
			}
		}
	})
	if err != nil && ctx.Err() == nil {
		s.log.Error("WatchEvent exited", "err", err)
	}
}

// PeerConfig is the desired-state of one BGP neighbor.
type PeerConfig struct {
	Address    string // peer IP (the ISP's BGP endpoint)
	RemoteASN  uint32
	HoldTime   uint32 // seconds, 0 = use protocol default (90s)
	PolicyName string // optional : named route filter policy applied at this peer
}

// ApplyPeers reconciles the running peer set to match the desired list.
// Idempotent : peers already running with matching neighbor-address are
// kept, missing peers are added, extra peers are removed.
//
// We diff by neighbor address only ; if a caller mutates RemoteASN on an
// existing peer, the delta misses it. That's fine for the scaffold —
// weft-network treats router config as "delete + recreate" on changes
// rather than in-place edits.
func (s *Server) ApplyPeers(ctx context.Context, peers []PeerConfig) error {
	s.mu.Lock()
	bgp := s.bgp
	s.mu.Unlock()
	if bgp == nil {
		return fmt.Errorf("bgp: not started")
	}

	have := map[string]bool{}
	if err := bgp.ListPeer(ctx, &api.ListPeerRequest{}, func(p *api.Peer) {
		if p.Conf != nil {
			have[p.Conf.NeighborAddress] = true
		}
	}); err != nil {
		return fmt.Errorf("ListPeer: %w", err)
	}

	want := map[string]bool{}
	for _, p := range peers {
		want[p.Address] = true
	}

	// Add desired peers that aren't present.
	for _, p := range peers {
		if have[p.Address] {
			continue
		}
		err := bgp.AddPeer(ctx, &api.AddPeerRequest{
			Peer: &api.Peer{
				Conf: &api.PeerConf{
					NeighborAddress: p.Address,
					PeerAsn:         p.RemoteASN,
				},
			},
		})
		if err != nil {
			return fmt.Errorf("AddPeer %s: %w", p.Address, err)
		}
		s.log.Info("peer added", "address", p.Address, "asn", p.RemoteASN)
	}

	// Delete running peers that aren't desired.
	for addr := range have {
		if want[addr] {
			continue
		}
		err := bgp.DeletePeer(ctx, &api.DeletePeerRequest{Address: addr})
		if err != nil {
			return fmt.Errorf("DeletePeer %s: %w", addr, err)
		}
		s.log.Info("peer removed", "address", addr)
	}
	return nil
}

// PeerStatus is the live state of one BGP neighbour, projected from
// GoBGP's api.PeerState. Emitted by the statusemitter to weft-network.
type PeerStatus struct {
	Address          string // peer IP
	State            string // BGP-4 session state per RFC 4271 §8 :
	//                       "Idle" | "Connect" | "Active" | "OpenSent"
	//                       | "OpenConfirm" | "Established"
	UptimeSec        int64  // seconds since last state transition to Established (0 otherwise)
	ReceivedPrefixes int    // RIB-in count from this neighbour
}

// PeerStatusList queries GoBGP for the live state of every configured
// peer and returns the FIB-friendly projection. Pure read — doesn't
// mutate peers. Suitable for periodic polling by the statusemitter.
func (s *Server) PeerStatusList(ctx context.Context) ([]PeerStatus, error) {
	s.mu.Lock()
	bgp := s.bgp
	s.mu.Unlock()
	if bgp == nil {
		return nil, fmt.Errorf("bgp: not started")
	}
	var out []PeerStatus
	err := bgp.ListPeer(ctx, &api.ListPeerRequest{}, func(p *api.Peer) {
		ps := PeerStatus{}
		if p.Conf != nil {
			ps.Address = p.Conf.NeighborAddress
		}
		if p.State != nil {
			ps.State = normalisePeerState(p.State.SessionState.String())
		}
		// Uptime / received-prefix count live under different sub-types
		// depending on whether the session is up. Defensive nil checks
		// — GoBGP populates them lazily.
		if t := p.GetTimers(); t != nil && t.State != nil && t.State.Uptime != nil {
			ps.UptimeSec = t.State.Uptime.GetSeconds()
		}
		if afis := p.AfiSafis; len(afis) > 0 {
			for _, af := range afis {
				if af.State != nil {
					ps.ReceivedPrefixes += int(af.State.Received)
				}
			}
		}
		out = append(out, ps)
	})
	if err != nil {
		return nil, fmt.Errorf("ListPeer: %w", err)
	}
	return out, nil
}

// AdvertisedPrefixCount returns the number of prefixes weft-network
// asked us to advertise. Cheap, doesn't touch GoBGP — reads the local
// tracking map we keep in sync with ApplyPrefixes.
func (s *Server) AdvertisedPrefixCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.advertised)
}

// normalisePeerState maps GoBGP's all-caps enum names ("ESTABLISHED",
// "OPENSENT") to RFC 4271 §8 mixed-case ("Established", "OpenSent")
// so the JSON wire shape matches what weft-network/statusreceiver
// expects in its rollup logic. Unknown values pass through verbatim
// so a future GoBGP enum addition surfaces as itself rather than as
// the empty string.
func normalisePeerState(raw string) string {
	switch raw {
	case "IDLE":
		return "Idle"
	case "CONNECT":
		return "Connect"
	case "ACTIVE":
		return "Active"
	case "OPENSENT":
		return "OpenSent"
	case "OPENCONFIRM":
		return "OpenConfirm"
	case "ESTABLISHED":
		return "Established"
	default:
		return raw
	}
}

// PrefixAdvertisement describes one prefix the router advertises out
// to its peers (the tenant's owned space).
type PrefixAdvertisement struct {
	Prefix      string // CIDR
	NextHop     string // optional — defaults to the BGP self-next-hop
	Communities []uint32
}

// ApplyPrefixes reconciles the advertised-prefix set.
//
// Diff is done against an internal map (s.advertised) populated as we
// AddPath ; that avoids round-tripping ListPath every reconcile. The
// map keys are the prefix CIDR strings, the values are the api.Path
// we'd hand back to DeletePath. Idempotent like ApplyPeers — duplicate
// payloads from NATS just no-op past the first reconcile.
func (s *Server) ApplyPrefixes(ctx context.Context, prefixes []PrefixAdvertisement) error {
	s.mu.Lock()
	bgp := s.bgp
	s.mu.Unlock()
	if bgp == nil {
		return fmt.Errorf("bgp: not started")
	}

	want := map[string]PrefixAdvertisement{}
	for _, p := range prefixes {
		want[p.Prefix] = p
	}

	// Add prefixes we haven't advertised yet.
	for _, p := range prefixes {
		s.mu.Lock()
		_, already := s.advertised[p.Prefix]
		s.mu.Unlock()
		if already {
			continue
		}
		path, err := pathForAdvertisement(p)
		if err != nil {
			return fmt.Errorf("build path %s: %w", p.Prefix, err)
		}
		if _, err := bgp.AddPath(ctx, &api.AddPathRequest{
			TableType: api.TableType_GLOBAL,
			Path:      path,
		}); err != nil {
			return fmt.Errorf("AddPath %s: %w", p.Prefix, err)
		}
		s.mu.Lock()
		s.advertised[p.Prefix] = path
		s.mu.Unlock()
		s.log.Info("prefix advertised", "prefix", p.Prefix, "nexthop", p.NextHop)
	}

	// Withdraw prefixes we'd previously advertised that aren't in the
	// desired set anymore.
	s.mu.Lock()
	stale := make([]string, 0)
	for prefix := range s.advertised {
		if _, ok := want[prefix]; !ok {
			stale = append(stale, prefix)
		}
	}
	s.mu.Unlock()
	for _, prefix := range stale {
		s.mu.Lock()
		path := s.advertised[prefix]
		s.mu.Unlock()
		if err := bgp.DeletePath(ctx, &api.DeletePathRequest{
			TableType: api.TableType_GLOBAL,
			Path:      path,
		}); err != nil {
			return fmt.Errorf("DeletePath %s: %w", prefix, err)
		}
		s.mu.Lock()
		delete(s.advertised, prefix)
		s.mu.Unlock()
		s.log.Info("prefix withdrawn", "prefix", prefix)
	}
	return nil
}

// pathForAdvertisement builds the GoBGP api.Path for one advertisement.
// Encodes the NLRI (IPv4 or IPv6 prefix), Origin (IGP), optional
// NextHop, and optional Communities — the minimum a BGP-4 speaker needs
// to advertise a prefix and have peers accept it.
//
// Pure : no GoBGP server interaction, safe to unit-test from any host.
func pathForAdvertisement(p PrefixAdvertisement) (*api.Path, error) {
	ip, ipnet, err := net.ParseCIDR(p.Prefix)
	if err != nil {
		return nil, fmt.Errorf("parse prefix: %w", err)
	}
	prefixLen, _ := ipnet.Mask.Size()
	isV4 := ip.To4() != nil

	nlri, err := anypb.New(&api.IPAddressPrefix{
		Prefix:    ipnet.IP.String(),
		PrefixLen: uint32(prefixLen),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal nlri: %w", err)
	}

	attrs := make([]*anypb.Any, 0, 3)
	// Origin = IGP (0). RFC 4271 §4.3 — "the path information eligible
	// for the BGP decision process". IGP is the standard for tenant
	// advertised prefixes ; EGP/Incomplete are legacy.
	originAttr, err := anypb.New(&api.OriginAttribute{Origin: 0})
	if err != nil {
		return nil, fmt.Errorf("marshal origin: %w", err)
	}
	attrs = append(attrs, originAttr)

	if p.NextHop != "" {
		nhAttr, err := anypb.New(&api.NextHopAttribute{NextHop: p.NextHop})
		if err != nil {
			return nil, fmt.Errorf("marshal nexthop: %w", err)
		}
		attrs = append(attrs, nhAttr)
	}

	if len(p.Communities) > 0 {
		cAttr, err := anypb.New(&api.CommunitiesAttribute{Communities: p.Communities})
		if err != nil {
			return nil, fmt.Errorf("marshal communities: %w", err)
		}
		attrs = append(attrs, cAttr)
	}

	family := &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST}
	if !isV4 {
		family = &api.Family{Afi: api.Family_AFI_IP6, Safi: api.Family_SAFI_UNICAST}
	}
	return &api.Path{
		Nlri:   nlri,
		Pattrs: attrs,
		Family: family,
	}, nil
}

// routeFromPath converts a GoBGP api.Path to the FIB-friendly Route shape.
// Returns ok=false when the NLRI isn't an IPAddressPrefix we can program
// (e.g. EVPN, flowspec — supported by the peer side but not the FIB).
func routeFromPath(p *api.Path) (Route, bool) {
	if p == nil || p.Nlri == nil {
		return Route{}, false
	}
	var nlri api.IPAddressPrefix
	if err := p.Nlri.UnmarshalTo(&nlri); err != nil {
		return Route{}, false
	}
	r := Route{
		Prefix:   fmt.Sprintf("%s/%d", nlri.Prefix, nlri.PrefixLen),
		Withdraw: p.IsWithdraw,
	}
	// NextHop hides inside Pattrs (either NextHopAttribute or MpReachNLRI
	// for v6). Walk the attributes to find it ; first hit wins.
	for _, anyAttr := range p.Pattrs {
		var nh api.NextHopAttribute
		if err := anyAttr.UnmarshalTo(&nh); err == nil {
			r.NextHop = nh.NextHop
			break
		}
	}
	return r, true
}

// listenPortFromAddr extracts the port from a "host:port" or ":port"
// address. Returns -1 when the address is empty or has no port, which
// is the GoBGP convention for "don't bind a listener" — the test-friendly
// mode used in unit tests.
func listenPortFromAddr(addr string) int32 {
	if addr == "" {
		return -1
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(portStr)
	if err != nil || n == 0 {
		return -1
	}
	return int32(n)
}
