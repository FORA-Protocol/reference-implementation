//go:build integration

// Package repotest holds fixture ARRANGEMENT that more than one of the
// Exchange's integration suites needs as a precondition rather than as its
// subject.
//
// It exists for one reason: the publisher registration model is real records. A
// catalog push is accepted only for a site registered under a payee and listed
// ACTIVE on the pushing seat, and fora.catalog's resource_owner_id references
// fora.publishers, so every suite that pushes an entry or writes a commission
// override must first register all three.
//
// Driving the registration routes for that would make every catalog suite a
// registration suite as well, and each one would otherwise grow its own copy of
// the same setup. One copy here, through the production write ports, keeps the
// fixture and the code under test agreeing on the encoding by construction. The
// registration routes have suites of their own, which drive them through the
// public surface with real signatures.
package repotest

import (
	"cmp"
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// SeedPublisher registers one payee through repo.PublisherWriteRepo, the surface
// the registration handler writes through. The transaction is the port's own
// contract, not a fixture flourish: in production the publisher commits with the
// grant that makes it reachable.
//
// Re-seeding the same id is NOT absorbed. The id is minted per registration, so a
// second seed under one id means two tests disagree about who owns it, and that
// must fail here rather than silently share a payee.
func SeedPublisher(
	tb testing.TB, ctx context.Context, pool *pgxpool.Pool, publisherID, displayName string,
) repo.Publisher {
	tb.Helper()
	publishers := repo.NewPublisherWriteRepo(sqlc.New(pool))
	var p repo.Publisher
	err := sharedb.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var terr error
		p, terr = publishers.Create(ctx, tx, publisherID, displayName)
		return terr
	})
	if err != nil {
		tb.Fatalf("seed publisher %q: %v", publisherID, err)
	}
	return p
}

// Seat describes the one Exchange seat a suite sells as.
type Seat struct {
	// Domain is the hostname the seat answers on, the value EXCHANGE_DOMAIN
	// carries. Registration and the catalog push path both resolve the seat by
	// it.
	Domain string
	// RSAKeyRef is the seat's rsa_key_ref. Empty leaves the column NULL, which
	// is a seat that signs only with Ed25519.
	RSAKeyRef string
}

// SeatEd25519KeyRef is the Ed25519 key reference SeedSeat gives the seat
// seatID. A suite that mints delivery URLs puts the seat's key in its keystore
// under it.
func SeatEd25519KeyRef(seatID string) string { return "secret://ed25519/" + seatID }

// SeedSeat creates one seat and returns its tenant id. Its Ed25519 key
// reference is SeatEd25519KeyRef of that id.
//
// It writes through the sqlc InsertTenant query rather than a repository port,
// because none exists: seats are provisioned by the operator's bootstrap, not by
// any request path, so there is no production write surface to arrange through.
// The seat gets the schema's default fee rate. The operator changes it through
// the admin plane's SetTenantFeeRate, and a suite that needs another rate
// calls that route once the seat exists.
func SeedSeat(tb testing.TB, ctx context.Context, pool *pgxpool.Pool, in Seat) string {
	tb.Helper()
	seatID := "t_" + uuid.NewString()
	params := sqlc.InsertTenantParams{
		TenantID:        seatID,
		Domain:          in.Domain,
		Ed25519KeyRef:   SeatEd25519KeyRef(seatID),
		ReportingPolicy: []byte(`{}`),
	}
	if in.RSAKeyRef != "" {
		params.RsaKeyRef = pgtype.Text{String: in.RSAKeyRef, Valid: true}
	}
	if _, err := sqlc.New(pool).InsertTenant(ctx, params); err != nil {
		tb.Fatalf("seed seat %q: %v", in.Domain, err)
	}
	return seatID
}

// Site describes one publisher site a suite needs registered, and how the seat
// under test lists it.
type Site struct {
	// SeatTenantID is the fora.tenants row the listing is created on: the
	// Exchange seat, whose domain is EXCHANGE_DOMAIN. It is NOT Domain below.
	SeatTenantID string
	// Domain is the publisher's own hostname, the site that serves the manifest
	// and that catalog entries are pushed for.
	Domain string
	// PublisherID is the payee the site belongs to. SeedSite registers it when
	// NewPublisher is true and expects it to exist otherwise, so two sites can
	// share one payee.
	PublisherID string
	// DisplayName is the payee's name, used only when NewPublisher is true.
	// Empty falls back to the publisher id.
	DisplayName string
	// NewPublisher registers PublisherID before the site. Leave it false to
	// attach a second site to a payee an earlier call already registered.
	NewPublisher bool
	// SigningScheme is the site's delivery-URL scheme. Empty means ED25519.
	SigningScheme string
	// CDN names the integration serving the site. Empty means cloudflare, the
	// shape most suites want: an Ed25519 site behind an edge worker.
	CDN string
	// CloudFrontKeyPairID is recorded after the site row exists, mirroring
	// production: the publisher enters it once CloudFront has assigned it. It is
	// written only for an AWS_CLOUDFRONT_RSA site; the write port is a no-op on
	// any other scheme.
	CloudFrontKeyPairID string
	// Pending leaves the listing in PENDING_MANIFEST. The default is an ACTIVE
	// listing, because a push is refused until the listing is active and almost
	// every suite wants one that works.
	Pending bool
	// ContentPathScope is the part of the site the listing offers. Nil offers
	// the whole site, which is what every suite but the importer's wants.
	ContentPathScope []string
	// Terms is the licensing the listing records, as registration stores it.
	// Nil records the shared fixture document: free for every use the
	// vocabulary offers.
	Terms []byte
	// SitemapURL is where the listing's pages are listed. Empty records none,
	// which is a listing the automatic import skips.
	SitemapURL string
	// ImportReplacesManualTerms records the publisher's answer that the
	// automatic import may take over pages another source pushed. False, the
	// default, is what a publisher who left the box unticked records.
	ImportReplacesManualTerms bool
	// Unlisted registers the site and creates no listing at all: a site the
	// payee holds that this seat does not sell, which is what a second seat
	// finds, and what a seat finds after its listing was removed.
	Unlisted bool
}

