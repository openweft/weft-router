package statusemitter

import (
	"testing"

	"github.com/openweft/weft-router/internal/bgp"
)

func TestRollupOverall(t *testing.T) {
	cases := []struct {
		name  string
		peers []bgp.PeerStatus
		want  string
	}{
		{"none", nil, "configuring"},
		{"all-established",
			[]bgp.PeerStatus{{State: "Established"}, {State: "Established"}},
			"active"},
		{"some-established",
			[]bgp.PeerStatus{{State: "Established"}, {State: "Idle"}},
			"active"},
		{"all-negotiating",
			[]bgp.PeerStatus{{State: "OpenSent"}, {State: "Active"}},
			"configuring"},
		{"all-idle",
			[]bgp.PeerStatus{{State: "Idle"}, {State: "Idle"}},
			"down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rollupOverall(tc.peers); got != tc.want {
				t.Errorf("rollupOverall = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToJSONPeers(t *testing.T) {
	in := []bgp.PeerStatus{
		{Address: "198.51.100.1", State: "Established", UptimeSec: 600, ReceivedPrefixes: 12},
	}
	out := toJSONPeers(in)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].Address != "198.51.100.1" || out[0].State != "Established" {
		t.Errorf("address/state copy lost : %+v", out[0])
	}
	if out[0].UptimeSec != 600 || out[0].ReceivedPrefixes != 12 {
		t.Errorf("uptime/received lost : %+v", out[0])
	}
}
