//go:build integration

package transport_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	sdkconnect "github.com/FORA-Protocol/protocol/sdk/go/connect"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/catalogfeed"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/importer"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest/ingesttest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk/sitemaptest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/termscodec"
)

// The importer's end-to-end substrate: a real Exchange on the shared
// testcontainer with both contributor gates live (pushHarness), a real sitemap
// origin on loopback, and the production importer driven against them in one
// of two ways. run hands the listing to importer.RunAndRecord with the rig's
// own deps, so a scenario can watch what the walker requests or change a
// limit; runJob runs importer.RunAll, the function the fora-import command
// runs, with its own key file, client, lock and logger. The catalog is
// observed through DiscoverResources, the public read surface, except what it
// does not show, which goes through the importer's repository port
// (storedPage): whose row a page is, and a stored row whole. The listing's
// two import settings are recorded when the rig seeds the listing, as
// registration records them; its outcome goes through the listing repository
// ports, because no RPC or console route reads those columns yet.

// importDomain is the publisher site every importer scenario lists.
const importDomain = "publisher.example"

// importContributor is the operator's contributor identity: the hostname the
// publisher authorizes and the kid the importer signs as.
const importContributor = "catalog-contributor.fora.test"

// rigTerms is the licensing every rig listing records, in the shape
// registration stores it: charged per access at 0.05, for search.
const rigTerms = `{"pricing_model":"per_unit","rate":"0.05",` +
	`"permitted_functions":["search"],"attribution_required":false}`

// rigDenomination is what the rig's importer prices in unless a scenario
// changes it: the currency is named, the unit is termspreset's fallback.
var rigDenomination = termspreset.Defaults{Currency: "EUR"}

// sitePages renders n page URLs under the site's /news/ section.
func sitePages(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("https://%s/news/story-%03d", importDomain, i))
	}
	return out
}

// handRate is what a page pushed by hand charges: another price, currency and
// unit than the listing's licensing produces, so an offer shows at a glance
// which of the two a page carries.
const handRate = "0.3"

// handTitle is the title of a page pushed by hand.
const handTitle = "Pushed by hand"

// handRecord is a page on the listed site as someone pushes it by hand: its
// own terms, a title, and metadata an offer shows.
func handRecord(path string) catalogfeed.Record {
	return catalogfeed.Record{
		Domain: importDomain, Path: path, Title: handTitle,
		Terms: []catalogfeed.Term{{
			Semantics: "enumerated", Functions: []string{"search"},
			Pricing: &catalogfeed.Pricing{Model: "per_unit", Unit: "accesses", Rate: handRate, Currency: "USD"},
		}},
		ContentHash: "sha256:abc", HashMethod: "sha256",
		Ext: json.RawMessage(`{"desk":"science"}`),
	}
}

// sitemapOrigin is a sitemaptest.Origin serving one sitemap document, or a
// failure, at /sitemap.xml.
type sitemapOrigin struct{ *sitemaptest.Origin }

const sitemapPath = "/sitemap.xml"

func newSitemapOrigin(t *testing.T) *sitemapOrigin {
	t.Helper()
	return &sitemapOrigin{sitemaptest.NewOrigin(t)}
}

func (o *sitemapOrigin) url() string { return o.URL + sitemapPath }

func (o *sitemapOrigin) serve(body string) { o.Doc(sitemapPath, body) }

func (o *sitemapOrigin) fail(status int) { o.Fail(sitemapPath, status) }

// recordingDoer wraps the guarded walker client and keeps every address it was
// asked for, so a test can prove the importer never requested an article.
type recordingDoer struct {
	next sitemapwalk.HTTPDoer
	mu   sync.Mutex
	urls []string
}

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.urls = append(d.urls, req.URL.String())
	d.mu.Unlock()
	return d.next.Do(req)
}

func (d *recordingDoer) requested() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.urls...)
}

// verdictProxy sits between the importer's catalog client and the Exchange,
// forwarding every push and letting a test act on the n-th one: revoke the
// contributor before it is forwarded, or hold it past the listing deadline.
// hook returns true when it answered the request itself.
type verdictProxy struct {
	*httptest.Server
	calls atomic.Int32
}

