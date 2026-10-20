package exchreg_test

import (
	"context"
	"errors"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

// TestReaderRefusals_AreFinalOrRetryableAsTheSeamClassifiesThem asks the SDK what
// this reader's sentinels actually buy, which is the question the sibling
// contract table cannot ask.
//
// That table checks which sentinels a refusal CARRIES. This one drives the verb
// those sentinels are for and reads the class it returns. The difference is the
// whole defect this test exists for: a refusal can carry a sentinel that names
// its condition perfectly and still be classified as retryable, because the seam
// reading it does not have that sentinel in its verdict set. Nothing in the
// carrying half can see that.
//
// The SDK keeps two verdict sets. A refused manifest version is in the ROUTING
// tier's set, which the status leg goes through. It is not in the
// registration-requirements tier's set, which is the seam this reader plugs
// into, and the register leg reads requirements before it routes -- so on that
// leg the routing tier never runs and the version sentinel alone buys nothing. A
// refused manifest version is a verdict: never cached, never retried
// (ADR-025 D4).
//
// Nothing is dialled beyond the loopback origin. The requirements read fails
// before the verb resolves an endpoint, so no signer, key or peer is needed.
func TestReaderRefusals_AreFinalOrRetryableAsTheSeamClassifiesThem(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		// serve prepares the origin the reader will fetch and returns the domain
		// to register at.
		serve func(t *testing.T) string
		// want is the class the SDK's registration verb must report.
		want connect.CallErrorKind
		// why says what breaks if the class is the other one.
		why string
	}{
		"a manifest whose document version this build does not accept": {
			serve: func(t *testing.T) string {
				t.Helper()
				origin := rwtestutil.NewOrigin(nil)
				t.Cleanup(origin.Close)
				origin.SetManifest(rwtestutil.ExchangeManifestAtVersion(
					"exchange.example", origin.URL, "2.0",
				))
				return origin.Host()
			},
			want: connect.CallNotSent,
			why: "the origin runs a manifest layout this build does not implement, so every " +
				"retry fetches the same document and gets the same refusal",
		},
		"a manifest this reader cannot use": {
			serve: func(t *testing.T) string {
				t.Helper()
				origin := rwtestutil.NewOrigin([]byte(`{"ver":"1.0"}`))
				t.Cleanup(origin.Close)
				return origin.Host()
			},
			want: connect.CallNotSent,
			why:  "the document arrived and broke the rules, so the next fetch returns the same bytes",
		},
		"a manifest that could not be fetched": {
			serve: func(t *testing.T) string {
				t.Helper()
				origin := rwtestutil.NewOrigin(nil)
				t.Cleanup(origin.Close)
				origin.SetManifestStatus(503)
				return origin.Host()
			},
			want: connect.CallUnreachable,
			why: "a manifest that could not be fetched at all may well be fetchable on the next " +
				"try, and calling it final drops a registration over a momentary outage",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			exchange := tc.serve(t)

			// The reader is the real one, wired the way the composition root wires
			// it, so what the seam classifies is what production hands it.
			client := connect.NewClient(
				"https://home.invalid",
				connect.WithRegistrationRequirements(newReader(t)),
			)

			// TermsDigest is deliberately unset: that is what asks the verb to read
			// this Exchange's requirements, which is the leg under test.
			_, err := client.Register(context.Background(), &forav1.RegisterRequest{
				Ver:      helpers.ProtocolVersion,
				Exchange: exchange,
			})
			if err == nil {
				t.Fatal("the registration succeeded, so there is no refusal to classify")
			}
			var callErr *connect.CallError
			if !errors.As(err, &callErr) {
				t.Fatalf("err = %v is not an SDK call failure, so the verb classified nothing", err)
			}
			if callErr.Kind != tc.want {
				t.Errorf("the seam classified this as %s, want %s -- %s",
					callErr.Kind, tc.want, tc.why)
			}
		})
	}
}
