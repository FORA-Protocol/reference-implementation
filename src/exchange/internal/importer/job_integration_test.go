//go:build integration

package importer_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/importer"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest/ingesttest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk/sitemaptest"
)

// The import job's run contract on the shared testcontainer: the lock, the
// sequential loop, the exit signal. No listing here ever reaches a push: every
// sitemap is empty (a complete run of zero) or unreadable (a failed run), so
// the Exchange is never dialled and the catalog client is built but idle. What
// the job sends -- the automatic import's label and the run's one request id,
// on every push and on every importer.* line -- and that a partial or an
// unconfirmed listing fails the run are proven against a real Exchange by the
// transport package's job scenarios
// (src/exchange/internal/transport/importer_label_e2e_test.go and
// importer_e2e_test.go).

const (
	seatDomain  = "exchange.example"
	publisherID = "pub-import"
)

// rig is one run's arrangement: the seat, a pool to list sites with, a sitemap
// origin per site, and the config a run reads.
type rig struct {
	t       *testing.T
	ctx     context.Context
	dsn     string
	pool    *pgxpool.Pool
	seat    string
	cfg     importer.Config
	origins map[string]*sitemapOrigin
}

func newRig(t *testing.T) *rig {
	t.Helper()
	// The walker's guarded client reaches the loopback sitemap origins only
	// with the deployment opt-outs set, and reads them when it is built.
	testutil.AllowLoopbackFetch(t)
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)
	pool := sharedb.OpenForTest(t, ctx, dsn)
	seat := repotest.SeedSeat(t, ctx, pool, repotest.Seat{Domain: seatDomain})
	repotest.SeedPublisher(t, ctx, pool, publisherID, "Stoa Press")
	return &rig{
		t: t, ctx: ctx, dsn: dsn, pool: pool, seat: seat,
		origins: map[string]*sitemapOrigin{},
		cfg: importer.Config{
			DSN:            dsn,
			ExchangeDomain: seatDomain,
			ExchangeURL:    "https://" + seatDomain,
			KeyPath:        ingesttest.WriteContributorKeyFile(t, "catalog-contributor.example"),
			RequestTimeout: 5 * time.Second,
			ListingTimeout: 30 * time.Second,
		},
	}
}

// list registers a site ACTIVE on the seat, with its sitemap recorded on the
// listing as registration records it: at its own sitemap origin, which serves
// body. The licensing is the shared fixture document.
func (r *rig) list(domain, body string) *sitemapOrigin {
	r.t.Helper()
	o := newSitemapOrigin(r.t, body)
	r.origins[domain] = o
	repotest.SeedSite(r.t, r.ctx, r.pool, repotest.Site{
		SeatTenantID: r.seat, Domain: domain, PublisherID: publisherID,
		SitemapURL: o.URL + "/sitemap.xml",
	})
	return o
}

// run drives one run of the job with the rig's config and returns what it
// logged.
func (r *rig) run() (*testutil.LogCapture, error) {
	r.t.Helper()
	logs, logger := testutil.NewLogCapture()
	return logs, importer.RunAll(r.ctx, r.cfg, logger)
}

// lastImport reads a listing's recorded outcome back through the listing read
// port. No RPC and no console route reads the import-outcome columns -- the
// console's registration reads carry no import outcome (internal/consoleapi) --
// so this is the documented repository fallback until one does.
func (r *rig) lastImport(domain string) repo.ListingImport {
	r.t.Helper()
	l, err := repo.NewPublisherListingReadRepo(sqlc.New(r.pool)).ByDomain(r.ctx, r.seat, domain)
	if err != nil {
		r.t.Fatalf("ByDomain %s: %v", domain, err)
	}
	return l.LastImport
}

var emptySitemap = sitemaptest.Urlset()

// sitemapOrigin is a sitemaptest.Origin that serves one document at
// /sitemap.xml, with a switchable status and an optional delay.
type sitemapOrigin struct{ *sitemaptest.Origin }

func newSitemapOrigin(t *testing.T, body string) *sitemapOrigin {
	t.Helper()
	o := &sitemapOrigin{sitemaptest.NewOrigin(t)}
	o.set(body, http.StatusOK, 0)
	return o
}

