package bgp

import (
	"strings"
	"testing"

	api "github.com/osrg/gobgp/v3/api"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestListenPortFromAddr(t *testing.T) {
	cases := []struct {
		addr string
		want int32
	}{
		{"", -1},
		{":0", -1},     // explicit zero → no listen
		{":179", 179},
		{":bogus", -1}, // unparseable → no listen
		{"10.0.0.1:179", 179},
		{"[::]:179", 179},
		{"no-colon", -1}, // SplitHostPort fails
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			if got := listenPortFromAddr(tc.addr); got != tc.want {
				t.Errorf("listenPortFromAddr(%q) = %d, want %d", tc.addr, got, tc.want)
			}
		})
	}
}

func TestRouteFromPath_IPv4Install(t *testing.T) {
	nlri, _ := anypb.New(&api.IPAddressPrefix{Prefix: "10.20.0.0", PrefixLen: 16})
	nh, _ := anypb.New(&api.NextHopAttribute{NextHop: "10.0.0.1"})
	p := &api.Path{
		Nlri:       nlri,
		Pattrs:     []*anypb.Any{nh},
		IsWithdraw: false,
	}
	r, ok := routeFromPath(p)
	if !ok {
		t.Fatal("expected ok=true for IPAddressPrefix nlri")
	}
	if r.Prefix != "10.20.0.0/16" {
		t.Errorf("Prefix = %q, want 10.20.0.0/16", r.Prefix)
	}
	if r.NextHop != "10.0.0.1" {
		t.Errorf("NextHop = %q, want 10.0.0.1", r.NextHop)
	}
	if r.Withdraw {
		t.Error("Withdraw should be false")
	}
}

func TestRouteFromPath_Withdraw(t *testing.T) {
	nlri, _ := anypb.New(&api.IPAddressPrefix{Prefix: "203.0.113.0", PrefixLen: 24})
	p := &api.Path{Nlri: nlri, IsWithdraw: true}
	r, ok := routeFromPath(p)
	if !ok || !r.Withdraw {
		t.Errorf("expected withdraw=true, got ok=%v withdraw=%v", ok, r.Withdraw)
	}
	if r.Prefix != "203.0.113.0/24" {
		t.Errorf("Prefix = %q", r.Prefix)
	}
}

func TestRouteFromPath_NotIPPrefix(t *testing.T) {
	// EVPN / flowspec NLRIs shouldn't translate to a FIB-installable Route.
	// Synthesise an Any of the wrong type — Unmarshal into IPAddressPrefix
	// fails, routeFromPath returns ok=false.
	other, _ := anypb.New(&api.OriginAttribute{Origin: 0}) // not an NLRI type
	p := &api.Path{Nlri: other}
	if _, ok := routeFromPath(p); ok {
		t.Error("expected ok=false for non-IPAddressPrefix nlri")
	}
}

func TestRouteFromPath_NilSafe(t *testing.T) {
	if _, ok := routeFromPath(nil); ok {
		t.Error("expected ok=false for nil path")
	}
	if _, ok := routeFromPath(&api.Path{}); ok {
		t.Error("expected ok=false for path with nil Nlri")
	}
}

func TestPathForAdvertisement_V4(t *testing.T) {
	p, err := pathForAdvertisement(PrefixAdvertisement{
		Prefix:      "203.0.113.0/24",
		NextHop:     "192.0.2.1",
		Communities: []uint32{65000<<16 | 1},
	})
	if err != nil {
		t.Fatalf("pathForAdvertisement: %v", err)
	}
	if p.Family.Afi != api.Family_AFI_IP || p.Family.Safi != api.Family_SAFI_UNICAST {
		t.Errorf("Family = %v/%v, want IPv4 unicast", p.Family.Afi, p.Family.Safi)
	}
	// 3 attributes : Origin, NextHop, Communities. Order matters only for
	// the wire encoding but we asserted the BGP-4 minimum here.
	if len(p.Pattrs) != 3 {
		t.Errorf("attribute count = %d, want 3", len(p.Pattrs))
	}
	var nlri api.IPAddressPrefix
	if err := p.Nlri.UnmarshalTo(&nlri); err != nil {
		t.Fatalf("nlri unmarshal: %v", err)
	}
	if nlri.Prefix != "203.0.113.0" || nlri.PrefixLen != 24 {
		t.Errorf("nlri = %s/%d", nlri.Prefix, nlri.PrefixLen)
	}
}

func TestPathForAdvertisement_V6(t *testing.T) {
	p, err := pathForAdvertisement(PrefixAdvertisement{Prefix: "2001:db8::/32"})
	if err != nil {
		t.Fatalf("pathForAdvertisement: %v", err)
	}
	if p.Family.Afi != api.Family_AFI_IP6 {
		t.Errorf("Family.Afi = %v, want IP6", p.Family.Afi)
	}
	// No NextHop / Communities → only the mandatory Origin attribute.
	if len(p.Pattrs) != 1 {
		t.Errorf("attribute count = %d, want 1 (Origin only)", len(p.Pattrs))
	}
}

func TestPathForAdvertisement_BadPrefix(t *testing.T) {
	if _, err := pathForAdvertisement(PrefixAdvertisement{Prefix: "not-a-cidr"}); err == nil {
		t.Error("expected error for malformed prefix")
	} else if !strings.Contains(err.Error(), "parse prefix") {
		t.Errorf("error doesn't mention parse prefix: %v", err)
	}
}