func newVerdictProxy(t *testing.T, upstream string, hook func(n int, w http.ResponseWriter, r *http.Request) bool) *verdictProxy {
	t.Helper()
	p := &verdictProxy{}
	forward := forwardWithoutTrailers(upstream)
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(p.calls.Add(1))
		if hook != nil && hook(n, w, r) {
			return
		}
		forward(w, r)
	}))
	t.Cleanup(p.Close)
	return p
}

// importRig is one importer scenario: the harness Exchange, the listed site,
// the authorized contributor, the sitemap origin and the importer's deps.
type importRig struct {
	t    *testing.T
	h    *pushHarness
	kid  string
	priv ed25519.PrivateKey
	// keyPath is the contributor key file, which is what fora-ingest reads:
	// a console-registered site authorizes only this one contributor, so a page
	// pushed by hand is signed with the importer's own key.
	keyPath string
	origin  *publisherOrigin
	sitemap *sitemapOrigin
	fetch   *recordingDoer
	deps    importer.Deps
	// dial is where the importer's pushes are sent: the harness Exchange, or
	// the verdictProxy in front of it once viaProxy has put one there.
	dial string
	// runCtx is the context of the per-listing run in progress, carrying that
	// run's one request id, as every run of the job carries one; the rig's
	// client reads its request id from here. Outside a run it is the harness
	// context.
	runCtx context.Context
}

// importRigOptions are the listing settings a scenario registers the site
// with, beyond the licensing and the sitemap every rig records.
type importRigOptions struct {
	// Scope is the listing's path scope; nil offers the whole site.
	Scope []string
	// ImportReplacesManualTerms is the publisher's registration answer
	// letting the import take over pages another source pushed.
	ImportReplacesManualTerms bool
}

// newImportRig is newImportRigWith for a scenario that sets only the path
// scope.
func newImportRig(t *testing.T, scope []string) *importRig {
	t.Helper()
	return newImportRigWith(t, importRigOptions{Scope: scope})
}

// newImportRigWith starts a sitemap origin, lists importDomain ACTIVE on the
// seat under the given settings with rigTerms and that origin recorded on the
// listing, and authorizes the contributor in the site's manifest. The importer
// prices in rigDenomination and reads the site's rows through its production
// read port. Its catalog client dials the Exchange directly; viaProxy re-dials
// it through a verdictProxy.
func newImportRigWith(t *testing.T, opts importRigOptions) *importRig {
	t.Helper()
	// The walker's guarded client must reach the loopback sitemap origin; the
	// opt-outs are read when the transport is built, so this runs first.
	testutil.AllowLoopbackFetch(t)
	h := newPushHarness(t)
	sitemap := newSitemapOrigin(t)
	repotest.SeedSite(t, h.ctx, h.pool, repotest.Site{
		SeatTenantID: h.tenantID, Domain: importDomain, PublisherID: harnessResourceOwner,
		ContentPathScope: opts.Scope, Terms: []byte(rigTerms), SitemapURL: sitemap.url(),
		ImportReplacesManualTerms: opts.ImportReplacesManualTerms,
	})
	keyPath := ingesttest.WriteContributorKeyFile(t, importContributor)
	kid, priv, err := ingest.LoadContributorKey(keyPath)
	if err != nil {
		t.Fatalf("load contributor key: %v", err)
	}
	h.publishAgent(t, kid, priv)                       // Gate 1: kid -> key directory
	origin := h.publishPublisher(t, importDomain, kid) // Gate 2: kid in catalog_contributors
	r := &importRig{
		t: t, h: h, kid: kid, priv: priv, keyPath: keyPath, origin: origin,
		sitemap: sitemap,
		fetch:   &recordingDoer{next: sitemapwalk.NewClient(rigFetchTimeout)},
		dial:    h.server.URL,
		runCtx:  h.ctx,
	}
	r.deps = importer.Deps{
		Client:         r.importerClient(h.server.URL),
		Pages:          repo.NewSitePagesRepo(sqlc.New(h.pool)),
		Fetch:          r.fetch,
		Exchange:       harnessExchangeDomain,
		CallerID:       kid,
		TermsDefaults:  rigDenomination,
		Limits:         sitemapwalk.Limits{},
		ListingTimeout: 60 * time.Second,
	}
	return r
}

