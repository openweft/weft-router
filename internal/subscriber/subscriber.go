// Package subscriber listens on the per-tenant NATS subject
// "weft.router.<tenant-uuid>.config" and reconciles the BGP server with
// each new desired-state message.
//
// The pattern is the same Subscriber+ApplyFunc that weft-microvm-agent
// uses for WireGuard mesh, FUSE/SFTP mounts, etc. ([[guest-dynamic-config]]
// in the project memory) : one subject per concern, retained on the
// stream so a freshly-booted agent gets the last desired-state replay
// without weft-network having to chase it, every apply is idempotent so
// duplicate deliveries don't hurt.
package subscriber

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/openweft/weft-router/internal/bgp"
	"github.com/openweft/weft-router/internal/config"
)

// DesiredState is the JSON payload weft-network publishes. The router
// applies it whole each time — no field-level diffing here, that's BGP's
// job downstream.
type DesiredState struct {
	Peers    []bgp.PeerConfig            `json:"peers"`
	Prefixes []bgp.PrefixAdvertisement   `json:"prefixes"`
}

// Subscriber binds the NATS subject to a bgp.Server.
type Subscriber struct {
	log *slog.Logger
	cfg *config.Config
	bgp *bgp.Server
}

// New constructs a Subscriber. The NATS connection itself is dialed
// inside Run so a transient NATS outage at startup doesn't crash the
// daemon — Run keeps retrying with backoff.
func New(log *slog.Logger, cfg *config.Config, b *bgp.Server) (*Subscriber, error) {
	if cfg == nil {
		return nil, fmt.Errorf("subscriber.New: nil config")
	}
	return &Subscriber{log: log, cfg: cfg, bgp: b}, nil
}

// Subject returns the NATS subject this subscriber listens on. Exposed
// for tests and for the metrics labeller.
func (s *Subscriber) Subject() string {
	return fmt.Sprintf("weft.router.%s.config", s.cfg.TenantUUID)
}

// Run blocks until ctx is cancelled. Stub for now : logs the subject,
// then waits.
//
// TODO: dial nats with the NKey seed at /run/weft/nats/nats.nkey, subscribe
// to s.Subject(), and on each message decode the DesiredState then call
// bgp.ApplyPeers + bgp.ApplyPrefixes. Match weft-microvm-agent's reconnect
// + backoff loop ; never panic on a malformed message, just log and skip.
func (s *Subscriber) Run(ctx context.Context) error {
	s.log.Info("subscriber starting (stub)",
		"subject", s.Subject(), "nats_url", s.cfg.NATSURL)
	<-ctx.Done()
	return ctx.Err()
}
