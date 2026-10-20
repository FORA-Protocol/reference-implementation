package exchpolicy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraroute"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/account"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/exchpolicy"
)

// countingDelegate stands in for the SDK's endpoint resolver. It answers rather
// than fails, so a test asserting "the delegate was never asked" cannot pass
// because the delegate was asked and errored.
type countingDelegate struct {
	calls int
	last  string
}

func (d *countingDelegate) ResolveEndpoint(_ context.Context, host string) (string, error) {
	d.calls++
	d.last = host
	return "https://" + host + "/api", nil
}

func mustPolicy(t *testing.T, raw string) *exchpolicy.Allowlist {
	t.Helper()
	policy, err := exchpolicy.New(raw)
	if err != nil {
		t.Fatalf("New(%q): %v", raw, err)
	}
	return policy
}

// TestPermittedEndpoints_RefusesAnExcludedDomainBeforeAsking pins the whole
// point of the wrapper: the refusal happens here, and it says both things it has
// to say.
//
// Two sentinels, because two readers ask different questions of the same error.
// The account service reads ours to tell an operator's own policy from a peer
// that would not answer -- the distinction the wrapper exists to restore. The
// SDK reads its own to decide whether the caller should try again.
func TestPermittedEndpoints_RefusesAnExcludedDomainBeforeAsking(t *testing.T) {
	t.Parallel()
	delegate := &countingDelegate{}
	r := exchpolicy.NewPermittedEndpoints(mustPolicy(t, "listed.example"), delegate)

	endpoint, err := r.ResolveEndpoint(context.Background(), "excluded.example")
	if err == nil {
		t.Fatalf("an excluded domain resolved to %q", endpoint)
	}
	if !errors.Is(err, account.ErrNotPermitted) {
		t.Errorf("err = %v, want it to wrap ErrNotPermitted so the account service "+
			"reports the operator's policy rather than a peer that refused", err)
	}
	if !errors.Is(err, resolvers.ErrNoEndpoint) {
		t.Errorf("err = %v, want it to wrap a routing sentinel the SDK recognises", err)
	}
	if delegate.calls != 0 {
		t.Errorf("the delegate was asked %d times about an excluded domain; the refusal "+
			"must precede it, so nothing is fetched", delegate.calls)
	}
}

// TestPermittedEndpoints_RefusalIsFinal is the half a sentinel choice can get
// wrong silently.
//
// The SDK's routing tier classifies a resolver failure by CAUSE: four sentinels
// mean "we declined to send, do not retry" and everything else means "we could
// not reach it, try again". A refusal outside that set is retried forever
// against a domain the operator excluded, and nothing about the message would
// say so. The shared predicate is asked rather than the set restated, because
// the set belongs to the SDK's resolver contract and can grow.
func TestPermittedEndpoints_RefusalIsFinal(t *testing.T) {
	t.Parallel()
	r := exchpolicy.NewPermittedEndpoints(mustPolicy(t, "listed.example"), &countingDelegate{})

	_, err := r.ResolveEndpoint(context.Background(), "excluded.example")
	if !foraroute.IsVerdict(err) {
		t.Errorf("err = %v is not a verdict, so the SDK reads a permanent policy "+
			"refusal as a transport failure and retries it", err)
	}
}

// TestPermittedEndpoints_PassesAPermittedDomainThrough is the other side of the
// lever: it confines rather than blocks, and it does not rewrite the answer.
func TestPermittedEndpoints_PassesAPermittedDomainThrough(t *testing.T) {
	t.Parallel()
	delegate := &countingDelegate{}
	r := exchpolicy.NewPermittedEndpoints(mustPolicy(t, "listed.example"), delegate)

	endpoint, err := r.ResolveEndpoint(context.Background(), "listed.example")
	if err != nil {
		t.Fatalf("a listed domain was refused: %v", err)
	}
	if want := "https://listed.example/api"; endpoint != want {
		t.Errorf("endpoint = %q, want %q — the wrapper must return what the delegate "+
			"answered, not a value of its own", endpoint, want)
	}
	if delegate.calls != 1 || delegate.last != "listed.example" {
		t.Errorf("the delegate saw %d calls, last for %q; want one, for the host asked about",
			delegate.calls, delegate.last)
	}
}

// TestPermittedEndpoints_NoPolicyPermitsEverything keeps the no-policy
// deployment on the delegate's own behaviour, so adding the wrapper to a
// deployment that sets no allowlist changes nothing.
func TestPermittedEndpoints_NoPolicyPermitsEverything(t *testing.T) {
	t.Parallel()
	for name, policy := range map[string]*exchpolicy.Allowlist{
		"an unset policy": nil,
		"an empty policy": mustPolicy(t, ""),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			delegate := &countingDelegate{}
			r := exchpolicy.NewPermittedEndpoints(policy, delegate)
			if _, err := r.ResolveEndpoint(context.Background(), "anything.example"); err != nil {
				t.Fatalf("%s refused a domain: %v", name, err)
			}
			if delegate.calls != 1 {
				t.Errorf("the delegate saw %d calls, want 1", delegate.calls)
			}
		})
	}
}
