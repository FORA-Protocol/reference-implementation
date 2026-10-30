//go:build e2e

package main

import (
	"strings"
	"testing"
)

// In an e2e build the label reaches the adapter followed by the colon that keeps
// namespaces prefix-free. adapterOptions is the only way the settings reach the
// adapter, so this covers the wiring as well as the parse.
func TestTigerBeetleSettings_E2ENamespaceReachesTheAdapter(t *testing.T) {
	for _, label := range []string{"exchange", "exchange-b", "a", strings.Repeat("a", 63)} {
		t.Run(label, func(t *testing.T) {
			setTigerBeetleEnv(t)
			t.Setenv(billingIDNamespaceEnv, label)
			settings, err := readTigerBeetleSettings()
			if err != nil {
				t.Fatalf("readTigerBeetleSettings: %v", err)
			}
			if ns := settings.adapterOptions(nil).IDNamespace; ns != label+":" {
				t.Fatalf("IDNamespace = %q, want %q", ns, label+":")
			}
		})
	}
}

// Every refused label must name the setting, so the boot failure says what to
// fix. An unset label is refused too: the e2e Exchanges share one cluster, and
// an Exchange without a namespace would share its accounts with the others.
func TestTigerBeetleSettings_E2ENamespaceRejects(t *testing.T) {
	for _, tc := range []struct {
		name, label, wantIn string
	}{
		{"unset", "", "required"},
		{"a colon would let one namespace be the prefix of another", "exchange:b", "invalid"},
		{"trailing colon", "exchange-b:", "invalid"},
		{"uppercase", "Exchange-B", "invalid"},
		{"leading hyphen", "-exchange", "invalid"},
		{"space", "exchange b", "invalid"},
		{"longer than 63 characters", strings.Repeat("a", 64), "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setTigerBeetleEnv(t)
			t.Setenv(billingIDNamespaceEnv, tc.label)
			settings, err := readTigerBeetleSettings()
			if err == nil {
				t.Fatalf("label %q accepted as namespace %q, want it refused",
					tc.label, settings.adapterOptions(nil).IDNamespace)
			}
			if !strings.Contains(err.Error(), tc.wantIn) || !strings.Contains(err.Error(), billingIDNamespaceEnv) {
				t.Errorf("error %q does not name %s and %q", err, billingIDNamespaceEnv, tc.wantIn)
			}
		})
	}
}
