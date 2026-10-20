// Package importer builds a publisher's catalog from its sitemap and the
// licensing the publisher chose for the site, and pushes it through the same
// signed CatalogService.PushResources path every catalog push takes.
//
// One listing is one run: build the listing's license terms in the
// deployment's denomination, walk the sitemap within the limits, keep the pages
// on the registered domain and inside the listing's path scope (by the rule the
// edge gates requests with, internal/licensedscope), build one entry
// per page carrying those terms, normalize and validate the whole listing, then
// submit it in the wire's batches and record what the Exchange said. The
// importer only adds and updates entries; a page missing from a later sitemap
// keeps its entry, and nothing here ever deletes.
//
// It pushes only pages that are its own. Before the first submission it reads
// the rows the catalog already holds for the site: a page with no row, or with
// a row the importer wrote itself, is pushed; a page another source pushed is
// left to that source unless the publisher let the import take such pages
// over. Whenever it pushes a page that already has a row, it sends the row's
// content id, title and metadata back, so only the terms change (ownership.go).
//
// Every failure is an outcome, never a panic and never a partial submission
// that looks whole: an incomplete walk, an invalid entry or a stored page that
// cannot be pushed again stops the listing before anything is signed, and a
// run that stops mid-way reports exactly which entries were accepted, refused,
// left unconfirmed or never sent.
//
// There are three ways in, one inside the next. Run imports one listing and
// returns its outcome without touching the listing's row. RunAndRecord is Run
// followed by recording that outcome on the listing's own row: the step every
// run of the job takes for each listing. RunAll is the whole job, the thing
// the fora-import command runs: it builds the contributor client, holds the
// job's advisory lock for as long as it runs, and imports every eligible
// listing of the seat in turn through RunAndRecord (job.go).
package importer

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	sdkconnect "github.com/FORA-Protocol/protocol/sdk/go/connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/proto"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/guardedfetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/licensedscope"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk"
)

// Deps is what one run needs beyond the listing: the signed catalog client,
// the guarded fetcher, who the push is addressed to and from, the denomination
// terms are priced in, and the limits.
type Deps struct {
	// Client signs and submits as the operator's contributor identity, and
	// labels every push as the automatic import's (NewCatalogClient).
	Client *sdkconnect.CatalogClient
	// Pages reads the catalog rows the site already has: the importer's only
	// access to the catalog, and a read. Every change goes through Client.
	Pages repo.SitePagesRepo
	// Fetch reads sitemap documents; it must be the guarded client
	// sitemapwalk.NewClient builds.
	Fetch sitemapwalk.HTTPDoer
	// Exchange is the recipient's bare identity domain (ingest.PushTarget).
	Exchange string
	// CallerID is the contributor key's kid: the hostname the publisher
	// authorizes in its manifest and where the Exchange fetches the key.
	CallerID string
	// TermsDefaults is the denomination every listing's terms are priced in:
	// the Exchange's own currency and metering unit, the values its console
	// previews terms in. They are applied on every run, so a change to them
	// re-prices every imported entry at the next run.
	TermsDefaults termspreset.Defaults
	// Limits bound the sitemap walk.
	Limits sitemapwalk.Limits
	// ListingTimeout bounds the whole run, walk and submission together. Zero
	// means the caller's context alone bounds it.
	ListingTimeout time.Duration
}

