//go:build integration

package transport_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/catalogfeed"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/catalogid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk/sitemaptest"
)

// What every catalog push carries on the wire: the automatic import labels
// each push it makes and the manual path never does, and every push of one run
// carries that run's one request id. The automatic import runs here as the
// whole job, importer.RunAll, the function the fora-import command runs with
// the configuration it reads from the environment; the manual path is
// fora-ingest's own pipeline. Both run against the real Exchange through the
// rig's verdictProxy, which forwards every push the way the TLS proxy in front
// of a deployment does -- headers cloned, trailers dropped -- and records the
// headers each push carried. The proxy stands in for that hop: the Caddy
// server a deployment runs there is not part of any stack these tests run,
// so that one hop is not exercised here. Its configuration in front of the
// Exchange has no rule that touches request headers. Whose row a pushed page is
// (catalog.auto_imported) has no public read, so it is read through the
// importer's own repository port (storedPage).

// pushRecorder records the request headers of every push a verdictProxy
// forwards.
type pushRecorder struct {
	mu      sync.Mutex
	headers []http.Header
}

// hook is the verdictProxy hook: it records and lets every push through.
func (p *pushRecorder) hook(_ int, _ http.ResponseWriter, req *http.Request) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.headers = append(p.headers, req.Header.Clone())
	return false
}

// seen returns the headers recorded so far, one set per forwarded push.
func (p *pushRecorder) seen() []http.Header {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]http.Header(nil), p.headers...)
}

// wantOneRequestID checks that every push carried exactly one X-Request-ID and
// that all of them carried the same one, and returns it.
func wantOneRequestID(t *testing.T, pushes []http.Header) string {
	t.Helper()
	if len(pushes) == 0 {
		t.Fatal("no push reached the proxy")
	}
	var id string
	for i, h := range pushes {
		got := h.Values(reqctx.HeaderRequestID)
		if len(got) != 1 || got[0] == "" {
			t.Fatalf("push %d carried %s %q; want exactly one", i+1, reqctx.HeaderRequestID, got)
		}
		if i == 0 {
			id = got[0]
		} else if got[0] != id {
			t.Fatalf("push %d carried %s %q and push 1 %q; want one id for the whole run",
				i+1, reqctx.HeaderRequestID, got[0], id)
		}
	}
	return id
}

// TestImporter_LabelsEveryPush: a run of the job that needs two submissions
// labels both, exactly once each, and sends both with the run's one request id,
// the id every importer.* line of that run carries; the Exchange accepts both,
// the job returns nil, and the Exchange records every page the run imported as
// the import's own.
func TestImporter_LabelsEveryPush(t *testing.T) {
	r := newImportRig(t, nil)
	pages := sitePages(300)
	r.sitemap.serve(sitemaptest.Urlset(pages...))
	var pushes pushRecorder
	r.viaProxy(pushes.hook)
	bound, err := ingest.EntriesPerSubmission()
	if err != nil {
		t.Fatal(err)
	}

	logs, err := r.runJob()
	if err != nil {
		t.Fatalf("the job returned %v; want nil", err)
	}
	want(t, r.recorded(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 300, Accepted: 300})
	logs.AssertLoggedOnce(t, "importer.listing", "INFO")
	seen := pushes.seen()
	if wantPushes := (300 + bound - 1) / bound; len(seen) != wantPushes {
		t.Fatalf("the proxy forwarded %d pushes, want the run's %d submissions", len(seen), wantPushes)
	}
	for i, h := range seen {
		if label := h.Values(catalogid.SourceHeader); len(label) != 1 || label[0] != catalogid.SourceAutoImport {
			t.Errorf("push %d carried %s %q, want exactly %q", i+1, catalogid.SourceHeader, label, catalogid.SourceAutoImport)
		}
	}
	wantRunLinesCarry(t, logs, wantOneRequestID(t, seen))
	for _, page := range []string{pages[0], pages[299]} {
		if !storedPage(t, r.h, page).AutoImported {
			t.Errorf("%s, imported by a labelled push, is not recorded as the import's", page)
		}
	}
}

// wantRunLinesCarry checks that every importer.* record a run logged carries
// id as its request_id: the id the run's pushes carried, so the Exchange's
// lines for a push and the run's own lines are found by one id.
func wantRunLinesCarry(t *testing.T, logs *testutil.LogCapture, id string) {
	t.Helper()
	lines := 0
	for _, rec := range testutil.DecodeRecords(t, []byte(strings.Join(logs.Lines(), "\n"))) {
		msg, _ := rec["msg"].(string)
		if !strings.HasPrefix(msg, "importer.") {
			continue
		}
		lines++
		if got, _ := rec["request_id"].(string); got != id {
			t.Errorf("%s carried request_id %q; the run's pushes carried %s %q", msg, got, reqctx.HeaderRequestID, id)
		}
	}
	if lines == 0 {
		t.Fatal("the run logged no importer.* line")
	}
}

// TestIngest_NeverLabelsItsPushes: fora-ingest's own pipeline pushes pages by
// hand with the same contributor key the import signs with, and none of its
// pushes carries the label, so the Exchange records its pages as someone
// else's than the import's. The key is not what tells the two apart; the label
// is.
func TestIngest_NeverLabelsItsPushes(t *testing.T) {
	r := newImportRig(t, nil)
	var pushes pushRecorder
	proxy := newVerdictProxy(t, r.h.server.URL, pushes.hook)
	paths := []string{"/news/by-hand-a", "/news/by-hand-b"}

	r.pushManual(proxy.URL, handRecord(paths[0]), handRecord(paths[1]))
	seen := pushes.seen()
	if len(seen) == 0 {
		t.Fatal("the manual push never reached the proxy")
	}
	for i, h := range seen {
		if label := h.Values(catalogid.SourceHeader); len(label) != 0 {
			t.Errorf("manual push %d carried %s %q; it must carry none", i+1, catalogid.SourceHeader, label)
		}
	}
	for _, path := range paths {
		if storedPage(t, r.h, "https://"+importDomain+path).AutoImported {
			t.Errorf("%s, pushed by hand, is recorded as the import's", path)
		}
	}
}

// TestIngest_OneRequestIDPerRun: every submission of one fora-ingest run
// carries the same request id, so the Exchange's log lines for the run share
// it; and a run whose context already carries an id sends that one.
func TestIngest_OneRequestIDPerRun(t *testing.T) {
	r := newImportRig(t, nil)
	bound, err := ingest.EntriesPerSubmission()
	if err != nil {
		t.Fatal(err)
	}
	records := make([]catalogfeed.Record, 0, 300)
	for i := range 300 {
		records = append(records, handRecord(fmt.Sprintf("/news/by-hand-%03d", i)))
	}
	wantPushes := (len(records) + bound - 1) / bound

	var fresh pushRecorder
	r.pushManual(newVerdictProxy(t, r.h.server.URL, fresh.hook).URL, records...)
	if got := fresh.seen(); len(got) != wantPushes {
		t.Fatalf("the run made %d pushes, want %d", len(got), wantPushes)
	}
	wantOneRequestID(t, fresh.seen())

	const given = "ingest-run-under-test"
	var kept pushRecorder
	r.pushManualWith(reqctx.WithID(r.h.ctx, given), newVerdictProxy(t, r.h.server.URL, kept.hook).URL, records...)
	if id := wantOneRequestID(t, kept.seen()); id != given {
		t.Fatalf("a run whose context carries %q sent %q", given, id)
	}
}
