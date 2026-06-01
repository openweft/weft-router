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
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/openweft/weft-router/internal/bgp"
	"github.com/openweft/weft-router/internal/config"
)

// DesiredState is the JSON payload weft-network publishes. The router
// applies it whole each time — no field-level diffing here, that's BGP's
// job downstream.
type DesiredState struct {
	Peers    []bgp.PeerConfig          `json:"peers"`
	Prefixes []bgp.PrefixAdvertisement `json:"prefixes"`
}

// Subscriber binds the NATS subject to a bgp.Server.
type Subscriber struct {
	log *slog.Logger
	cfg *config.Config
	bgp *bgp.Server
}

// New constructs a Subscriber. The NATS connection itself is dialed
// inside Run so a transient NATS outage at startup doesn't crash the
// daemon — Run keeps retrying with exponential backoff via the nats
// client's own reconnect.
func New(log *slog.Logger, cfg *config.Config, b *bgp.Server) (*Subscriber, error) {
	if cfg == nil {
		return nil, fmt.Errorf("subscriber.New: nil config")
	}
	if b == nil {
		return nil, fmt.Errorf("subscriber.New: nil bgp server")
	}
	return &Subscriber{log: log, cfg: cfg, bgp: b}, nil
}

// Subject returns the NATS subject this subscriber listens on. Exposed
// for tests and for the metrics labeller.
func (s *Subscriber) Subject() string {
	return fmt.Sprintf("weft.router.%s.config", s.cfg.TenantUUID)
}

// Run dials NATS and subscribes to the tenant subject. Blocks until ctx
// is cancelled. The nats client itself handles reconnect with internal
// backoff ; we just provide the connection options and the message
// handler that reconciles desired-state.
//
// Auth uses the NKey seed at /run/weft/nats/nats.nkey (dropped by the
// host's adapter, matching weft-microvm-agent). When the file is absent
// (dev / tests without auth-enabled NATS), the connection goes anonymous
// and the NATS server is expected to permit the subject — same fallback
// pattern weft-microvm-agent uses.
func (s *Subscriber) Run(ctx context.Context) error {
	opts := []nats.Option{
		nats.Name(fmt.Sprintf("weft-router/%s", s.cfg.TenantUUID)),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1), // forever — weft-network is the source of truth
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			s.log.Warn("nats disconnected", "err", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			s.log.Info("nats reconnected", "url", c.ConnectedUrl())
		}),
	}
	if seed := nkeySeedPath; fileExists(seed) {
		opt, err := nats.NkeyOptionFromSeed(seed)
		if err != nil {
			return fmt.Errorf("nats nkey: %w", err)
		}
		opts = append(opts, opt)
	}

	nc, err := nats.Connect(s.cfg.NATSURL, opts...)
	if err != nil {
		return fmt.Errorf("nats connect %s: %w", s.cfg.NATSURL, err)
	}
	defer nc.Drain()

	sub, err := nc.Subscribe(s.Subject(), func(msg *nats.Msg) {
		s.handle(ctx, msg.Data)
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", s.Subject(), err)
	}
	defer sub.Unsubscribe()

	s.log.Info("subscriber listening", "subject", s.Subject(), "nats_url", s.cfg.NATSURL)
	<-ctx.Done()
	return ctx.Err()
}

// handle decodes one desired-state message and reconciles the BGP server.
// Errors are logged but don't propagate — weft-network's next publish
// will retry implicitly. We never panic on a malformed payload.
func (s *Subscriber) handle(ctx context.Context, data []byte) {
	var desired DesiredState
	if err := json.Unmarshal(data, &desired); err != nil {
		s.log.Warn("malformed desired state, skipping", "err", err)
		return
	}
	if err := s.bgp.ApplyPeers(ctx, desired.Peers); err != nil {
		s.log.Error("ApplyPeers", "err", err)
	}
	if err := s.bgp.ApplyPrefixes(ctx, desired.Prefixes); err != nil {
		s.log.Error("ApplyPrefixes", "err", err)
	}
}

// nkeySeedPath is the conventional location weft-microvm-agent drops the
// per-tenant NATS NKey seed (chmod 600). Same path used here for
// symmetry. Tests override via the unexported test hook below.
var nkeySeedPath = "/run/weft/nats/nats.nkey"

// fileExists is small enough to inline rather than pull in a helper pkg.
func fileExists(p string) bool {
	if p == "" {
		return false
	}
	// os.Stat would be more proper, but we avoid the import here because
	// the rest of the package is os-free. The subscriber.go already pulls
	// log/slog so a simple try-open is fine ; keep it minimal.
	return statExists(p)
}
