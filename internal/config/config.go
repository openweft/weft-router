// Package config models the static bootstrap config weft-router reads at
// startup. Anything dynamic (peer list, route policies, prefix advertisements)
// arrives later over NATS via the subscriber package.
package config

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
)

// Config is the bootstrap manifest. The HCL on disk gets decoded to this
// struct ; we accept JSON too so the test fixtures stay dep-free.
type Config struct {
	// TenantUUID identifies the tenant this router serves. Used to build
	// the NATS subject the subscriber listens on.
	TenantUUID string `json:"tenant_uuid"`

	// LocalASN is the tenant's BGP autonomous system number (the public
	// ASN they brought, e.g. AS65501). 4-byte ASN supported.
	LocalASN uint32 `json:"local_asn"`

	// RouterID is the BGP router-id, typically the router's mesh address.
	// Per RFC 4271 it's a 32-bit identifier ; we accept any IP that fits.
	RouterID netip.Addr `json:"router_id"`

	// NATSURL is the address of the NATS cluster the subscriber dials
	// (e.g. "nats://nats.weft.internal:4222"). Auth via NKey, seed is
	// dropped by the host adapter into /run/weft/nats/nats.nkey ; the
	// subscriber reads that path directly, not this config.
	NATSURL string `json:"nats_url"`

	// ListenAddress is the IP+port BGP binds for inbound peer sessions.
	// Default ":179" — the router microVM's veth IP is the mesh address.
	ListenAddress string `json:"listen_address,omitempty"`
}

// Load reads a config file from disk. Accepts JSON ; an HCL decoder
// can be plugged in by callers that want HCL ergonomics — we keep the
// dep surface minimal here.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate enforces the invariants required to start the BGP server.
// Pure function — exercised by tests without touching disk.
func (c *Config) Validate() error {
	if c.TenantUUID == "" {
		return fmt.Errorf("tenant_uuid is required")
	}
	if c.LocalASN == 0 {
		return fmt.Errorf("local_asn must be non-zero")
	}
	if !c.RouterID.IsValid() {
		return fmt.Errorf("router_id must be a valid IP address")
	}
	if c.NATSURL == "" {
		return fmt.Errorf("nats_url is required")
	}
	if c.ListenAddress == "" {
		c.ListenAddress = ":179"
	}
	return nil
}
