package main

import "testing"

func TestRoutesSendAZoneNameToItsServiceAndAnyOtherNameToItself(t *testing.T) {
	r, err := parseRoutes("foramcp.org=identity, agents.example=agent-directory", "broker=8082,exchange=8081")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"abc.foramcp.org":   "identity",
		"FORAMCP.ORG":       "identity",
		"x.agents.example":  "agent-directory",
		"notforamcp.org":    "notforamcp.org",
		"broker":            "broker",
		"demo.fora.example": "demo.fora.example",
	}
	for host, want := range cases {
		if got := r.upstreamHost(host); got != want {
			t.Errorf("upstreamHost(%q) = %q, want %q", host, got, want)
		}
	}
	if got := r.tlsPort("Broker"); got != "8082" {
		t.Errorf("tlsPort(Broker) = %q, want 8082", got)
	}
	if got := r.tlsPort("agent-e2e"); got != defaultTLSPort {
		t.Errorf("tlsPort(agent-e2e) = %q, want the default %q", got, defaultTLSPort)
	}
}

func TestRoutesRefuseAMalformedTable(t *testing.T) {
	for _, tc := range []struct{ zones, ports string }{
		{"foramcp.org", ""},
		{"=identity", ""},
		{"", "broker=http"},
		{"", "broker=70000"},
		{"", "broker="},
	} {
		if _, err := parseRoutes(tc.zones, tc.ports); err == nil {
			t.Errorf("parseRoutes(%q, %q) accepted a malformed table", tc.zones, tc.ports)
		}
	}
}
