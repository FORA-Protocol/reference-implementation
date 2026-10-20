package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
)

// DirectoryMediaType is the media type a Web Bot Auth key directory is served
// under. A verifier refuses a directory served as anything else, the JWK Set
// type included.
const DirectoryMediaType = forawellknown.WBAMediaType

// DirectorySignatureWindow is how long one directory response signature stays
// valid. A directory is cached by verifiers and by any CDN in front of it, so the
// window is longer than a request signature's five minutes; it is still bounded,
// so a captured response stops verifying on its own.
const DirectorySignatureWindow = time.Hour

// defaultSignatureCacheLimit bounds how many authorities one SignatureCache
// remembers. The authority comes from the request's Host header, so without a
// bound a caller could grow the cache with invented hosts. Past the bound the
// cache starts over, which costs one signature per authority and nothing else.
const defaultSignatureCacheLimit = 256

// ErrNoAuthority is returned when a directory request names no host, so there is
// no authority for the response signature to cover.
var ErrNoAuthority = errors.New("forawellknown/server: the directory request names no host")

// RequestAuthority is the RFC 9421 @authority of the request that fetched a
// directory, as the fetching client derives it: the Host header, lowercased,
// without the :443 an https client never sends. A proxy in front of the service
// must preserve the Host the client used, or every response signature names the
// wrong authority and verifiers refuse the directory.
func RequestAuthority(r *http.Request) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(r.Host), ":443")
	if host == "" {
		return "", ErrNoAuthority
	}
	return host, nil
}

// SignatureCache keeps one directory response signature per authority, so a
// directory fetched often is signed once per window rather than once per request.
// An entry is reused while the body it was made for is unchanged and more than
// half of its window remains. It is safe for concurrent use.
type SignatureCache struct {
	now   func() time.Time
	limit int

	mu      sync.Mutex
	entries map[string]cachedSignature
}

type cachedSignature struct {
	digest  string
	renewAt time.Time
	sig     helpers.DirectoryResponseSignature
}

// NewSignatureCache builds a cache reading time from now (nil means the system clock)
// and remembering at most limit authorities (zero or less means the default).
func NewSignatureCache(now func() time.Time, limit int) *SignatureCache {
	if now == nil {
		now = clock.System{}.Now
	}
	if limit <= 0 {
		limit = defaultSignatureCacheLimit
	}
	return &SignatureCache{now: now, limit: limit, entries: map[string]cachedSignature{}}
}

// Sign returns the response signature of body served under authority, made by
// every signer once (helpers.SignDirectoryResponse), reusing a cached one when it
// still applies.
func (c *SignatureCache) Sign(
	ctx context.Context, authority string, body []byte, signers []helpers.Signer,
) (helpers.DirectoryResponseSignature, error) {
	digest := helpers.ContentDigest(body)
	now := c.now()
	c.mu.Lock()
	entry, ok := c.entries[authority]
	c.mu.Unlock()
	if ok && entry.digest == digest && now.Before(entry.renewAt) {
		return entry.sig, nil
	}
	sig, err := helpers.SignDirectoryResponse(ctx, authority, body, signers,
		now.Unix(), now.Add(DirectorySignatureWindow).Unix())
	if err != nil {
		return helpers.DirectoryResponseSignature{}, err
	}
	c.mu.Lock()
	if len(c.entries) >= c.limit {
		c.entries = map[string]cachedSignature{}
	}
	c.entries[authority] = cachedSignature{
		digest: digest, renewAt: now.Add(DirectorySignatureWindow / 2), sig: sig,
	}
	c.mu.Unlock()
	return sig, nil
}

// WriteDirectory answers r with body as a signed key directory: 200, the
// directory media type, and one response signature per signer covering the
// authority r was fetched under and the body's digest. A directory always answers
// itself and never redirects. A request naming no host, or a signer that fails,
// is answered 500 and reported to the caller to log; nothing unsigned is served.
func WriteDirectory(
	w http.ResponseWriter, r *http.Request, body []byte, signers []helpers.Signer, cache *SignatureCache,
) error {
	authority, err := RequestAuthority(r)
	if err == nil {
		var sig helpers.DirectoryResponseSignature
		if sig, err = cache.Sign(r.Context(), authority, body, signers); err == nil {
			sig.Apply(w.Header())
			w.Header().Set("Content-Type", DirectoryMediaType)
			_, _ = w.Write(body)
			return nil
		}
	}
	http.Error(w, "key directory unavailable", http.StatusInternalServerError)
	return fmt.Errorf("forawellknown/server: sign key directory response: %w", err)
}

// signersFor returns the signers whose keyid is the thumbprint of a key the
// directory lists, in key order, and refuses a directory listing a key no signer
// holds. Every listed key must sign the response, because a verifier hands out
// only the keys that did.
func signersFor(keys []*forawellknown.Key, signers []helpers.Signer) ([]helpers.Signer, error) {
	byKeyID := make(map[string]helpers.Signer, len(signers))
	for _, s := range signers {
		if s != nil {
			byKeyID[s.KeyID()] = s
		}
	}
	out := make([]helpers.Signer, 0, len(keys))
	seen := map[string]bool{}
	for _, k := range keys {
		tp, err := forawellknown.Thumbprint(k)
		if err != nil {
			return nil, fmt.Errorf("forawellknown/server: listed key: %w", err)
		}
		if seen[tp] {
			continue
		}
		seen[tp] = true
		s, ok := byKeyID[tp]
		if !ok {
			return nil, fmt.Errorf("forawellknown/server: no signer holds listed key %s, "+
				"so the directory response could not be signed by it", tp)
		}
		out = append(out, s)
	}
	return out, nil
}
