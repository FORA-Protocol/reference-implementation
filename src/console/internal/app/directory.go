package app

import (
	"crypto/ed25519"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	wkserver "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
)

// keyValidity is how long the console's published key is declared valid for.
//
// The window is what bounds a stale copy of the directory: a verifier that
// cached it before a rotation stops accepting the old key when the window
// lapses, without anyone having to act. Ninety days matches what the other
// services publish, so an operator reading two directories sees one convention.
const keyValidity = 90 * 24 * time.Hour

// keyClockSkew backdates the published not_before so a verifier whose clock
// runs slightly behind the console still accepts a freshly built document. One
// hour is the allowance the Exchange and the Broker apply to their own keys.
const keyClockSkew = time.Hour

// clockedKey publishes the console's key with a window read from the clock
// each time the document is built. A window stamped once at start-up would
// lapse after keyValidity while the process stayed up, and from then on the
// Exchange would refuse every request the console signs.
type clockedKey struct {
	pub ed25519.PublicKey
	clk clock.Clock
}

func (k clockedKey) Keys() []*forawellknown.Key {
	now := k.clk.Now()
	return []*forawellknown.Key{forawellknown.NewKey(k.pub, now.Add(-keyClockSkew), now.Add(keyValidity))}
}

// directory serves the console's own Web Bot Auth key directory.
//
// This is the other half of how the Exchange trusts this console: the console
// signs with its private key, and the Exchange fetches this document to learn
// the public one. Serving it from the console itself, at the origin the console
// names in every signature, is what keeps the two from disagreeing -- there is
// no second place to publish the key to and forget.
//
// Nothing secret is served. The document carries the public half only, which is
// what a verifier needs and all it needs.
//
// The served document is rebuilt on a request that arrives more than
// wkserver.RebuildIntervalFor(keyValidity) after the last build, so its window
// keeps moving with the clock without a background goroutine. A failed rebuild
// is logged and the previous document keeps being served; the next request
// tries again.
type directory struct {
	doc   *wkserver.Handler
	clk   clock.Clock
	every time.Duration

	mu      sync.Mutex
	builtAt time.Time
}

func directoryHandler(priv ed25519.PrivateKey, clk clock.Clock) (*directory, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("app: the console signing key is not Ed25519")
	}
	// The directory response is signed by the key it lists, under the key's RFC
	// 7638 thumbprint, as every key directory's is: a verifier hands out only
	// the keys that signed the response it fetched.
	keyID, err := helpers.Thumbprint(pub)
	if err != nil {
		return nil, fmt.Errorf("app: console key thumbprint: %w", err)
	}
	signer, err := helpers.NewEd25519Signer(keyID, priv)
	if err != nil {
		return nil, fmt.Errorf("app: console directory signer: %w", err)
	}
	builtAt := clk.Now()
	doc, err := wkserver.NewWBAHandler(wkserver.WBAConfig{
		Keys:    clockedKey{pub: pub, clk: clk},
		Signers: []helpers.Signer{signer},
		Now:     clk.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("app: build the console key directory: %w", err)
	}
	return &directory{
		doc:     doc,
		clk:     clk,
		every:   wkserver.RebuildIntervalFor(keyValidity),
		builtAt: builtAt,
	}, nil
}

func (d *directory) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.refreshIfStale(r)
	d.doc.ServeHTTP(w, r)
}

// refreshIfStale rebuilds the document when the last build is older than the
// rebuild interval. The lock keeps two concurrent requests from both
// rebuilding; the directory is fetched rarely, so holding it is cheap.
func (d *directory) refreshIfStale(r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.clk.Now()
	if now.Sub(d.builtAt) < d.every {
		return
	}
	if err := d.doc.Rebuild(); err != nil {
		reqctx.FromContext(r.Context()).WarnContext(r.Context(), "wellknown.rebuild_failed",
			"path", DirectoryPath, "err", err)
		return
	}
	d.builtAt = now
}

// DirectoryPath is where the console publishes its key directory. It is the
// fixed path every FORA participant serves one at, so the Exchange needs only
// the console's origin to find it.
const DirectoryPath = forawellknown.WBAPath
