//go:build !e2e

package main

import (
	"context"
	"strings"
	"testing"
)

// A production build refuses the e2e-only namespace setting instead of
// ignoring it. The refusal must come while the settings are read, before any
// connection: the address names a port no ledger listens on and the operation
// timeout is short, so a build that tried to connect first would fail with a
// TigerBeetle error that does not name the setting.
func TestTigerBeetleSettings_ProductionRefusesIDNamespace(t *testing.T) {
	setTigerBeetleEnv(t)
	t.Setenv(billingIDNamespaceEnv, "exchange-b")
	t.Setenv("EXCHANGE_BILLING_TB_OP_TIMEOUT", "100ms")

	got, _, _, err := selectBillingAdapter(context.Background(), discardLogger())
	if err == nil {
		t.Fatalf("want the namespace setting refused, got adapter %T", got)
	}
	if !strings.Contains(err.Error(), billingIDNamespaceEnv) || !strings.Contains(err.Error(), "e2e builds") {
		t.Fatalf("error %q does not refuse %s as an e2e-only setting", err, billingIDNamespaceEnv)
	}
}

// Without the setting a production build derives unsalted ids: the options the
// adapter is built with carry no namespace.
func TestTigerBeetleSettings_ProductionIsUnsalted(t *testing.T) {
	setTigerBeetleEnv(t)
	settings, err := readTigerBeetleSettings()
	if err != nil {
		t.Fatalf("readTigerBeetleSettings: %v", err)
	}
	if ns := settings.adapterOptions(nil).IDNamespace; ns != "" {
		t.Fatalf("IDNamespace = %q, want unsalted (empty) in a production build", ns)
	}
}
