package forawellknown_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

// ErrNoDocument deliberately names no document, so that a missing document is
// never reported as a different one. That neutrality moves the whole diagnostic
// burden onto the URL.
//
// Nothing tested it. Every existing assertion in this package uses errors.Is,
// which passes on the bare sentinel, so the wrapping was free to disappear from
// any one path — and it had, on the Cache's remembered 404s. The tests below
// read the message text on purpose. They are the reason the promise in the
// ErrNoDocument comment can be believed.

// TestNoDocument_NamesTheDocumentItCouldNotFind covers the DIRECT fetch. The
// sentinel names no document, so the address is the only thing that says which
// one was missing. The overlay case is checked against the WBA path too,
// because a wrapper that named a fixed document would pass a same-document
// check.
func TestNoDocument_NamesTheDocumentItCouldNotFind(t *testing.T) {
	t.Parallel()
	origin := testutil.NewOrigin(nil)
	defer origin.Close()
	origin.SetManifestStatus(http.StatusNotFound)
	ctx := context.Background()
	opts := forawellknown.FetchOptions{Client: testutil.Client(), Scheme: "http"}

	_, err := forawellknown.Fetch(ctx, origin.Host(), opts)
	assertNamesPath(t, err, forawellknown.Path, forawellknown.WBAPath)
}

// TestNoDocument_CachedAbsenceSaysAsMuchAsTheFirstOne is the case that was
// broken. The Cache remembers a 404 for its negative TTL and answers later
// callers from that memory. Those answers returned the bare sentinel, so the
// SECOND caller learned strictly less than the first: no document name in the
// sentinel, and no address in the wrapper.
//
// The second Get is asserted to have taken the remembered path — the origin hit
// count does not move — so this cannot pass by quietly re-fetching.
func TestNoDocument_CachedAbsenceSaysAsMuchAsTheFirstOne(t *testing.T) {
	t.Parallel()
	origin := testutil.NewOrigin(nil)
	defer origin.Close()
	origin.SetManifestStatus(http.StatusNotFound)
	clk := clock.NewDeterministic(anchor)
	c := newPublisherCacheTTL(clk, time.Hour)
	ctx := context.Background()

	_, first := c.Get(ctx, origin.URL)
	assertNamesPath(t, first, forawellknown.Path, forawellknown.WBAPath)

	clk.Advance(time.Minute) // still inside the default 5m negative TTL
	_, second := c.Get(ctx, origin.URL)
	if got := origin.Hits(); got != 1 {
		t.Fatalf("origin hits = %d, want 1; the second Get re-fetched instead of "+
			"answering from the remembered 404, so it did not exercise that path", got)
	}
	assertNamesPath(t, second, forawellknown.Path, forawellknown.WBAPath)

	if first.Error() != second.Error() {
		t.Errorf("remembered absence reads differently from the first one:\n first  = %q\n second = %q",
			first, second)
	}
}

// assertNamesPath holds err to the ErrNoDocument contract: it is the sentinel,
// it names want, and it does not name notWant. The negative half matters as much
// as the positive one — a message that named both documents would be no more
// useful than one that named neither.
func assertNamesPath(t *testing.T, err error, want, notWant string) {
	t.Helper()
	if !errors.Is(err, forawellknown.ErrNoDocument) {
		t.Fatalf("err = %v, want ErrNoDocument", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q does not name %q, so a reader cannot tell which document was missing", err, want)
	}
	if strings.Contains(err.Error(), notWant) {
		t.Errorf("err = %q names %q, which is not the document that was fetched", err, notWant)
	}
}