// serveManifestUncached puts a proxy in front of the publisher's manifest
// origin that marks every manifest response uncacheable. The Exchange's
// manifest cache honours a max-age shorter than its TTL, so from here on every
// push re-reads the manifest -- the way a test revokes the contributor between
// two submissions of one run.
func (r *importRig) serveManifestUncached() {
	r.t.Helper()
	target, err := url.Parse(r.origin.server.URL)
	if err != nil {
		r.t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("Cache-Control", "max-age=0")
		return nil
	}
	srv := httptest.NewServer(proxy)
	r.t.Cleanup(srv.Close)
	r.h.registerHost(importDomain, srv.URL)
}

// viaProxy re-dials the Exchange through a verdictProxy, for the per-listing
// runs and for runJob alike.
func (r *importRig) viaProxy(hook func(n int, w http.ResponseWriter, req *http.Request) bool) *verdictProxy {
	r.t.Helper()
	proxy := newVerdictProxy(r.t, r.h.server.URL, hook)
	r.deps.Client = r.importerClient(proxy.URL)
	r.dial = proxy.URL
	return proxy
}

// importerClient is the catalog client the per-listing runs push through,
// dialling exchangeURL: built by importer.NewCatalogClient, the constructor
// importer.RunAll builds its own client with, so every scenario pushes with
// the label production sends and with its run's one request id (runCtx). That
// RunAll wires its client that way is proven by the runJob scenarios.
func (r *importRig) importerClient(exchangeURL string) *sdkconnect.CatalogClient {
	r.t.Helper()
	client, err := importer.NewCatalogClient(exchangeURL, exchangetest.SignatureAgentOrigin(r.kid), r.priv,
		func() string { return reqctx.IDOrNew(r.runCtx) })
	if err != nil {
		r.t.Fatalf("build the importer's catalog client: %v", err)
	}
	return client
}

// pushManual pushes records the way the manual path does: through ingest.Run,
// fora-ingest's own parse, map, sign and push pipeline, over a JSON-Lines feed,
// signed with the rig's contributor key and without the automatic import's
// label. dial is where the push is sent; the recipient is always the harness
// Exchange.
func (r *importRig) pushManual(dial string, records ...catalogfeed.Record) {
	r.t.Helper()
	r.pushManualWith(r.h.ctx, dial, records...)
}

// pushManualWith is pushManual under ctx, for a scenario about what the run's
// context carries.
func (r *importRig) pushManualWith(ctx context.Context, dial string, records ...catalogfeed.Record) {
	r.t.Helper()
	if err := r.runIngest(ctx, dial, records...); err != nil {
		r.t.Fatalf("push by hand: %v", err)
	}
}

// runIngest runs fora-ingest's pipeline, ingest.Run, over records as a
// JSON-Lines feed, signed with the rig's key file, and returns its error, for a
// scenario about a run that must be refused.
func (r *importRig) runIngest(ctx context.Context, dial string, records ...catalogfeed.Record) error {
	r.t.Helper()
	var feed bytes.Buffer
	for _, rec := range records {
		line, err := json.Marshal(rec)
		if err != nil {
			r.t.Fatalf("encode feed line: %v", err)
		}
		feed.Write(line)
		feed.WriteByte('\n')
	}
	_, err := ingest.Run(ctx, ingest.Options{
		ExchangeURL: dial, Exchange: harnessExchangeDomain, TenantID: r.h.tenantID,
		KeyPath: r.keyPath, Feed: &feed,
	})
	return err
}

// listing reads the eligible listing back through the importer's own read
// port, so a scenario runs exactly what RunAll's loop would hand RunAndRecord.
func (r *importRig) listing() repo.ImportListing {
	r.t.Helper()
	eligible, err := repo.NewListingImportRepo(sqlc.New(r.h.pool)).Eligible(r.h.ctx, r.h.tenantID)
	if err != nil {
		r.t.Fatalf("Eligible: %v", err)
	}
	for _, l := range eligible {
		if l.Domain == importDomain {
			return l
		}
	}
	r.t.Fatalf("%s is not an eligible listing: %+v", importDomain, eligible)
	return repo.ImportListing{}
}

