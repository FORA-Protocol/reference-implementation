package billing

// WelcomeCreditKey derives the idempotency key of the shared welcome-credit
// ledger slot for one agent in one currency: "service-welcome:" + billingRef +
// ":" + currency. The Register flow's default-credit grant and the operator
// funding scripts' reserved "service-welcome" label both derive their transfer
// id from this one string, so whichever side lands first wins and the other
// side is a no-op — an agent is never double-credited. "service-welcome" is a
// reserved label that has never carried any other derivation; every other
// funding label keeps the amount-bearing staging derivation and therefore
// stacks deliberately.
//
// The currency is part of the key because a TigerBeetle transfer id is unique
// per CLUSTER, not per ledger. Without it, one agent's EUR grant and USD grant
// would name the same slot: the first would apply and the second would read an
// existing transfer and silently do nothing, leaving the agent funded in one
// currency only with no error anywhere. The adapter deliberately does not add
// the currency itself — the shell scripts derive the id from this same key text
// and both sides have to agree on it.
//
// This function is the single Go owner of the derivation. The shell copy in
// deploy/terraform/scripts/fund-staging-agent.sh must stay byte-identical;
// the pinned-vector test in welcome_test.go breaks when either the prefix or
// the transfer-id derivation drifts.
func WelcomeCreditKey(billingRef, currency string) string {
	return "service-welcome:" + billingRef + ":" + currency
}