// SeedSite registers a publisher site and lists it on one seat, through the
// production write ports the registration handlers use.
//
// Each step runs in its own transaction, which is what the ports do in
// production: registering a publisher, registering a site and activating a
// listing are three separate operator or console actions, and a fixture that
// bundled them into one transaction would arrange a state no real sequence
// produces.
func SeedSite(tb testing.TB, ctx context.Context, pool *pgxpool.Pool, in Site) repo.PublisherDomain {
	tb.Helper()
	if in.NewPublisher {
		SeedPublisher(tb, ctx, pool, in.PublisherID, cmp.Or(in.DisplayName, in.PublisherID))
	}
	site := seedDomain(tb, ctx, pool, in)
	if !in.Unlisted {
		seedListing(tb, ctx, pool, in)
	}
	return site
}

// SeedGrant lets one person act for one payee on one seat, through
// repo.PublisherGrantWriteRepo, the port RegisterPublisher writes through.
//
// Registration only ever grants a person the payee it mints for them, so no
// request path grants an existing payee on a second seat. A suite that needs a
// payee reachable from two seats arranges the second grant here.
func SeedGrant(tb testing.TB, ctx context.Context, pool *pgxpool.Pool, g repo.PublisherGrant) {
	tb.Helper()
	grants := repo.NewPublisherGrantWriteRepo(sqlc.New(pool))
	err := sharedb.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return grants.Grant(ctx, tx, g)
	})
	if err != nil {
		tb.Fatalf("grant publisher %q on seat %q: %v", g.PublisherID, g.TenantID, err)
	}
}

func seedDomain(tb testing.TB, ctx context.Context, pool *pgxpool.Pool, in Site) repo.PublisherDomain {
	tb.Helper()
	domains := repo.NewPublisherDomainWriteRepo(sqlc.New(pool))
	scheme := cmp.Or(in.SigningScheme, "ED25519")
	var site repo.PublisherDomain
	err := sharedb.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var terr error
		site, terr = domains.Create(ctx, tx, repo.NewSite{
			Domain:        in.Domain,
			PublisherID:   in.PublisherID,
			SigningScheme: scheme,
			CDN:           cmp.Or(in.CDN, "cloudflare"),
		})
		return terr
	})
	if err != nil {
		tb.Fatalf("register site %q under publisher %q: %v", in.Domain, in.PublisherID, err)
	}
	if in.CloudFrontKeyPairID == "" {
		return site
	}
	err = sharedb.WithTx(ctx, pool, func(tx pgx.Tx) error {
		n, terr := domains.SetCloudFrontKeyPairID(ctx, tx, in.Domain, in.PublisherID, in.CloudFrontKeyPairID)
		if terr == nil && n == 0 {
			// Zero rows means the site does not sign with CloudFront RSA, so the
			// id would be a value nothing reads. The other two no-op cases
			// cannot arise here: seedDomain registered this site under this
			// publisher two statements ago. Failing rather than returning tells
			// the suite its fixture is inconsistent.
			tb.Fatalf("site %q takes no CloudFront key-pair id under scheme %q", in.Domain, scheme)
		}
		return terr
	})
	if err != nil {
		tb.Fatalf("set CloudFront key-pair id for site %q: %v", in.Domain, err)
	}
	site.CloudFrontKeyPairID = in.CloudFrontKeyPairID
	return site
}

func seedListing(tb testing.TB, ctx context.Context, pool *pgxpool.Pool, in Site) {
	tb.Helper()
	listings := repo.NewPublisherListingWriteRepo(sqlc.New(pool))
	err := sharedb.WithTx(ctx, pool, func(tx pgx.Tx) error {
		// The path scope, the licensing, the sitemap and the import's take-over
		// answer are the suite's when it sets them, and only the importer's
		// suites do: the push path reads none of them -- what it reads is that
		// the listing exists and is active. Otherwise the whole site is
		// offered, free for every use the vocabulary offers, with no sitemap.
		terms := in.Terms
		if terms == nil {
			terms = fixtureTerms(tb)
		}
		_, terr := listings.Create(ctx, tx, repo.NewListing{
			TenantID:                  in.SeatTenantID,
			Domain:                    in.Domain,
			ContentPathScope:          in.ContentPathScope,
			Terms:                     terms,
			SitemapURL:                in.SitemapURL,
			ImportReplacesManualTerms: in.ImportReplacesManualTerms,
		})
		return terr
	})
	if err != nil {
		tb.Fatalf("list site %q on seat %q: %v", in.Domain, in.SeatTenantID, err)
	}
	if in.Pending {
		return
	}
	// Activation is its own write in production too: the listing is created
	// PENDING_MANIFEST and the manifest check moves it, which is why Create
	// takes no state.
	err = sharedb.WithTx(ctx, pool, func(tx pgx.Tx) error {
		written, terr := listings.Activate(ctx, tx, in.SeatTenantID, in.Domain)
		if terr == nil && written.State != repo.ListingStateActive {
			tb.Fatalf("activating the listing of %q on seat %q left it %q",
				in.Domain, in.SeatTenantID, written.State)
		}
		return terr
	})
	if err != nil {
		tb.Fatalf("activate the listing of %q on seat %q: %v", in.Domain, in.SeatTenantID, err)
	}
}
