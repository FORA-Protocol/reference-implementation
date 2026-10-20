package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/wellknown"
)

// The wiring of the document this Exchange publishes at
// /.well-known/fora.json — what it advertises, and the cache the admission
// gates read OTHER parties' documents through.
//
// It sits beside main.go rather than in it because the composition root is at
// the repository's file-length cap. Nothing here is called from anywhere else.

// baseCurrency is the currency this Exchange advertises in its well-known
// document: the FIRST of the configured ledger currencies. The protocol field
// holds a single value, so the order of EXCHANGE_BILLING_LEDGER is what decides
// it — adding a currency at the end of that list leaves the advertised value
// alone, and reordering the list changes it on purpose.
//
// It is read from the billing backend's own currencies rather than written as a
// literal, because base_currency is a claim about what every price this Exchange
// signs is denominated in, and only the billing adapter knows that. An unwired
// list yields the empty string, which publishes as no base currency — wrong, but
// visibly wrong, and it does not take the process down at boot.
func (d muxDeps) baseCurrency() string {
	return billing.BaseCurrency(d.ledgerCurrencies)
}

// exchangeSupportedProfiles is the single source of truth for the extension
// profiles the Exchange advertises (WellKnownManifest.supported_profiles) AND
// projects on the discovery path (service.ExchangeConfig).
// The list itself is forawellknown.ExchangeProfiles, shared with the console
// and the onboarding preview. It is deployment-wide.
func exchangeSupportedProfiles() []string {
	return forawellknown.ExchangeProfiles()
}

func registerWellKnown(mux *http.ServeMux, d muxDeps) (server.Handlers, error) {
	hops := exchangeMaxIntermediaryHops()
	// Endpoint is the ExchangeService ORIGIN (e.g. http://exchange:8081), NOT a
	// service path: the manifest's top-level endpoint (WellKnownManifest.endpoint)
	// is what a broker's well-known resolver reads to route a re-packaged execute,
	// and Connect appends "/fora.v1.ExchangeService/<Method>" to that origin
	// itself. A service-path value would double the path. Sourced from
	// EXCHANGE_PUBLIC_ORIGIN (default https://<EXCHANGE_DOMAIN>); CatalogEndpoint
	// likewise rides the origin. HealthEndpoint is the liveness route on that
	// same origin, served by the same listener as the RPCs, so a Broker probing
	// it measures the address it would route to.
	domain := exchangeDomain()
	origin := runhttp.EnvOr("EXCHANGE_PUBLIC_ORIGIN", "https://"+domain)
	directorySigner, err := d.offerSigner.DirectorySigner()
	if err != nil {
		return server.Handlers{}, fmt.Errorf("register well-known: %w", err)
	}
	wk, err := wellknown.New(wellknown.Config{
		Domain:                 domain,
		Endpoint:               origin,
		HealthEndpoint:         strings.TrimSuffix(origin, "/") + healthzPath,
		CatalogEndpoint:        origin,
		BaseCurrency:           d.baseCurrency(),
		SupportedProfiles:      exchangeSupportedProfiles(),
		MaxIntermediaryHops:    &hops,
		TermsURI:               d.registration.termsURI,
		TermsDigest:            d.registration.termsDigest,
		RegistrationDataSchema: d.registration.schema,
		OfferKey:               d.offerSigner.PublicKey(),
		OfferSigner:            directorySigner,
		Clock:                  clock.System{},
		KeyLifetime:            wellknown.OfferKeyLifetime,
	})
	if err != nil {
		// The terms pair is the half an operator sets by hand, and its rules live
		// in the protocol as protovalidate constraints on the manifest message.
		// They fire two layers down and report a constraint violation on a
		// manifest field, naming neither variable. Name them here so the boot
		// failure points at what to edit.
		return server.Handlers{}, fmt.Errorf("register well-known (EXCHANGE_TERMS_URI=%q EXCHANGE_TERMS_DIGEST=%q): %w",
			d.registration.termsURI, d.registration.termsDigest, err)
	}
	wk.RegisterRoutes(mux)
	return wk, nil
}

// exchangeMaxIntermediaryHops reads EXCHANGE_MAX_INTERMEDIARY_HOPS (default 4):
// the maximum number of signatures the Exchange accepts on a request, every
// signature counted, the agent's included (FORA
// WellKnownManifest.max_intermediary_hops). The manifest publishes it and the
// signature gate enforces the same value, so a sender that stays within the
// published number is never refused by the gate.
func exchangeMaxIntermediaryHops() int32 {
	const def int32 = 4
	raw := runhttp.EnvOr("EXCHANGE_MAX_INTERMEDIARY_HOPS", "")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 1<<20 {
		return def
	}
	return int32(n) //nolint:gosec // bounded to [1, 2^20] above
}

// newManifestCache builds the publisher-manifest cache over the shared
// SSRF-guarded fetch client. Honors FORA_WELLKNOWN_{SCHEME,PORT} for
// docker-compose / local-dev stacks; production leaves both unset (defaults to
// https + scheme-default port). ExpectRole pins every fetched manifest to
// ROLE_PUBLISHER so a misrouted document (e.g. an agent or exchange manifest)
// is rejected at the cache.
func newManifestCache(client forawellknown.HTTPDoer) *forawellknown.Cache {
	return forawellknown.NewCache(forawellknown.CacheOptions{
		Client:     client,
		Scheme:     runhttp.EnvOr("FORA_WELLKNOWN_SCHEME", "https"),
		Port:       runhttp.EnvOr("FORA_WELLKNOWN_PORT", ""),
		ExpectRole: forawellknown.RolePublisher,
	})
}
