package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openweft/weft-router/internal/bgp"
	"github.com/openweft/weft-router/internal/config"
	"github.com/openweft/weft-router/internal/fib"
	"github.com/openweft/weft-router/internal/metrics"
	"github.com/openweft/weft-router/internal/statusemitter"
	"github.com/openweft/weft-router/internal/subscriber"
	"github.com/spf13/cobra"
)

// newAgentCmd wires the daemon : load static bootstrap config → start the
// BGP server (stopped, no peers yet) → start the FIB programmer (subscribed
// to BGP RIB changes) → start the NATS subscriber that applies live config
// → expose /metrics → block until SIGINT/SIGTERM.
//
// Each component goroutine cooperates with the cancel context so a single
// signal tears the whole tree down in order : subscriber first (stop
// accepting updates), then BGP (stop sessions cleanly, send notifications),
// then FIB (no-op, kernel keeps routes ; weft-network will reissue them
// on the next agent).
func newAgentCmd() *cobra.Command {
	var (
		configPath     string
		metricsAddr    string
		shutdownGrace  time.Duration
		statusInterval time.Duration
	)
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run the weft-router data-plane daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAgent(cmd.Context(), agentOptions{
				configPath:     configPath,
				metricsAddr:    metricsAddr,
				shutdownGrace:  shutdownGrace,
				statusInterval: statusInterval,
			})
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "/etc/weft-router/config.hcl",
		"Path to the static bootstrap config (NATS endpoint, tenant subject, …)")
	cmd.Flags().StringVar(&metricsAddr, "metrics-addr", ":9100",
		"Address the Prometheus /metrics endpoint listens on (separate from data-plane)")
	cmd.Flags().DurationVar(&shutdownGrace, "shutdown-grace", 10*time.Second,
		"Maximum wait for BGP sessions to drain cleanly before forcing exit")
	cmd.Flags().DurationVar(&statusInterval, "status-interval", 10*time.Second,
		"How often the status emitter publishes RouterStatus to weft-network")
	return cmd
}

type agentOptions struct {
	configPath     string
	metricsAddr    string
	shutdownGrace  time.Duration
	statusInterval time.Duration
}

func runAgent(ctx context.Context, opts agentOptions) error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return fmt.Errorf("config %s: %w", opts.configPath, err)
	}
	log.Info("weft-router starting",
		"tenant", cfg.TenantUUID, "asn", cfg.LocalASN, "router_id", cfg.RouterID)

	// Lifecycle context — SIGINT/SIGTERM cancels everything downstream.
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// BGP server : in-process GoBGP instance, no peers configured yet.
	// Peers are applied later by the subscriber when weft-network publishes
	// the tenant config.
	bgpSrv, err := bgp.New(log, cfg)
	if err != nil {
		return fmt.Errorf("bgp: %w", err)
	}
	if err := bgpSrv.Start(ctx); err != nil {
		return fmt.Errorf("bgp start: %w", err)
	}

	// FIB programmer : reads route updates off bgpSrv's RIB watch and pushes
	// RTM_NEWROUTE / RTM_DELROUTE to the kernel via netlink. Runs in its own
	// goroutine ; errors bubble back through the context.
	prog := fib.NewProgrammer(log, bgpSrv)
	go func() {
		if err := prog.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("fib programmer exited", "err", err)
			cancel()
		}
	}()

	// NATS subscriber : the live-config plane. Pattern aligned with
	// weft-microvm-agent (Subscriber+ApplyFunc per concern, subject par-VM,
	// idempotent reconcile on every message — even if it duplicates).
	sub, err := subscriber.New(log, cfg, bgpSrv)
	if err != nil {
		return fmt.Errorf("subscriber: %w", err)
	}
	go func() {
		if err := sub.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("subscriber exited", "err", err)
			cancel()
		}
	}()

	// Status emitter : reverse direction of the subscriber. Polls the
	// BGP server for peer state every opts.statusInterval and publishes
	// RouterStatus on weft.router.<tenant>.status. weft-network's
	// statusreceiver picks that up and refreshes Router.Status /
	// PeerState in the store. Best-effort like the rest of the data
	// plane — outages and races are tolerated.
	em, err := statusemitter.New(log, cfg, bgpSrv, opts.statusInterval)
	if err != nil {
		return fmt.Errorf("statusemitter: %w", err)
	}
	go func() {
		if err := em.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("statusemitter exited", "err", err)
			cancel()
		}
	}()

	// /metrics : Prometheus scrape on a dedicated port, separate fate from
	// the data plane. weft-network's otel-collector scrape walks every
	// weft-router microVM via the mesh address.
	metricsSrv := metrics.NewServer(opts.metricsAddr)
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics listener exited", "err", err)
			cancel()
		}
	}()

	log.Info("weft-router ready", "metrics", opts.metricsAddr)
	<-ctx.Done()
	log.Info("weft-router shutting down")

	// Drain in reverse order — subscriber first (stop new updates), BGP
	// next (graceful notifications to peers), FIB last (no-op, kernel keeps
	// routes until weft-network reissues them).
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), opts.shutdownGrace)
	defer shutdownCancel()
	_ = metricsSrv.Shutdown(shutdownCtx)
	_ = bgpSrv.Stop(shutdownCtx)
	return nil
}
