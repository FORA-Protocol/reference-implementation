package exchacct_test

import (
	"context"
	"testing"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/account"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/exchacct"
)

// The doubles and values every test in this package shares, so a second test
// does not grow a near-copy of one under a different name.

// payloadSentinel stands in for the business detail a real registration carries.
// It is readable so a failure message shows plainly what leaked.
const payloadSentinel = "VAT-SENTINEL-0000"

// malformedValue is the one thing that reaches structpb.NewStruct as a refusal.
//
// It is raw invalid UTF-8, NOT NaN or infinity. Those two never get this far:
// helpers.CheckRegistrationData canonicalises the payload first and answers
// "uncanonicalizable" for both, which this service reports as
// KindFieldsOutOfBounds. Invalid UTF-8 passes that check and fails at the pack,
// where structpb quotes what it was given.
//
// Nor can it arrive through the MCP surface, which is the point of driving the
// port directly: a JSON decoder turns an unpaired surrogate escape into U+FFFD,
// which is valid UTF-8. These bytes come from a caller that builds the map
// itself, which is exactly the second caller the kind's doc says can reach it.
var malformedValue = payloadSentinel + string([]byte{0xff, 0xfe})

// newService builds the service under test from cfg, filling in the two
// collaborators New requires and failing the test if it refuses.
//
// It takes the whole Config rather than the one field the first version took.
// That version could express exactly one arrangement -- the counting caller, a
// caller-supplied note log, no clock -- so every test that wanted a different
// caller or a controlled clock wrote the constructor and the same four-line
// error check itself, and seven of them did. The two axes that actually vary are
// which caller double is bound and whether a clock is set, and a Config argument
// carries both.
//
// The defaults are the doubles rather than nil because New refuses nil, and
// because a test that does not name a collaborator is saying it does not care
// what that collaborator does -- not that it wants the service built without one.
func newService(t *testing.T, cfg exchacct.Config) *exchacct.Service {
	t.Helper()
	if cfg.Caller == nil {
		cfg.Caller = &countingCaller{}
	}
	if cfg.Notes == nil {
		cfg.Notes = &recordingNotes{}
	}
	svc, err := exchacct.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

// stoppedClock makes AsOf comparable across two calls, which is the point of the
// test that uses it: with a wall clock the two answers differ by however long the
// second call took, and the field could not be compared at all.
type stoppedClock struct{ at time.Time }

func (c stoppedClock) Now() time.Time { return c.at }

// After is never called on this path. It returns a channel that never fires
// rather than nil, so a future caller blocks visibly instead of panicking.
func (stoppedClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// tickingClock moves one second every time it is read, which turns "how many
// times did this call read the clock?" into something a test can assert on. A
// stopped clock cannot: every read returns the same instant, so an extra one is
// invisible.
type tickingClock struct {
	base  time.Time
	reads int
}

func (c *tickingClock) Now() time.Time {
	c.reads++
	return c.base.Add(time.Duration(c.reads) * time.Second)
}

func (*tickingClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// countingCaller records how many times the wire was reached. It answers rather
// than fails, so a test asserting "nothing was sent" cannot pass because the send
// happened and errored.
type countingCaller struct {
	registers int
	statuses  int
}

func (c *countingCaller) Register(
	context.Context, *forav1.RegisterRequest,
) (*forav1.RegisterResponse, error) {
	c.registers++
	return &forav1.RegisterResponse{
		Ver: helpers.ProtocolVersion, Active: true, BillingRef: "ref",
	}, nil
}

func (c *countingCaller) AccountStatus(
	context.Context, *forav1.GetAccountStatusRequest,
) (*forav1.GetAccountStatusResponse, error) {
	c.statuses++
	return &forav1.GetAccountStatusResponse{
		Ver: helpers.ProtocolVersion, Active: true, BillingRef: "ref",
	}, nil
}

type recordingNotes struct {
	recorded []string
	// stamps holds the instant each note was written with, so a test can compare
	// it against the answer the same call returned.
	stamps []time.Time
}

func (n *recordingNotes) Record(_ context.Context, _, exchange string, at time.Time) error {
	n.recorded = append(n.recorded, exchange)
	n.stamps = append(n.stamps, at)
	return nil
}

func (n *recordingNotes) Forget(context.Context, string, string) error { return nil }

func (n *recordingNotes) List(context.Context, string) ([]account.ExchangeRegistration, error) {
	return nil, nil
}

// sameConfirmedAccount compares two Accounts the way this value's contract means
// them to be compared.
//
// Not ==, because Active is a *bool and struct equality compares the two
// pointers. Two calls always produce different pointers, so == could never hold
// and a test written that way would fail whatever the code did.
func sameConfirmedAccount(a, b exchacct.Account) bool {
	if (a.Active == nil) != (b.Active == nil) {
		return false
	}
	return a.Exchange == b.Exchange &&
		a.Registered == b.Registered &&
		a.IsActive() == b.IsActive() &&
		a.BillingRef == b.BillingRef &&
		a.AsOf.Equal(b.AsOf)
}

// refusingCaller is an outbound leg that fails with a chosen cause, so a test can
// hand the classifier one failure at a time.
//
// It stands in for the leg rather than for a peer. The leg is where the manifest
// is now read and where the payload is pre-checked, so the refusals a test wants
// to classify all arrive from it — including the ones no peer ever saw.
type refusingCaller struct{ err error }

func (c refusingCaller) Register(
	context.Context, *forav1.RegisterRequest,
) (*forav1.RegisterResponse, error) {
	return nil, c.err
}

func (c refusingCaller) AccountStatus(
	context.Context, *forav1.GetAccountStatusRequest,
) (*forav1.GetAccountStatusResponse, error) {
	return nil, c.err
}
