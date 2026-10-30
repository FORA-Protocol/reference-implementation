package main

// billingIDNamespaceEnv names the setting that salts every TigerBeetle id this
// Exchange derives, so that several Exchanges can bill on one cluster without
// posting into each other's accounts. A TigerBeetle id is unique per cluster,
// not per ledger, and the platform fee and liquidity accounts and a resource
// owner's revenue account are derived from fixed names, so two unsalted
// Exchanges on one cluster would share them.
//
// billingIDNamespace reads it. It has two definitions selected by the e2e build
// tag: billing_namespace_e2e.go, which the e2e stack builds so its Exchanges can
// share one ledger, and billing_namespace_production.go, which every production
// build compiles. A production Exchange derives unsalted ids, the ones the
// operator funding and balance scripts compute, so that build refuses the
// setting instead of ignoring it.
const billingIDNamespaceEnv = "EXCHANGE_BILLING_TB_ID_NAMESPACE"