// Run imports one listing and returns the outcome to record on it.
func Run(ctx context.Context, deps Deps, listing repo.ImportListing) repo.ImportOutcome {
	if deps.ListingTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deps.ListingTimeout)
		defer cancel()
	}
	terms, err := listingTerms(listing, deps.TermsDefaults)
	if err != nil {
		return failed(err.Error())
	}
	if listing.SitemapURL == "" {
		return failed("the listing has no sitemap URL")
	}
	walk := sitemapwalk.Walk(ctx, deps.Fetch, listing.SitemapURL, deps.Limits)
	if !walk.Complete {
		return failed(fmt.Sprintf("sitemap walk incomplete (%s): %v", walk.Reason, walk.Err))
	}
	entries, err := buildEntries(listing, terms, walk.URLs)
	if err != nil {
		return failed(err.Error())
	}
	if len(entries) == 0 {
		// A valid sitemap listing no offered page is a complete run of zero:
		// the push helper refuses an empty feed, and there is nothing to send.
		return repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete}
	}
	stored, err := deps.Pages.PagesOfSite(ctx, listing.TenantID, listing.Domain)
	if err != nil {
		// Without the rows the site already has, the run cannot tell whose
		// pages are whose, so it pushes nothing rather than push over them.
		return failed(fmt.Sprintf("read the site's catalog: %v", err))
	}
	plan, err := planEntries(listing, entries, stored)
	if err != nil {
		return failed(err.Error())
	}
	if len(plan.push) == 0 {
		// Every page is skipped: a complete run that sends nothing, since
		// every page the sitemap lists is in the catalog.
		return repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: len(entries), Skipped: plan.skipped}
	}
	report, err := ingest.PushEntries(ctx, deps.Client, ingest.PushTarget{
		Exchange: deps.Exchange,
		TenantID: listing.TenantID,
		CallerID: deps.CallerID,
	}, plan.push)
	return aggregate(len(entries), plan.skipped, report, err)
}

func failed(reason string) repo.ImportOutcome {
	return repo.ImportOutcome{Outcome: repo.ImportOutcomeFailed, FailureReason: reason}
}

// listingTerms builds the license terms the listing's licensing produces in the
// given denomination, and checks them before anything is fetched, returning
// them canonicalized.
//
// They are built on every run rather than read from storage: the listing
// stores the publisher's answers, and the currency and metering unit are the
// deployment's (service.Terms says why). The stored document is read through
// the service's own reading of it and built with the same builder that
// validated it when it was stored, so terms that were accepted build here.
//
// The terms are checked on their own, not only on the entries they end up in:
// a listing whose sitemap happens to be empty must still fail on bad terms
// rather than record a complete run, and a record that cannot be read fails
// this listing alone. The check is the one every entry gets, run over a probe
// entry at the root path, so a term this accepts is a term the Exchange
// accepts -- which is also what catches a denomination the Exchange refuses.
func listingTerms(listing repo.ImportListing, denomination termspreset.Defaults) ([]*forav1.LicenseTerm, error) {
	answers, err := service.DecodeTerms(listing.Terms, listing.Domain)
	if err != nil {
		return nil, err
	}
	terms, err := termspreset.FromControls(answers.Controls(), denomination)
	if err != nil {
		return nil, fmt.Errorf("the listing's licensing cannot be turned into license terms: %w", err)
	}
	if len(terms) == 0 {
		return nil, errors.New("the listing's licensing produced no license terms; nothing is imported under a default")
	}
	probe := &forav1.ResourceEntry{Domain: listing.Domain, Path: "/", Terms: terms}
	helpers.NormalizeResourceEntry(probe)
	if verdict := helpers.ValidateResourceEntry(probe); !verdict.OK() {
		v := verdict.Violations[0]
		return nil, fmt.Errorf("the license terms built for the listing are invalid: rule=%s path=%s: %s",
			v.Rule, v.Path, v.Message)
	}
	return probe.Terms, nil
}