func (o *sitemapOrigin) set(body string, status int, delay time.Duration) {
	o.Handle("/sitemap.xml", func(w http.ResponseWriter, req *http.Request) {
		if delay > 0 {
			select {
			case <-req.Context().Done():
				return
			case <-time.After(delay):
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// TestRunAll_SkipsWhileAnotherRunHoldsTheLock: a run that finds the lock held
// logs a skip, writes no outcome and returns nil (exit 0); once the holder
// releases, the same run completes and the lock is free again afterwards.
func TestRunAll_SkipsWhileAnotherRunHoldsTheLock(t *testing.T) {
	r := newRig(t)
	r.list("stoapress.example", emptySitemap)

	release, held, err := sharedb.TryAdvisoryLock(r.ctx, r.dsn, importer.LockKey)
	if err != nil || !held {
		t.Fatalf("take the lock as the other run: held=%v err=%v", held, err)
	}
	logs, err := r.run()
	if err != nil {
		t.Fatalf("a run skipped on lock contention returned %v; want nil (exit 0)", err)
	}
	logs.AssertLoggedOnce(t, "importer.skipped", "INFO")
	if li := r.lastImport("stoapress.example"); li.Outcome != "" || li.AttemptAt != nil {
		t.Fatalf("a skipped run wrote an outcome: %+v", li)
	}
	release()

	if _, err := r.run(); err != nil {
		t.Fatalf("the run after the lock was released returned %v", err)
	}
	if li := r.lastImport("stoapress.example"); li.Outcome != repo.ImportOutcomeComplete {
		t.Fatalf("after the real run the listing records %+v", li)
	}
	r.wantLockFree()
}

// TestRunAll_HoldsTheLockUntilTheRunEnds: a run holds the lock for as long as
// it works, not only while it starts. While the first run is inside its
// listing, no other session can take the lock, and a second run logs a skip,
// returns nil and writes no outcome; once the first run returns, it has
// recorded its own outcome and the lock is free.
func TestRunAll_HoldsTheLockUntilTheRunEnds(t *testing.T) {
	r := newRig(t)
	r.cfg.RequestTimeout = 30 * time.Second
	hit, open := r.listHeld("held.example")
	wait := r.start()
	// Registered after the origin, so it runs before the origin closes: the
	// held request is let go and the first run finishes before its server does.
	t.Cleanup(func() { open(); _ = wait() })

	select {
	case <-hit:
	case <-time.After(15 * time.Second):
		t.Fatal("the first run never fetched its sitemap")
	}
	r.wantLockHeld()
	logs, err := r.run()
	if err != nil {
		t.Fatalf("a run overlapping another returned %v; want nil (exit 0)", err)
	}
	logs.AssertLoggedOnce(t, "importer.skipped", "INFO")
	if started := logs.Records(t, "importer.start"); len(started) != 0 {
		t.Fatalf("a run overlapping another started importing: %v", started)
	}
	if li := r.lastImport("held.example"); li.Outcome != "" || li.AttemptAt != nil {
		t.Fatalf("an outcome was written while the first run was still working: %+v", li)
	}

	open()
	if err := wait(); err != nil {
		t.Fatalf("the first run returned %v once its sitemap was answered", err)
	}
	if li := r.lastImport("held.example"); li.Outcome != repo.ImportOutcomeComplete {
		t.Fatalf("the first run recorded %+v; want complete", li)
	}
	r.wantLockFree()
}

// listHeld registers a site whose sitemap origin holds the FIRST request open
// until open is called, closing hit when that request arrives, and answers
// every later request at once with an empty sitemap. Holding only the first
// keeps a second run from waiting at the same gate, so a run that does not keep
// the lock fails the test rather than hanging it.
func (r *rig) listHeld(domain string) (hit <-chan struct{}, open func()) {
	r.t.Helper()
	o := r.list(domain, emptySitemap)
	arrived := make(chan struct{})
	gate := make(chan struct{})
	var taken atomic.Bool
	o.Handle("/sitemap.xml", func(w http.ResponseWriter, req *http.Request) {
		if taken.CompareAndSwap(false, true) {
			close(arrived)
			select {
			case <-gate:
			case <-req.Context().Done():
				return
			}
		}
		_, _ = w.Write([]byte(emptySitemap))
	})
	return arrived, sync.OnceFunc(func() { close(gate) })
}

// start runs the job in the background with the rig's config, as the run
// another invocation overlaps, and returns a function that waits for it and
// reports its result; calling that function again returns the same result.
func (r *rig) start() (wait func() error) {
	r.t.Helper()
	cfg := r.cfg
	done := make(chan error, 1)
	go func() {
		_, logger := testutil.NewLogCapture()
		done <- importer.RunAll(r.ctx, cfg, logger)
	}()
	var err error
	finished := sync.OnceFunc(func() { err = <-done })
	return func() error { finished(); return err }
}

// wantLockHeld fails the test unless the job's lock cannot be taken right now:
// another session holds it.
func (r *rig) wantLockHeld() {
	r.t.Helper()
	release, held, err := sharedb.TryAdvisoryLock(r.ctx, r.dsn, importer.LockKey)
	if err != nil {
		r.t.Fatalf("try the lock: %v", err)
	}
	if held {
		release()
		r.t.Fatal("the lock was free while a run was still working")
	}
}

// wantLockFree fails the test unless the job's lock can be taken right now,
// and gives it back.
func (r *rig) wantLockFree() {
	r.t.Helper()
	release, held, err := sharedb.TryAdvisoryLock(r.ctx, r.dsn, importer.LockKey)
	if err != nil || !held {
		r.t.Fatalf("the lock is still held after the run returned: held=%v err=%v", held, err)
	}
	release()
}

// TestRunAll_LockDatabaseErrorIsAFailure: a database that cannot be reached
// while taking the lock is an error, never a skip.
func TestRunAll_LockDatabaseErrorIsAFailure(t *testing.T) {
	r := newRig(t)
	r.cfg.DSN = "postgres://nobody:nothing@127.0.0.1:1/fora?sslmode=disable&connect_timeout=2"
	logs, err := r.run()
	if skipped := logs.Records(t, "importer.skipped"); err == nil || len(skipped) != 0 {
		t.Fatalf("a lock database error returned %v (skip lines: %v); want an error and no skip", err, skipped)
	}
}

// TestRunAll_ContinuesAfterAFailedListingAndExitsNonZero: the first listing's
// sitemap is unreachable, the second is valid; both outcomes are recorded and
// the run fails. With every listing complete the same run returns nil.
func TestRunAll_ContinuesAfterAFailedListingAndExitsNonZero(t *testing.T) {
	r := newRig(t)
	broken := r.list("a-broken.example", emptySitemap)
	broken.set("", http.StatusInternalServerError, 0)
	r.list("b-fine.example", emptySitemap)

	if _, err := r.run(); !errors.Is(err, importer.ErrIncomplete) {
		t.Fatalf("run = %v; want ErrIncomplete", err)
	}
	if li := r.lastImport("a-broken.example"); li.Outcome != repo.ImportOutcomeFailed || li.FailureReason == nil {
		t.Fatalf("the broken listing records %+v", li)
	}
	if li := r.lastImport("b-fine.example"); li.Outcome != repo.ImportOutcomeComplete {
		t.Fatalf("the listing after the failed one records %+v; want complete", li)
	}

	broken.set(emptySitemap, http.StatusOK, 0)
	if _, err := r.run(); err != nil {
		t.Fatalf("with every listing complete the run returned %v", err)
	}
}

// TestRunAll_ConfiguredLimitsReachTheWalker: the resource limit and the
// listing deadline from the config are what stop a walk, and each stop is
// recorded with its reason.
func TestRunAll_ConfiguredLimitsReachTheWalker(t *testing.T) {
	r := newRig(t)
	r.list("capped.example", sitemaptest.Urlset("https://capped.example/a", "https://capped.example/b"))
	slow := r.list("slow.example", emptySitemap)
	slow.set(emptySitemap, http.StatusOK, 5*time.Second)
	r.cfg.Limits = sitemapwalk.Limits{MaxResources: 1}
	r.cfg.ListingTimeout = 500 * time.Millisecond

	if _, err := r.run(); !errors.Is(err, importer.ErrIncomplete) {
		t.Fatalf("run = %v; want ErrIncomplete", err)
	}
	r.wantWalkStopped("capped.example", sitemapwalk.ReasonResources)
	r.wantWalkStopped("slow.example", sitemapwalk.ReasonDeadline)
}

// TestRunAll_RequestTimeoutReachesTheWalker: the request timeout from the
// config bounds each sitemap fetch on its own. A site slower than it fails the
// listing as request-timeout while the listing still has time left, which is
// what tells the operator which of the two timeouts to raise.
func TestRunAll_RequestTimeoutReachesTheWalker(t *testing.T) {
	r := newRig(t)
	slow := r.list("slow.example", emptySitemap)
	slow.set(emptySitemap, http.StatusOK, 5*time.Second)
	r.cfg.RequestTimeout = 200 * time.Millisecond
	r.cfg.ListingTimeout = 30 * time.Second

	if _, err := r.run(); !errors.Is(err, importer.ErrIncomplete) {
		t.Fatalf("run = %v; want ErrIncomplete", err)
	}
	r.wantWalkStopped("slow.example", sitemapwalk.ReasonRequestTimeout)
}

// wantWalkStopped checks that domain's listing recorded a failed run whose
// walk stopped for reason. The reason is matched as the token the importer
// writes, "(reason)": the bare word could also match the error text after it,
// as "deadline" matches "context deadline exceeded".
func (r *rig) wantWalkStopped(domain string, reason sitemapwalk.Reason) {
	r.t.Helper()
	token := "(" + string(reason) + ")"
	li := r.lastImport(domain)
	recorded := ""
	if li.FailureReason != nil {
		recorded = *li.FailureReason
	}
	if li.Outcome != repo.ImportOutcomeFailed || !strings.Contains(recorded, token) {
		r.t.Fatalf("the %s listing records %q, reason %q; want failed with %s", domain, li.Outcome, recorded, token)
	}
}

// TestRunAll_RefusesIncompleteConfig: each required setting is named when it
// is missing, before anything connects.
func TestRunAll_RefusesIncompleteConfig(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		name, want string
		mutate     func(*importer.Config)
	}{
		{"dsn", "EXCHANGE_DSN", func(c *importer.Config) { c.DSN = "" }},
		{"domain", "EXCHANGE_DOMAIN", func(c *importer.Config) { c.ExchangeDomain = "" }},
		{"url", "IMPORT_EXCHANGE_URL", func(c *importer.Config) { c.ExchangeURL = "" }},
		{"key", "IMPORT_KEY_FILE", func(c *importer.Config) { c.KeyPath = "" }},
	} {
		cfg := r.cfg
		tc.mutate(&cfg)
		err := importer.RunAll(r.ctx, cfg, testutil.DiscardLogger())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: run = %v; want an error naming %s", tc.name, err, tc.want)
		}
	}
}