// run imports the listing once through importer.RunAndRecord, the step the job
// takes for every listing, and returns the outcome it recorded.
func (r *importRig) run() repo.ImportOutcome {
	r.t.Helper()
	return r.runListing(r.listing())
}

// runListing is one per-listing run of l, under a context carrying a request
// id of its own, as every run of the job carries one. The outcome is recorded
// on l's own row.
func (r *importRig) runListing(l repo.ImportListing) repo.ImportOutcome {
	r.t.Helper()
	r.runCtx = reqctx.WithID(r.h.ctx, reqctx.IDOrNew(r.h.ctx))
	defer func() { r.runCtx = r.h.ctx }()
	out, err := importer.RunAndRecord(r.runCtx, r.deps, repo.NewListingImportRepo(sqlc.New(r.h.pool)), l)
	if err != nil {
		r.t.Fatalf("RunAndRecord: %v", err)
	}
	return out
}

// rigFetchTimeout bounds one sitemap fetch in every rig run, per-listing or
// job.
const rigFetchTimeout = 5 * time.Second

// runJob runs importer.RunAll, the function the fora-import command runs,
// with its own key file, labelled client, advisory lock and per-listing
// loop, against the harness Exchange at dial. It imports the seat's eligible
// listings, which are the rig's one listing, since the harness's own sites
// have no sitemap. The limits, the listing deadline and the denomination
// are the rig's deps', so a scenario's change to them reaches the job. It
// returns the job's result and every record the job logged.
func (r *importRig) runJob() (*testutil.LogCapture, error) {
	r.t.Helper()
	logs, logger := testutil.NewLogCapture()
	err := importer.RunAll(r.h.ctx, importer.Config{
		DSN:               sharedPG.DSN,
		ExchangeDomain:    harnessExchangeDomain,
		ExchangeURL:       r.dial,
		ExchangeRecipient: harnessExchangeDomain,
		KeyPath:           r.keyPath,
		RequestTimeout:    rigFetchTimeout,
		ListingTimeout:    r.deps.ListingTimeout,
		Limits:            r.deps.Limits,
		TermsDefaults:     r.deps.TermsDefaults,
	}, logger)
	return logs, err
}

// recorded is the outcome the last run recorded on the listing, in the shape a
// run returns it, so want and its partition check apply to what a job run
// wrote.
func (r *importRig) recorded() repo.ImportOutcome {
	r.t.Helper()
	li := r.lastImport()
	out := repo.ImportOutcome{
		Outcome: li.Outcome, Entries: li.Entries, Accepted: li.Accepted, Refused: li.Refused,
		Unconfirmed: li.Unconfirmed, Unsent: li.Unsent, Skipped: li.Skipped,
	}
	if li.FailureReason != nil {
		out.FailureReason = *li.FailureReason
	}
	return out
}

// lastImport reads the recorded outcome back through the listing read port.
// No RPC and no console route reads the import-outcome columns -- the console's
// registration reads carry no import outcome (internal/consoleapi) -- so this
// is the documented repository fallback until one does.
func (r *importRig) lastImport() repo.ListingImport {
	r.t.Helper()
	l, err := repo.NewPublisherListingReadRepo(sqlc.New(r.h.pool)).ByDomain(r.h.ctx, r.h.tenantID, importDomain)
	if err != nil {
		r.t.Fatalf("ByDomain: %v", err)
	}
	return l.LastImport
}

// offers is how many offers DiscoverResources yields for a page: one per term
// the entry carries, zero when the page is not in the catalog.
func (r *importRig) offers(page string) int {
	r.t.Helper()
	return discoverOfferCount(r.t, r.h, page)
}

// pricing is the price DiscoverResources shows for a page's one term: the
// pricing of the one offer that sells it. An offer states its price once, on
// Offer.pricing, and the term it sells carries none, so the term is never read
// for it.
func (r *importRig) pricing(page string) *forav1.Pricing {
	r.t.Helper()
	offers := discoverOffersAs(r.t, r.h, page, requesterWithScopes("agent-discover"))
	if terms := offerTerms(r.t, page, offers); len(terms) != 1 {
		r.t.Fatalf("%s projects %d terms, want 1", page, len(terms))
	}
	return offers[0].GetPricing()
}

