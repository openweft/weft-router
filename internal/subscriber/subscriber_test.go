package subscriber

import (
	"encoding/json"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/openweft/weft-router/internal/bgp"
	"github.com/openweft/weft-router/internal/config"
)

func TestSubject(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := &config.Config{
		TenantUUID: "ten-abcd",
		LocalASN:   65501,
		RouterID:   netip.MustParseAddr("10.0.0.1"),
		NATSURL:    "nats://localhost:4222",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}
	// bgp.New won't talk to the network ; we only need a non-nil Server.
	bs, err := bgp.New(log, cfg)
	if err != nil {
		t.Fatalf("bgp.New: %v", err)
	}
	s, err := New(log, cfg, bs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := s.Subject()
	want := "weft.router.ten-abcd.config"
	if got != want {
		t.Errorf("Subject() = %q, want %q", got, want)
	}
}

func TestDesiredStateRoundTrip(t *testing.T) {
	// The contract with weft-network is JSON on the wire. Lock it down
	// here so a future field rename / removal trips this test before it
	// lands as a silent decode failure in the subscriber's Run loop.
	in := DesiredState{
		Peers: []bgp.PeerConfig{
			{Address: "203.0.113.1", RemoteASN: 64512, HoldTime: 90},
		},
		Prefixes: []bgp.PrefixAdvertisement{
			{Prefix: "198.51.100.0/24", NextHop: "192.0.2.1"},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out DesiredState
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(out.Peers) != 1 || out.Peers[0].Address != "203.0.113.1" {
		t.Errorf("Peers round-trip: %+v", out.Peers)
	}
	if len(out.Prefixes) != 1 || out.Prefixes[0].Prefix != "198.51.100.0/24" {
		t.Errorf("Prefixes round-trip: %+v", out.Prefixes)
	}
}

func TestStatExists(t *testing.T) {
	dir := t.TempDir()
	exists := filepath.Join(dir, "yes")
	absent := filepath.Join(dir, "no")
	if err := os.WriteFile(exists, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !statExists(exists) {
		t.Errorf("statExists(%s) = false, want true", exists)
	}
	if statExists(absent) {
		t.Errorf("statExists(%s) = true, want false", absent)
	}
	if statExists("") {
		t.Errorf("statExists(\"\") = true, want false")
	}
}
