package registry

import (
	"errors"
	"testing"
)

// TestResolveHealthEndpoint pins how a manifest's health_endpoint becomes the
// URL a pass probes. It is a parser: one string in, one URL or one refusal out,
// so it is tested as one. The integration suite proves the refresher uses it.
func TestResolveHealthEndpoint(t *testing.T) {
	const manifest = "https://exchange.example/.well-known/fora.json"
	tests := []struct {
		name       string
		advertised string
		want       string
	}{
		{"absolute on the serving host", "https://exchange.example/healthz", "https://exchange.example/healthz"},
		{"absolute on another host", "https://status.exchange.example/up", "https://status.exchange.example/up"},
		{"absolute with a port", "http://exchange.example:8081/healthz", "http://exchange.example:8081/healthz"},
		{"path-absolute resolves against the serving origin", "/healthz", "https://exchange.example/healthz"},
		{"path-relative resolves against the manifest's directory", "status", "https://exchange.example/.well-known/status"},
		{"dot segments are removed", "../healthz", "https://exchange.example/healthz"},
		{"scheme-relative takes the manifest's scheme", "//status.exchange.example/up", "https://status.exchange.example/up"},
		{"query is kept", "/healthz?deep=1", "https://exchange.example/healthz?deep=1"},
		{"fragment is dropped", "/healthz#top", "https://exchange.example/healthz"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveHealthEndpoint(manifest, tc.advertised)
			if err != nil {
				t.Fatalf("resolveHealthEndpoint(%q) refused: %v", tc.advertised, err)
			}
			if got != tc.want {
				t.Errorf("resolveHealthEndpoint(%q) = %q, want %q", tc.advertised, got, tc.want)
			}
		})
	}
}

// TestResolveHealthEndpointRefusals pins the values no pass may dial. Each is a
// settled fact about the manifest, so the refresher records it as a refusal
// rather than as an outage.
func TestResolveHealthEndpointRefusals(t *testing.T) {
	const manifest = "https://exchange.example/.well-known/fora.json"
	for _, advertised := range []string{
		"ftp://exchange.example/healthz",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"https://user:secret@exchange.example/healthz",
		"https:///healthz",
		"https://exchange.example/%zz",
	} {
		t.Run(advertised, func(t *testing.T) {
			got, err := resolveHealthEndpoint(manifest, advertised)
			if err == nil {
				t.Fatalf("resolveHealthEndpoint(%q) = %q, want a refusal", advertised, got)
			}
			if !errors.Is(err, errUnusableHealthEndpoint) {
				t.Errorf("resolveHealthEndpoint(%q) refused with %v, want errUnusableHealthEndpoint", advertised, err)
			}
		})
	}
}
