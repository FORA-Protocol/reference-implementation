package exchreg_test

import (
	"context"
	"errors"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/account"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/exchreg"
)

// TestResolveRegistrationRequirements_EveryRefusalCarriesBothVocabularies pins
// the seam contract, which is the whole reason this reader is shaped the way it
// is.
//
// Two layers ask different questions of the same error and neither can answer
// the other's. The SDK verb this reader plugs into classifies a refusal on the
// SDK's own verdict sentinels and reads anything else as a transport failure
// worth retrying, so a refusal missing its SDK sentinel is retried forever
// against a domain the deployment will never permit. The service above needs to tell a
// failed manifest READ from a failed SEND, and both arrive at it wearing the
// same SDK class, so it reads this repository's sentinel instead.
//
// Written as a table because the pairing is the rule. Checking each half where
// it happens to be raised is how one arm comes to carry a sentinel the arm
// beside it does not.
func TestResolveRegistrationRequirements_EveryRefusalCarriesBothVocabularies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// arrange returns the reader and the value to resolve.
		arrange func(t *testing.T) (*exchreg.Reader, string)
		// ours is the sentinel the account service classifies on.
		ours error
		// sdk is the sentinel the SDK's registration-requirements verb classifies
		// on, nil where the refusal is deliberately NOT a verdict and must read as
		// retryable. It is the verb's OWN set that decides this, not whichever
		// sentinel names the condition best -- see alsoCarries.
		sdk error
		// alsoCarries is a sentinel the refusal carries for a reader other than
		// the requirements verb, nil where there is none. Only the version row has
		// one: the routing tier reads it when the status leg meets the same
		// document, and it buys nothing on this seam. Asserted so it cannot be
		// dropped as redundant, and kept apart from sdk so nobody reads it as the
		// thing that makes this refusal final.
		alsoCarries error
	}{
		{
			name: "a value that is not a bare domain",
			arrange: func(t *testing.T) (*exchreg.Reader, string) {
				return newReader(t), "https://exchange.example/register"
			},
			ours: account.ErrNotBareDomain,
			sdk:  helpers.ErrInvalidHost,
		},
		{
			name: "a domain this deployment excludes",
			arrange: func(t *testing.T) (*exchreg.Reader, string) {
				origin := exchangeServing(t, testutil.FullRegistration)
				reader, err := exchreg.New(exchreg.Config{
					Fetch:  rwtestutil.Client(),
					Scheme: "http",
					Allow:  func(string) bool { return false },
				})
				if err != nil {
					t.Fatalf("exchreg.New: %v", err)
				}
				return reader, origin.Host()
			},
			ours: account.ErrNotPermitted,
			sdk:  resolvers.ErrExchangeNotPermitted,
		},
		{
			name: "a manifest describing another role",
			arrange: func(t *testing.T) (*exchreg.Reader, string) {
				origin := rwtestutil.NewOrigin(rwtestutil.MarshalManifest(
					rwtestutil.Manifest(forawellknown.RolePublisher, "publisher.example"),
				))
				t.Cleanup(origin.Close)
				return newReader(t), origin.Host()
			},
			ours: account.ErrRequirementsUnread,
			sdk:  resolvers.ErrManifestNotExchange,
		},
		{
			// A document that arrived, parsed, carried a version this build
			// accepts, and then failed the document rules. This reader
			// validates the whole manifest where the SDK's own validates
			// nothing beyond the role, so this refusal exists only here --
			// which is why the seam needs a way to say it is final. A body
			// carrying nothing but a usable version is the cheapest way to
			// reach it; the wire-shape and field-rule failures beside it carry
			// the same sentinel and get the same answer, because the origin
			// served a document this participant cannot use either way.
			name: "a manifest this reader cannot use",
			arrange: func(t *testing.T) (*exchreg.Reader, string) {
				origin := rwtestutil.NewOrigin([]byte(`{"ver":"` + forawellknown.Version + `"}`))
				t.Cleanup(origin.Close)
				return newReader(t), origin.Host()
			},
			ours: account.ErrRequirementsUnread,
			sdk:  resolvers.ErrManifestUnusable,
		},
		{
			// A document whose version this build does not implement. Separate
			// from the row above in THIS repository's vocabulary because the
			// repair differs: no correction of the document's shape resolves it,
			// and it is decided before any other member is read.
			//
			// At the SDK seam it is the SAME verdict as the row above, and that
			// is the point of this row. The requirements verb treats four
			// sentinels as final and the version one is not among them, so the
			// unusable-manifest sentinel is what makes this refusal final here.
			// Carrying only the version sentinel reports a refusal that answers
			// the same on every attempt as one worth retrying, which is the one
			// answer a refused manifest version must never get.
			name: "a manifest whose document version this build does not accept",
			arrange: func(t *testing.T) (*exchreg.Reader, string) {
				m := rwtestutil.Manifest(forawellknown.RoleExchange, "exchange.example")
				m.Ver = "2.0"
				origin := rwtestutil.NewOrigin(rwtestutil.MarshalManifest(m))
				t.Cleanup(origin.Close)
				return newReader(t), origin.Host()
			},
			ours:        account.ErrManifestVersionRefused,
			sdk:         resolvers.ErrManifestUnusable,
			alsoCarries: resolvers.ErrManifestVersionRefused,
		},
		{
			// The other row with no SDK sentinel, and the one that separates
			// "served nothing readable" from "served a bad document". A proxy
			// answering 200 with an error page produces this. Nothing was
			// validated, so it says nothing about what the Exchange publishes and
			// the next fetch may return the real manifest -- which is why it must
			// NOT arrive as the verdict the row above carries, even though both
			// are a body this reader could not turn into requirements.
			name: "a body that is not JSON at all",
			arrange: func(t *testing.T) (*exchreg.Reader, string) {
				origin := rwtestutil.NewOrigin([]byte(`<html>502 Bad Gateway</html>`))
				t.Cleanup(origin.Close)
				return newReader(t), origin.Host()
			},
			ours: account.ErrRequirementsUnread,
			sdk:  nil,
		},
		{
			// The original row with no SDK sentinel. A manifest that could not
			// be fetched at all may well be fetchable on the
			// next try, so it must reach the SDK as a transport failure rather
			// than as a verdict -- and a verdict sentinel here would make the
			// verb refuse finally on a fault that clears itself.
			name: "a manifest that could not be fetched",
			arrange: func(t *testing.T) (*exchreg.Reader, string) {
				origin := rwtestutil.NewOrigin(nil)
				t.Cleanup(origin.Close)
				origin.SetManifestStatus(404)
				return newReader(t), origin.Host()
			},
			ours: account.ErrRequirementsUnread,
			sdk:  nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader, exchange := tc.arrange(t)

			_, err := reader.ResolveRegistrationRequirements(context.Background(), exchange)
			if err == nil {
				t.Fatal("the read succeeded, so there is no refusal to classify")
			}
			if !errors.Is(err, tc.ours) {
				t.Errorf("err = %v, want it to wrap %v so the account service can name the kind", err, tc.ours)
			}
			if tc.sdk != nil && !errors.Is(err, tc.sdk) {
				t.Errorf("err = %v, want it to wrap %v -- that is the sentinel the "+
					"registration-requirements verb reads, so without it the refusal is "+
					"reported as a transport failure worth retrying", err, tc.sdk)
			}
			if tc.alsoCarries != nil && !errors.Is(err, tc.alsoCarries) {
				t.Errorf("err = %v, want it to wrap %v as well; it buys nothing at this seam "+
					"but it is how the routing tier names the same condition on the status leg",
					err, tc.alsoCarries)
			}
			if tc.sdk != nil {
				return
			}
			// Every sentinel that could make a refusal final at EITHER seam, not
			// only the four this verb reads. A retryable refusal must carry none
			// of them: one that leaks through is a fetch failure the seam would
			// stop retrying, which is this table's defect pointed the other way.
			// Written wider than the verb's own set on purpose, so a sentinel
			// that becomes load-bearing later is already covered here.
			for _, verdict := range []error{
				helpers.ErrInvalidHost,
				resolvers.ErrExchangeNotPermitted,
				resolvers.ErrManifestNotExchange,
				resolvers.ErrManifestUnusable,
				resolvers.ErrManifestVersionRefused,
			} {
				if errors.Is(err, verdict) {
					t.Errorf("err = %v wraps %v, so the SDK reads a transient fetch failure as final", err, verdict)
				}
			}
		})
	}
}
