package main

import "testing"

// setTigerBeetleEnv selects the TigerBeetle adapter with a valid ledger and an
// address no ledger listens on, so a test reaches the setting it is about
// without a cluster. The namespace tests of both builds share it.
func setTigerBeetleEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FORA_BILLING_ADAPTER", "tigerbeetle")
	t.Setenv("EXCHANGE_BILLING_LEDGER", "840")
	t.Setenv("EXCHANGE_BILLING_TB_ADDRESS", "127.0.0.1:1")
}
