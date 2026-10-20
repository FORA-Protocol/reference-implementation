//go:build integration

package exchangetest

import (
	"crypto/ed25519"
	"net/http"
	"strings"

	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraauth"
	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

// SigningTransport signs every /fora.* request under the Web Bot Auth profile
// with priv, as the SDK signs a real caller's. directory names the signer: an
// https origin, or a bare host the signer is reached at, which signs as
// https://<host> — the only form a Signature-Agent member takes. Both the
// ExchangeService and the CatalogService paths are signed; anything else, such
// as agents/register, goes out unsigned.
//
// The SDK transport puts a fresh nonce on every signature, so two identical
// calls in one second still carry different signatures and the replay store
// accepts both. That lets a test exercise the service's own idempotency. The
// signature lives for the profile's five-minute ceiling.
func SigningTransport(base http.RoundTripper, directory string, priv ed25519.PrivateKey) http.RoundTripper {
	return core.NewSigningTransport(
		Signer(priv), base,
		core.WithSignPredicate(foraauth.IsFORAProcedure),
		core.WithSignatureAgent(SignatureAgentOrigin(directory)),
		core.WithWindow(core.ClockWindow(clock.System{}.Now, SignatureTTL)),
	)
}

// SignatureTTL is the lifetime of a signature made by SigningTransport: the
// longest the Web Bot Auth profile admits (helpers.MaxSignatureLifetime).
const SignatureTTL = helpers.MaxSignatureLifetime

// Signer is priv as an RFC 9421 signer whose keyid is the key's RFC 7638
// thumbprint, the keyid a Web Bot Auth directory publishes it under.
func Signer(priv ed25519.PrivateKey) helpers.Signer {
	signer, err := helpers.NewEd25519Signer(rwtestutil.MustThumbprintPriv(priv), priv)
	if err != nil {
		panic(err)
	}
	return signer
}

// SignatureAgentOrigin is the Signature-Agent member a test signer named by
// directory signs with: directory itself when it already carries a scheme, and
// https://<directory> for a bare host.
func SignatureAgentOrigin(directory string) string {
	if strings.Contains(directory, "://") {
		return directory
	}
	return "https://" + directory
}