// buildEntries turns the walked pages into the entries to push: one per page
// on the registered domain and inside the path scope, each carrying its own
// copy of the listing's terms, canonicalized and validated as the Exchange
// will see them. The first violation stops the listing, naming the page
// without any credentials its address carries.
func buildEntries(
	listing repo.ImportListing, terms []*forav1.LicenseTerm, pages []string,
) ([]*forav1.ResourceEntry, error) {
	entries := make([]*forav1.ResourceEntry, 0, len(pages))
	seen := map[string]bool{}
	for _, page := range pages {
		path, ok := offeredPath(listing, page)
		if !ok || seen[path] {
			continue
		}
		seen[path] = true
		entry := &forav1.ResourceEntry{
			Domain: listing.Domain,
			Path:   path,
			Terms:  cloneTerms(terms),
		}
		helpers.NormalizeResourceEntry(entry)
		if verdict := helpers.ValidateResourceEntry(entry); !verdict.OK() {
			v := verdict.Violations[0]
			return nil, fmt.Errorf("entry %s is invalid: rule=%s path=%s: %s",
				guardedfetch.Redacted(page), v.Rule, v.Path, v.Message)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// offeredPath returns the catalog path for a page the listing offers, or false
// for a page on another host, another scheme, outside the path scope, or with
// a query string. A catalog path cannot carry a query (the protocol's
// ResourceEntry.path admits no "?"), and a page keyed by its query, such as
// "/article?id=42", is not the page at "/article", so the query is never
// stripped: such a page is left out of the import, and the rest of the
// sitemap still goes through.
//
// The scope is matched by the rule the edge gates requests with
// (licensedscope), so a page the edge sends bots to negotiate for is a page
// the import offers. A path that rule cannot place -- one that does not
// decode, or holds a backslash or a dot segment -- is left out when the
// listing has a scope: the edge counts such a request as licensed only because
// it cannot place it, and its URL parser resolves dot segments before it
// matches, so the path it would judge is not this one. Membership is decided
// on the decoded path, but the path returned is the page's own escaped path,
// unchanged, because a page's identity is its path byte for byte.
func offeredPath(listing repo.ImportListing, page string) (string, bool) {
	u, err := url.Parse(page)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !strings.EqualFold(u.Host, listing.Domain) {
		return "", false
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "", false
	}
	if licensedscope.Classify(path, listing.ContentPathScope) != licensedscope.Inside || u.RawQuery != "" {
		return "", false
	}
	return path, true
}

func cloneTerms(terms []*forav1.LicenseTerm) []*forav1.LicenseTerm {
	out := make([]*forav1.LicenseTerm, 0, len(terms))
	for _, t := range terms {
		out = append(out, proto.Clone(t).(*forav1.LicenseTerm))
	}
	return out
}

// aggregate reads the six counts off the push report's explicit results and
// names the outcome. entries is every entry the run built; skipped is how many
// of them it never sent -- pages another source pushed, and pages it cannot
// push again under their row's identity. The report covers the rest, in the
// order they were sent.
//
// accepted is the Exchange's own count over the submissions it stored.
// refused counts only a submission the Exchange answered with a verdict.
// unconfirmed counts a submission with no verdict: the Exchange may have stored
// it. unsent counts a submission that never left this process and every entry
// after the one the run stopped at. Rejection is never derived as submitted
// minus accepted; the counts come from what each submission reported.
//
// The outcome: unconfirmed whenever any submission has no verdict, whatever
// came before it; complete when every entry it did not skip was accepted;
// failed when none was; partial when some were and a later submission was
// refused or unsent.
func aggregate(entries, skipped int, report ingest.PushReport, err error) repo.ImportOutcome {
	toSend := entries - skipped
	out := repo.ImportOutcome{Entries: entries, Skipped: skipped}
	if err != nil {
		out.FailureReason = err.Error()
	}
	lastSent := -1
	for _, s := range report.Submissions {
		n := s.Last - s.First + 1
		lastSent = s.Last
		switch {
		case s.Stored():
			out.Accepted += int(s.Accepted)
		case s.Unconfirmed():
			out.Unconfirmed += n
		case notSent(s.Err):
			out.Unsent += n
		default:
			out.Refused += n
		}
	}
	if err != nil {
		out.Unsent += toSend - 1 - lastSent
	}
	switch {
	case out.Unconfirmed > 0:
		out.Outcome = repo.ImportOutcomeUnconfirmed
	case out.Accepted == toSend:
		out.Outcome = repo.ImportOutcomeComplete
	case out.Accepted == 0:
		out.Outcome = repo.ImportOutcomeFailed
	default:
		out.Outcome = repo.ImportOutcomePartial
	}
	return out
}

// notSent reports whether the SDK declined to send the submission at all, so
// nothing can have been stored: the address failed a guard, the request could
// not be built, or the signature could not be produced.
func notSent(err error) bool {
	var ce *sdkconnect.CallError
	if !errors.As(err, &ce) {
		return false
	}
	switch ce.Kind {
	case sdkconnect.CallNotSent, sdkconnect.CallMalformed, sdkconnect.CallNotSignable:
		return true
	case sdkconnect.CallUnknown, sdkconnect.CallRefused, sdkconnect.CallUnreachable, sdkconnect.CallTooLarge:
		return false
	default:
		return false
	}
}
