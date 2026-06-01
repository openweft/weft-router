// Package statusemitter publishes this micro-VM's live BGP state on the
// per-tenant NATS subject weft-network listens to.
//
// Reverse direction of internal/subscriber : where the subscriber pulls
// DesiredState (peers + prefixes to advertise) on
// weft.router.<tenant-uuid>.config, the emitter pushes RouterStatus
// (live peer states + RIB counts) on weft.router.<tenant-uuid>.status.
// weft-network's statusreceiver decodes that and updates the Router
// resource's Status / PeerState live-state fields.
//
// Cadence : a ticker (default 10 s) walks bgp.Server.PeerStatusList
// and publishes whatever it sees, plus once at Run() entry so the
// dashboard sees the first state inside a second of boot. Best-effort
// — publish failures log + skip, the next tick reconciles.
package statusemitter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/openweft/weft-router/internal/bgp"
	"github.com/openweft/weft-router/internal/config"
)

// PeerStatus is the JSON projection of bgp.PeerStatus. Duplicated
// across the wire-contract boundary rather than imported (weft-network
// pulls the same shape from its own internal/statusreceiver package).
type PeerStatus struct {
	Address          string `json:"Address"`
	State            string `json:"State"`
	UptimeSec        int64  `json:"UptimeSec"`
	ReceivedPrefixes int    `json:"ReceivedPrefixes"`
}

// RouterStatus is the payload published on each tick.
type RouterStatus struct {
	Overall         string       `json:"Overall"`
	Peers           []PeerStatus `json:"Peers"`
	RoutesInstalled int          `json:"RoutesInstalled"`
	PublishedAtUnix int64        `json:"PublishedAtUnix"`
}

// Emitter polls the BGP server and publishes RouterStatus on NATS.
//
// Owns its own nats.Conn (parallel to subscriber.Subscriber). Costs
// one extra TCP session per micro-VM today ; consolidating into a
// single shared conn is a future refactor when there's a 3rd caller.
type Emitter struct {
	log      *slog.Logger
	cfg      *config.Config
	bgp      *bgp.Server
	interval time.Duration
}

// New constructs an Emitter. Doesn't dial NATS yet ; Run does that so
// a transient NATS outage at startup doesn't crash the daemon.
func New(log *slog.Logger, cfg *config.Config, bs *bgp.Server, interval time.Duration) (*Emitter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("statusemitter.New: nil config")
	}
	if bs == nil {
		return nil, fmt.Errorf("statusemitter.New: nil bgp server")
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	return &Emitter{log: log, cfg: cfg, bgp: bs, interval: interval}, nil
}

// Subject returns the NATS subject this emitter publishes on. Exposed
// so the wire-up test in agent.go can assert against the matching
// statusreceiver pattern.
func (e *Emitter) Subject() string {
	return fmt.Sprintf("weft.router.%s.status", e.cfg.TenantUUID)
}

// Run dials NATS, publishes once on entry (so the dashboard sees boot
// state sub-second), then on every tick until ctx is cancelled.
// Returns ctx.Err() so the agent's goroutine logging treats a clean
// shutdown as "exited" rather than "failed".
//
// NATS auth mirrors subscriber : reads /run/weft/nats/nats.nkey when
// present, anonymous otherwise. nats.go owns reconnect with forever
// retry, so a transient NATS outage just delays a few ticks.
func (e *Emitter) Run(ctx context.Context) error {
	opts := []nats.Option{
		nats.Name(fmt.Sprintf("weft-router/%s/status", e.cfg.TenantUUID)),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			e.log.Warn("nats disconnected (emitter)", "err", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			e.log.Info("nats reconnected (emitter)", "url", c.ConnectedUrl())
		}),
	}
	if statExists(nkeySeedPath) {
		opt, err := nats.NkeyOptionFromSeed(nkeySeedPath)
		if err != nil {
			return fmt.Errorf("nats nkey: %w", err)
		}
		opts = append(opts, opt)
	}
	nc, err := nats.Connect(e.cfg.NATSURL, opts...)
	if err != nil {
		return fmt.Errorf("nats connect %s: %w", e.cfg.NATSURL, err)
	}
	defer nc.Drain()

	e.log.Info("statusemitter starting", "subject", e.Subject(), "interval", e.interval)
	e.tick(ctx, nc)
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			e.tick(ctx, nc)
		}
	}
}

// tick captures peer state, builds RouterStatus, publishes on nc.
// Errors are logged but never propagate — the next tick reconciles.
func (e *Emitter) tick(ctx context.Context, nc *nats.Conn) {
	peers, err := e.bgp.PeerStatusList(ctx)
	if err != nil {
		e.log.Warn("peer status list failed", "err", err)
		return
	}
	status := RouterStatus{
		Overall:         rollupOverall(peers),
		Peers:           toJSONPeers(peers),
		RoutesInstalled: e.bgp.AdvertisedPrefixCount(),
		PublishedAtUnix: time.Now().Unix(),
	}
	payload, err := json.Marshal(status)
	if err != nil {
		// Marshal can only fail on unsupported types ; ours are all
		// plain scalars. Defensive log.
		e.log.Error("status marshal failed (shouldn't happen)", "err", err)
		return
	}
	if err := nc.Publish(e.Subject(), payload); err != nil {
		e.log.Warn("status publish failed", "subject", e.Subject(), "err", err)
		return
	}
	e.log.Debug("status published",
		"subject", e.Subject(), "overall", status.Overall, "peers", len(peers))
}

// nkeySeedPath mirrors subscriber's so a single NKey works for both
// directions. Tests override via package-private assignment.
var nkeySeedPath = "/run/weft/nats/nats.nkey"

// toJSONPeers converts the bgp.PeerStatus slice to the JSON shape.
// Kept distinct from bgp.PeerStatus so a future change to bgp's
// internal fields doesn't force a wire-shape change.
func toJSONPeers(in []bgp.PeerStatus) []PeerStatus {
	out := make([]PeerStatus, len(in))
	for i, p := range in {
		out[i] = PeerStatus{
			Address:          p.Address,
			State:            p.State,
			UptimeSec:        p.UptimeSec,
			ReceivedPrefixes: p.ReceivedPrefixes,
		}
	}
	return out
}

// rollupOverall mirrors weft-network/internal/statusreceiver's
// rollupStatus — emitted server-side for completeness, but the
// receiver re-derives it from Peers anyway so a buggy emitter can't
// pin the dashboard. Pure ; tested.
func rollupOverall(peers []bgp.PeerStatus) string {
	if len(peers) == 0 {
		return "configuring"
	}
	anyEstablished := false
	anyNegotiating := false
	for _, p := range peers {
		switch p.State {
		case "Established":
			anyEstablished = true
		case "OpenSent", "OpenConfirm", "Active":
			anyNegotiating = true
		}
	}
	switch {
	case anyEstablished:
		return "active"
	case anyNegotiating:
		return "configuring"
	default:
		return "down"
	}
}
