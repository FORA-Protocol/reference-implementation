package main

import "testing"

// The served manifest publishes EXCHANGE_MAX_INTERMEDIARY_HOPS as
// max_intermediary_hops, the number of signatures the gate admits. Every
// request carries at least the agent's own signature, so a value below one
// would publish a limit no request can meet; it falls back to the default like
// any other unusable value.
func TestManifestPublishesTheSignatureCap(t *testing.T) {
	t.Setenv("EXCHANGE_REGISTRATION_SCHEMA", "")
	t.Setenv("EXCHANGE_TERMS_URI", "")
	t.Setenv("EXCHANGE_TERMS_DIGEST", "")

	for raw, want := range map[string]float64{"": 4, "2": 2, "1": 1, "0": 4, "-1": 4, "x": 4} {
		t.Setenv("EXCHANGE_MAX_INTERMEDIARY_HOPS", raw)
		_, doc := servedManifest(t)
		if got := doc["max_intermediary_hops"]; got != want {
			t.Errorf("EXCHANGE_MAX_INTERMEDIARY_HOPS=%q: max_intermediary_hops = %v, want %v", raw, got, want)
		}
	}
}
