package forawellknown_test

// What NewDirectoryFetcher adds to the SDK's directory fetcher, each part driven
// against a testutil origin: the client, the clock and the time limit. The rules
// of the read itself (no redirect, the media type, the response signatures) are
// the SDK's; offerkeys and agentreg pin how their callers answer each failure.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

// servedDirectory serves a one-key directory signed by that key, on an origin
// newOrigin starts (testutil.NewTLSOrigin or the plaintext testutil.NewOrigin).
func servedDirectory(t *testing.T, newOrigin func([]byte) *testutil.Origin) *testutil.Origin {
	t.Helper()
	priv, key := testutil.NewSigningKey("directory-fetch", time.Unix(0, 0), time.Unix(4_100_000_000, 0))
	o := newOrigin(nil)
	t.Cleanup(o.Close)
	o.SetWBA(testutil.MarshalWBA(testutil.WBAFile(key)))
	o.SetWBASigners(priv)
	return o
}

func TestNewDirectoryFetcher_NilClientDialsNothing(t *testing.T) {
	t.Parallel()
	o := servedDirectory(t, testutil.NewTLSOrigin)
	_, err := forawellknown.NewDirectoryFetcher(nil, nil, 0)(context.Background(), o.Host())
	if !errors.Is(err, forawellknown.ErrNoClient) {
		t.Fatalf("a fetcher with no client answered %v, want ErrNoClient", err)
	}
	if n := o.WBAHits(); n != 0 {
		t.Fatalf("a fetcher with no client reached the directory %d times, want 0", n)
	}
}

func TestNewDirectoryFetcher_EndsAReadAtItsTimeLimit(t *testing.T) {
	t.Parallel()
	o := servedDirectory(t, testutil.NewTLSOrigin)
	t.Cleanup(o.BlockWBA())
	start := time.Now()
	_, err := forawellknown.NewDirectoryFetcher(o.HTTPClient(), nil, 100*time.Millisecond)(
		context.Background(), o.Host())
	if !errors.Is(err, resolvers.ErrDirectoryUnavailable) {
		t.Fatalf("a directory that never answers gave %v, want ErrDirectoryUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the read took %v; the 100ms limit did not end it", elapsed)
	}
}

func TestNewDirectoryFetcher_JudgesResponseSignaturesAtItsClock(t *testing.T) {
	t.Parallel()
	o := servedDirectory(t, testutil.NewTLSOrigin)
	keysAt := func(clk interface{ Now() time.Time }) int {
		t.Helper()
		f, err := forawellknown.NewDirectoryFetcher(o.HTTPClient(), clk, 0)(context.Background(), o.Host())
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		return len(f.GetKeys())
	}
	if n := keysAt(nil); n != 1 {
		t.Fatalf("under the system clock the directory listed %d keys, want 1", n)
	}
	// The fixture signs every response for 2001 to 2096. Past that window the
	// response signature does not verify, so the directory keeps no key.
	if n := keysAt(clock.NewDeterministic(time.Unix(4_000_000_001, 0).UTC())); n != 0 {
		t.Fatalf("past the response signature's window the directory listed %d keys, want 0", n)
	}
}

func TestNewDirectoryFetcher_ReadsOverHTTPSOnly(t *testing.T) {
	t.Parallel()
	o := servedDirectory(t, testutil.NewOrigin)
	_, err := forawellknown.NewDirectoryFetcher(o.HTTPClient(), nil, 0)(context.Background(), o.Host())
	if !errors.Is(err, resolvers.ErrDirectoryUnavailable) {
		t.Fatalf("a plaintext-only directory gave %v, want ErrDirectoryUnavailable", err)
	}
	if n := o.WBAHits(); n != 0 {
		t.Fatalf("the plaintext directory was served %d times, want 0", n)
	}
}
