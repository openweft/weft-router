package config

import (
	"net/netip"
	"testing"
)

func TestValidate(t *testing.T) {
	good := Config{
		TenantUUID: "t1",
		LocalASN:   65501,
		RouterID:   netip.MustParseAddr("10.0.0.1"),
		NATSURL:    "nats://nats:4222",
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if good.ListenAddress != ":179" {
		t.Errorf("ListenAddress default not applied: %q", good.ListenAddress)
	}

	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"empty tenant", func(c *Config) { c.TenantUUID = "" }},
		{"zero asn", func(c *Config) { c.LocalASN = 0 }},
		{"invalid router id", func(c *Config) { c.RouterID = netip.Addr{} }},
		{"empty nats url", func(c *Config) { c.NATSURL = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := good
			tc.mut(&c)
			if err := c.Validate(); err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}
