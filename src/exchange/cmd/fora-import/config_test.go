package main

import (
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/importer"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk"
)

// TestConfigFromEnv pins the variable names and the defaults an operator
// relies on: unset limits are the walker's defaults, set ones are read as
// given, and an unset denomination reads as the server's: EUR, and an empty
// unit for termspreset's own default.
func TestConfigFromEnv(t *testing.T) {
	for _, key := range []string{
		"EXCHANGE_DSN", "EXCHANGE_DOMAIN", "IMPORT_EXCHANGE_URL", "IMPORT_EXCHANGE_RECIPIENT", "IMPORT_KEY_FILE",
		"IMPORT_REQUEST_TIMEOUT", "IMPORT_MAX_DOC_BYTES", "IMPORT_MAX_DOCUMENTS", "IMPORT_MAX_RESOURCES",
		"IMPORT_LISTING_TIMEOUT", "EXCHANGE_TERMS_CURRENCY", "EXCHANGE_TERMS_UNIT",
	} {
		t.Setenv(key, "")
	}
	got := configFromEnv()
	want := importer.Config{
		RequestTimeout: 10 * time.Second,
		ListingTimeout: 5 * time.Minute,
		TermsDefaults:  termspreset.Defaults{Currency: "EUR"},
		Limits: sitemapwalk.Limits{
			MaxDocBytes:  sitemapwalk.DefaultMaxDocBytes,
			MaxDocuments: sitemapwalk.DefaultMaxDocuments,
			MaxResources: sitemapwalk.DefaultMaxResources,
		},
	}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	if err := got.Validate(); err == nil {
		t.Fatal("an empty config validated")
	}

	t.Setenv("EXCHANGE_DSN", "postgres://x")
	t.Setenv("EXCHANGE_DOMAIN", "exchange.example")
	t.Setenv("IMPORT_EXCHANGE_URL", "https://exchange.example")
	t.Setenv("IMPORT_EXCHANGE_RECIPIENT", "exchange.example:8081")
	t.Setenv("IMPORT_KEY_FILE", "/keys/contributor-key.json")
	t.Setenv("IMPORT_REQUEST_TIMEOUT", "3s")
	t.Setenv("IMPORT_MAX_DOC_BYTES", "2048")
	t.Setenv("IMPORT_MAX_DOCUMENTS", "7")
	t.Setenv("IMPORT_MAX_RESOURCES", "99")
	t.Setenv("IMPORT_LISTING_TIMEOUT", "90s")
	t.Setenv("EXCHANGE_TERMS_CURRENCY", "USD")
	t.Setenv("EXCHANGE_TERMS_UNIT", "accesses")
	got = configFromEnv()
	want = importer.Config{
		DSN: "postgres://x", ExchangeDomain: "exchange.example", ExchangeURL: "https://exchange.example",
		ExchangeRecipient: "exchange.example:8081", KeyPath: "/keys/contributor-key.json",
		RequestTimeout: 3 * time.Second, ListingTimeout: 90 * time.Second,
		Limits:        sitemapwalk.Limits{MaxDocBytes: 2048, MaxDocuments: 7, MaxResources: 99},
		TermsDefaults: termspreset.Defaults{Currency: "USD", Unit: "accesses"},
	}
	if got != want {
		t.Fatalf("configured = %+v, want %+v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("a complete config failed validation: %v", err)
	}
}
