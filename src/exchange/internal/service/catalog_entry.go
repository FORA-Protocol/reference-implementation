package service

// Converting a wire ResourceEntry into the stored CatalogEntry shape. Split from
// catalog.go because these are pure functions over the two message shapes — no
// receiver, no service state, no IO — and the service file is about the push and
// rebuild flows that call them.

import (
	"fmt"
	"net/url"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/catalogid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/metadatacodec"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/termscodec"
)

// siteDomainFromURI recovers the publisher site a stored catalog URI belongs to
// — the key of its fora.publisher_domains row.
//
// entryFromProto builds the URI as scheme + "://" + domain + path
// (catalogid.Page.URI), so url.Host returns exactly the domain the push carried.
// Host and not Hostname: Hostname strips a port, and a domain that carried one
// would then miss its registration.
func siteDomainFromURI(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse catalog uri %q: %w", uri, err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("catalog uri %q names no host", uri)
	}
	return parsed.Host, nil
}

// entryFromProto converts a ResourceEntry message into the repo.CatalogEntry
// shape, materializing the stable resource_id and the pricing JSON payload.
func entryFromProto(tenantID string, e *forav1.ResourceEntry) (repo.CatalogEntry, error) {
	if e.GetDomain() == "" || e.GetPath() == "" {
		return repo.CatalogEntry{}, fmt.Errorf("missing domain/path")
	}
	page := catalogid.Page{
		TenantID: tenantID, Domain: e.GetDomain(), Path: e.GetPath(), ContentID: e.GetContentId(),
	}
	scheme := currentCatalogURIScheme()
	uri := page.URI(scheme)
	terms, err := termscodec.Marshal(e.GetTerms())
	if err != nil {
		return repo.CatalogEntry{}, fmt.Errorf("marshal terms: %w", err)
	}
	metadata, err := metadatacodec.Marshal(e)
	if err != nil {
		return repo.CatalogEntry{}, fmt.Errorf("marshal metadata: %w", err)
	}
	return repo.CatalogEntry{
		ResourceID: page.ResourceID(scheme),
		TenantID:   tenantID,
		URI:        uri,
		URIPrefix:  uri,
		// pricing is the legacy catalog.pricing JSONB column (NOT NULL). It no
		// longer carries offer/billing price — pricing is derived per-request
		// from the selected LicenseTerm. A neutral sentinel keeps the
		// NOT NULL column satisfied without re-introducing a hardcoded default;
		// no code path reads it for price.
		PricingJSON:    legacyPricingSentinel,
		TermsJSON:      terms,
		DeliveryMethod: "INSTRUCTIONS",
		MetadataJSON:   metadata,
		// Stored in its own column, not in MetadataJSON: metadatacodec.Marshal
		// projects only the extension-metadata fields and excludes the title by
		// design. An empty pushed title is normalized to nil so the column stays
		// NULL and Offer.title stays unset, rather than becoming a
		// present-but-empty field.
		Title: titleOrNil(e.GetTitle()),
	}, nil
}

// titleOrNil maps a pushed ResourceEntry.title onto the nullable stored title.
// The proto field is optional, so an absent title already arrives as "", and an
// explicitly-empty one is treated the same way: neither is a title.
func titleOrNil(title string) *string {
	if title == "" {
		return nil
	}
	return &title
}

// legacyPricingSentinel is the value written into the NOT NULL catalog.pricing
// JSONB column now that offer/billing pricing is derived from the selected
// LicenseTerm rather than a stored pricing blob. It exists only to
// satisfy the column constraint until the column is dropped in a later schema
// change; nothing reads it for price.
var legacyPricingSentinel = []byte(`{}`)

// uriFromEntry reconstructs the URI from an entry even when the proto is
// missing fields — used only for the rejection message so invalid pushes show
// up as "uri": "" rather than being silently dropped.
func uriFromEntry(e *forav1.ResourceEntry) string {
	if e.GetDomain() == "" && e.GetPath() == "" {
		return ""
	}
	return catalogid.Page{Domain: e.GetDomain(), Path: e.GetPath()}.URI(currentCatalogURIScheme())
}