// want checks the outcome word and the six counts against wantOut, and that
// the counts partition the entries the run built: skipped + accepted + refused
// + unconfirmed + unsent = entries. The failure reason is not compared; a
// scenario that is about it asserts it itself.
func want(t *testing.T, got, wantOut repo.ImportOutcome) {
	t.Helper()
	counts := got
	counts.FailureReason, wantOut.FailureReason = "", ""
	if counts != wantOut {
		t.Fatalf("outcome = %+v; want %+v", got, wantOut)
	}
	if sum := got.Skipped + got.Accepted + got.Refused + got.Unconfirmed + got.Unsent; sum != got.Entries {
		t.Fatalf("the counts of %+v sum to %d, not to the %d entries built", got, sum, got.Entries)
	}
}

// resourceIDShown is the resource id an agent is shown for a page: the CoMP
// package id, "<resource id>#<term index>", on the offer discovery answers under
// the fora-comp-v1 profile. A scenario compares it before and after a run to
// see that a page kept its row, and with it its content id, without reading a
// table.
func resourceIDShown(t *testing.T, h *pushHarness, uri string) string {
	t.Helper()
	offer := discoverCompOffer(t, h, uri)
	id := offer.GetExt().GetFields()["comp"].GetStructValue().GetFields()["id"].GetStringValue()
	if id == "" {
		t.Fatalf("the offer for %s carries no CoMP package id: %v", uri, offer.GetExt())
	}
	return id
}

// assertPageKept checks what a run must never take from a page pushed by hand
// (handRecord), as an agent sees it: its title, its metadata, and the resource
// id it is sold under.
func assertPageKept(t *testing.T, h *pushHarness, uri, resourceID string) {
	t.Helper()
	offer := singleOffer(t, h, uri)
	if offer.GetTitle() != handTitle || offer.GetIdentity().GetContentHash() != "sha256:abc" ||
		offer.GetExt().GetFields()["desk"].GetStringValue() != "science" {
		t.Errorf("%s lost what it was pushed with: title %q, content hash %q, ext %v",
			uri, offer.GetTitle(), offer.GetIdentity().GetContentHash(), offer.GetExt())
	}
	if got := resourceIDShown(t, h, uri); got != resourceID {
		t.Errorf("%s is sold under %s after the run, want its own row's %s", uri, got, resourceID)
	}
}

// pushLabelled pushes records through the importer's own client, so the
// Exchange records their rows as the import's: what the import does itself,
// and what any contributor sending the import's label could do.
func (r *importRig) pushLabelled(records ...catalogfeed.Record) {
	r.t.Helper()
	entries, err := catalogfeed.MapRecords(records)
	if err != nil {
		r.t.Fatalf("map records: %v", err)
	}
	if _, err := ingest.PushEntries(r.h.ctx, r.importerClient(r.h.server.URL),
		pushTarget(r.h.tenantID, r.kid), entries); err != nil {
		r.t.Fatalf("labelled push: %v", err)
	}
}

// storeRowDirectly writes a catalog row for uri under resourceID, with
// metadata as its stored metadata document (nil for none), through the
// production catalog repository -- the port the push itself writes through --
// instead of through a push. It is for the rows no signed push can create
// today, which only a catalog written under older rules holds; every other
// page in these scenarios is pushed.
func (r *importRig) storeRowDirectly(uri, resourceID string, metadata []byte) {
	r.t.Helper()
	terms, err := termscodec.Marshal([]*forav1.LicenseTerm{seedPricedTerm()})
	if err != nil {
		r.t.Fatalf("marshal terms: %v", err)
	}
	repotest.SeedCatalogRow(r.t, r.h.ctx, r.h.pool, repotest.CatalogRow{
		TenantID: r.h.tenantID, ResourceID: resourceID, URI: uri, Domain: importDomain,
		OwnerID: harnessResourceOwner,
		Terms:   terms, Metadata: metadata,
	})
}
