package forawellknown

import (
	"context"
	"net/http"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
)

// NewDirectoryFetcher returns how this repository reads a Web Bot Auth key
// directory it does not serve: the SDK's resolvers.NewWBADirectoryFetcher over
// the caller's client and clock, with each read bounded by timeout.
//
// The SDK applies every rule of the read. It fetches over https from the host's
// own authority, follows no redirect whatever the client's own redirect policy,
// requires a 200 answer under the directory media type and a body of at most
// 1 MiB, checks the response signatures at clk, and keeps only the keys that
// signed. Request verification (internal/agentkeys, through the SDK's
// WBAKeyResolver) reads a directory through the same SDK path, so the two
// cannot disagree about one document. FORA_WELLKNOWN_SCHEME and
// FORA_WELLKNOWN_PORT never apply here.
//
// The domain is pasted into the URL as given, so a caller vets it first: a
// userinfo part would be sent as a Basic credential, and a path would move the
// fetch. offerkeys requires a bare domain; agentreg passes the anchored host of
// the identity.
//
// A nil client fails every read with ErrNoClient before anything is dialled,
// as Fetch does; the SDK would otherwise install its own guarded client, which
// ignores SKIP_SSRF. A nil clock is the system clock. A timeout of zero or
// less is the package default of five seconds.
func NewDirectoryFetcher(
	client *http.Client, clk interface{ Now() time.Time }, timeout time.Duration,
) resolvers.OfferDirectoryFetcher {
	if client == nil {
		return func(context.Context, string) (*WBAFile, error) { return nil, ErrNoClient }
	}
	if clk == nil {
		clk = clock.System{}
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	fetch := resolvers.NewWBADirectoryFetcher(client, "", "", resolvers.WBADirectoryFetcherOptions{Now: clk.Now})
	return func(ctx context.Context, domain string) (*WBAFile, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return fetch(ctx, domain)
	}
}
