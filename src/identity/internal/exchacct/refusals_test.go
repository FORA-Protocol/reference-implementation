package exchacct_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/account"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/exchacct"
)

// What this service refuses about the exchange ARGUMENT, before it reads,
// rewrites or sends anything.

// TestBothLegsRefuseANonBareDomainBeforeAnythingHappens is the structural half of
// the wire-shape rule, on both account calls.
//
// Register reached this rule through the requirements reader, which runs it
// before it dials. Status ran no shape check at all: it canonicalised whatever it
// was given and handed it to the wire, with only the tool layer's copy in front
// of it. A guarantee one caller holds by convention is not a guarantee, and this
// package's own doc names its consumers as "the MCP adapter today, and whatever
// asks next".
//
// The shared table is what makes this worth driving rather than asserting one
// bad string. Four of its entries separate the rule that IS applied from the
// weaker question of whether a value merely parses as a host — swap in the weak
// rule and those four start passing while every other entry still fails.
//
// Nothing sent and nothing noted is asserted alongside the refusal, because a
// refusal that happened after the request would be the defect wearing the right
// error.
func TestBothLegsRefuseANonBareDomainBeforeAnythingHappens(t *testing.T) {
	t.Parallel()
	for _, tc := range testutil.NonBareDomains {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			bad := tc.Of("exchange.example")
			legs := map[string]func(*exchacct.Service) error{
				"register": func(s *exchacct.Service) error {
					_, err := s.Register(context.Background(), "agent", bad, nil)
					return err
				},
				"status": func(s *exchacct.Service) error {
					_, err := s.Status(context.Background(), "agent", bad)
					return err
				},
			}
			for name, call := range legs {
				notes, peer := &recordingNotes{}, &countingCaller{}
				svc := newService(t, exchacct.Config{Caller: peer, Notes: notes})
				err := call(svc)
				if err == nil {
					t.Fatalf("%s accepted %q", name, bad)
				}
				if kind, ok := exchacct.KindOf(err); !ok || kind != exchacct.KindExchangeShape {
					t.Errorf("%s: kind = %v, want %v — a refused argument must not read "+
						"as the Exchange failing to answer", name, kind, exchacct.KindExchangeShape)
				}
				if peer.registers != 0 || peer.statuses != 0 {
					t.Errorf("%s: reached the wire with %q", name, bad)
				}
				if len(notes.recorded) != 0 {
					t.Errorf("%s: wrote a note for %q", name, bad)
				}
			}
		})
	}
}

// TestCheckExchange_RefusesTheValueAsWritten pins the ordering: the rule runs
// before canonicalisation, so the caller is refused the value it sent rather
// than one this service rewrote.
//
// Canonicalising lowercases, trims and folds a written-out :443. Check after
// that rewrite and the error quotes a value the caller never wrote, which is the
// "silently reinterpreted" outcome the tool layer's own rule argues against —
// these calls open accounts and move money.
func TestCheckExchange_RefusesTheValueAsWritten(t *testing.T) {
	t.Parallel()
	const bad = "  HTTPS://Exchange.Example:443/register  "
	svc := newService(t, exchacct.Config{})

	_, err := svc.Register(context.Background(), "agent", bad, nil)
	if err == nil {
		t.Fatal("Register accepted a URL")
	}
	if got := exchacct.ExchangeOf(err); got != bad {
		t.Errorf("the refusal names %q, want the value as written, %q — a caller told "+
			"its input was wrong must be shown the input it sent", got, bad)
	}
}

// TestBothLegsClassifyEveryFailureTheOutboundLegCanReport is the table the
// classifier exists for.
//
// The outbound leg reports in two vocabularies. It carries this service's own
// sentinels through from the requirements read, and it wraps everything in the
// SDK's typed failure. Neither alone is enough: the SDK's class answers "was it
// sent, and did a peer reply", which cannot separate a domain the deployment
// excludes from an Exchange that is down, and those have different remedies
// belonging to different people. Every one of these used to arrive as the
// outbound kind, which told an operator only that something failed.
//
// The table pins the MAPPING and says nothing about who produces each sentinel.
// Proving that each one is actually produced belongs to the tests over the
// requirements reader, which drive a real origin.
func TestBothLegsClassifyEveryFailureTheOutboundLegCanReport(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		refusal error
		want    exchacct.Kind
		// registerOnly marks a refusal only the register leg can receive. Status
		// sends no payload, so nothing can pre-check one for it.
		registerOnly bool
	}{
		"a refused argument": {
			refusal: readerRefusal(connect.CallNotSent,
				fmt.Errorf("%w: %w", account.ErrNotBareDomain, helpers.ErrInvalidHost)),
			want: exchacct.KindExchangeShape,
		},
		// The requirements reader's spelling, which the register leg meets.
		"a policy refusal on the requirements read": {
			refusal: readerRefusal(connect.CallNotSent,
				fmt.Errorf("%w: %w", account.ErrNotPermitted, resolvers.ErrExchangeNotPermitted)),
			want: exchacct.KindNotPermitted,
		},
		// The routing tier's spelling, which is what the status leg meets and what
		// used to arrive carrying nothing this classifier could match. Both rows
		// exist because one operator decision reaches this function through two
		// adapters, and the kind must not depend on which one ran.
		"a policy refusal on the routing leg": {
			refusal: readerRefusal(connect.CallNotSent,
				fmt.Errorf("%w: %w: host %q not allowed",
					account.ErrNotPermitted, resolvers.ErrNoEndpoint, "exchange.example")),
			want: exchacct.KindNotPermitted,
		},
		"a manifest that could not be read": {
			refusal: readerRefusal(connect.CallUnreachable,
				fmt.Errorf("%w: no such host", account.ErrRequirementsUnread)),
			want: exchacct.KindRequirements,
		},
		"a manifest naming another role": {
			refusal: readerRefusal(connect.CallNotSent,
				fmt.Errorf("%w: %w", account.ErrRequirementsUnread, resolvers.ErrManifestNotExchange)),
			want: exchacct.KindRequirements,
		},
		// One verdict, once in each leg's vocabulary. The register leg reads
		// requirements first, so the refusal comes back from this repository's
		// reader. The status leg reads none, so the same document is refused
		// inside the SDK's resolver. Both rows are here because one verdict
		// reaches this function through two adapters, and the kind must not
		// depend on which one ran.
		//
		// The remedy separates it from the two rows above: a newer build of this
		// service, or a word with the Exchange. A retry is not one of them, and
		// the first row is where that could quietly stop holding. A refused
		// version is a failed READ as well — nothing was pre-checked and nothing
		// was sent — so that row carries the unread sentinel too, and it fails if
		// the arm that names the version stops running first.
		"a manifest version refused on the requirements read": {
			refusal: readerRefusal(connect.CallNotSent, fmt.Errorf("%w: %w: %w",
				account.ErrRequirementsUnread, account.ErrManifestVersionRefused,
				resolvers.ErrManifestUnusable)),
			want: exchacct.KindManifestVersionRefused,
		},
		// Both legs report this as not-sent, and the two rows reach that answer
		// carrying different sentinels. The routing tier counts a refused version
		// among its own verdicts. The requirements tier does not, so the reader
		// attaches the unusable-manifest sentinel that tier DOES read — the two
		// classes agree only because the reader makes them agree.
		//
		// The kind is still taken off the sentinel and never off the class. The
		// two manifest-read rows above are what prove that: one arrives as
		// unreachable and one as not-sent, and both answer KindRequirements.
		"a manifest version refused on the routing leg": {
			refusal: readerRefusal(connect.CallNotSent,
				fmt.Errorf("%w: ver %q", resolvers.ErrManifestVersionRefused, "2.0")),
			want: exchacct.KindManifestVersionRefused,
		},
		"the payload refused before it was sent": {
			refusal:      precheckRefusalError(),
			want:         exchacct.KindFieldsRefused,
			registerOnly: true,
		},
		"a peer that refused the request": {
			refusal: &connect.CallError{Kind: connect.CallRefused, Op: "register", Reason: "invalid_argument"},
			want:    exchacct.KindOutbound,
		},
		"a peer that never answered": {
			refusal: &connect.CallError{Kind: connect.CallUnreachable, Op: "register"},
			want:    exchacct.KindOutbound,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for leg, call := range accountLegs(tc.registerOnly) {
				t.Run(leg, func(t *testing.T) {
					t.Parallel()
					svc := newService(t, exchacct.Config{Caller: refusingCaller{err: tc.refusal}})
					err := call(svc)
					if err == nil {
						t.Fatal("the call reported success on a leg that failed")
					}
					if kind, ok := exchacct.KindOf(err); !ok || kind != tc.want {
						t.Errorf("kind = %v, want %v", kind, tc.want)
					}
					// The cause survives on every arm. The layer that renders this
					// for an agent reads the protocol's typed reason off it, so an
					// arm that dropped it would leave that layer with a bare kind.
					if exchacct.CauseOf(err) == nil {
						t.Error("the failure carries no cause, so the typed reason underneath is unreachable")
					}
				})
			}
		})
	}
}

// TestRegister_ReadsTheRefusedMembersOffThePrecheck pins the half the kind alone
// does not carry.
//
// A schema refusal is only useful if it says WHICH members were wrong. The
// outbound leg computes that list before anything is signed and attaches it as a
// typed detail, and the tool layer renders it with a pointer to where the
// Exchange publishes the shape. Losing the list here would leave an agent told
// only that its payload was wrong.
func TestRegister_ReadsTheRefusedMembersOffThePrecheck(t *testing.T) {
	t.Parallel()
	svc := newService(t, exchacct.Config{Caller: refusingCaller{err: precheckRefusalError()}})

	_, err := svc.Register(context.Background(), "agent", "exchange.example", nil)
	fields := exchacct.FieldsOf(err)
	if len(fields) != 1 {
		t.Fatalf("got %d refused members, want 1: %v", len(fields), fields)
	}
	if got := fields[0].GetPath(); got != "/vat_id" {
		t.Errorf("refused member = %q, want the one the pre-check named", got)
	}
}

// TestRegister_APeersOwnSchemaRefusalIsNotReadAsALocalOne is the negative that
// makes the class check in the classifier load-bearing.
//
// An Exchange refusing the same payload attaches the same shape of detail. It is
// a different outcome — the request was signed and sent, and the peer decided —
// so it belongs to the outbound arm, where the tool layer renders the peer's own
// refusal alongside its reason. Reading the detail without asking the class
// would report a peer's refusal as this service's own pre-check.
func TestRegister_APeersOwnSchemaRefusalIsNotReadAsALocalOne(t *testing.T) {
	t.Parallel()
	peerRefusal := &connect.CallError{
		Kind:   connect.CallRefused,
		Op:     "register",
		Reason: "invalid_argument",
		Detail: helpers.RegistrationFailureDetail(
			"fora.v1.ExchangeService", "registration_data is not acceptable",
			forav1.RegistrationFailureReason_REGISTRATION_FAILURE_REASON_INVALID_REGISTRATION_DATA,
			&forav1.RegistrationFieldError{Path: "/vat_id", Error: "must match pattern"},
		),
	}
	svc := newService(t, exchacct.Config{Caller: refusingCaller{err: peerRefusal}})

	_, err := svc.Register(context.Background(), "agent", "exchange.example", nil)
	if kind, ok := exchacct.KindOf(err); !ok || kind != exchacct.KindOutbound {
		t.Errorf("kind = %v, want the outbound kind so the peer's own refusal is rendered as one", kind)
	}
}

// accountLegs are the two calls that route through classify, keyed by the name
// a subtest reports under.
//
// A table row drives BOTH unless it names a refusal only one leg can receive.
// That is the property the divergence this exists to catch was hiding in: the
// register leg reads an Exchange's requirements before it routes and the status
// leg routes straight away, so a refusal raised in the routing tier reached the
// classifier wearing a different vocabulary depending on which tool asked. The
// table drove register only, so it could not see it.
func accountLegs(registerOnly bool) map[string]func(*exchacct.Service) error {
	legs := map[string]func(*exchacct.Service) error{
		"register": func(svc *exchacct.Service) error {
			_, err := svc.Register(context.Background(), "agent", "exchange.example", nil)
			return err
		},
	}
	if registerOnly {
		return legs
	}
	legs["status"] = func(svc *exchacct.Service) error {
		_, err := svc.Status(context.Background(), "agent", "exchange.example")
		return err
	}
	return legs
}

// readerRefusal is a requirements-read failure in the shape the outbound leg
// hands it over: this repository's sentinel underneath, the SDK's typed failure
// around it.
//
// The leg never returns a bare sentinel. The SDK's registration verb classifies
// every reader refusal before returning it -- the verdict sentinels it names
// come back as CallNotSent, everything else as CallUnreachable -- so a row
// carrying an unwrapped error asserts a shape production cannot produce.
//
// The kinds come out the same either way, and that is the property worth
// pinning rather than assuming: classify reads our sentinels with errors.Is, and
// the SDK's Unwrap is what lets that reach through its own type. Were a later
// revision to stop preserving the cause, every row built here would collapse to
// the outbound kind -- an operator could no longer tell a domain the deployment
// excludes from an Exchange that is down, which is the distinction classify's
// doc says it exists for. Written as bare sentinels, this table would not have
// noticed.
func readerRefusal(kind connect.CallErrorKind, cause error) error {
	return &connect.CallError{Kind: kind, Op: "register", Err: cause}
}

// precheckRefusalError is the refusal the outbound leg computes when the payload
// does not match the schema the Exchange publishes: nothing was sent, and the
// offending members travel with it.
//
// Built in TWO layers because that is what production hands over. The SDK's own
// refusal is underneath, typed detail and all, and the leg wraps it in this
// repository's vocabulary once it has read the class and seen that nothing left
// the process. A row staging only the inner half would assert a shape the
// service no longer receives -- and staging only the outer half would stop
// proving that the typed detail survives the wrap, which is what the tool layer
// renders the member names from.
func precheckRefusalError() error {
	sdk := &connect.CallError{
		Kind: connect.CallMalformed,
		Op:   "register",
		Err:  errors.New("registration_data does not match the schema exchange.example publishes"),
		Detail: helpers.RegistrationFailureDetail(
			"fora.v1.Client", "registration_data does not match the published data_schema",
			forav1.RegistrationFailureReason_REGISTRATION_FAILURE_REASON_INVALID_REGISTRATION_DATA,
			&forav1.RegistrationFieldError{Path: "/vat_id", Error: "must match pattern"},
		),
	}
	return &account.FieldsRefusedError{
		Fields: sdk.Detail.GetRegistrationFailure().GetFieldErrors(),
		Err:    fmt.Errorf("%w exchange.example: %w", account.ErrFieldsRefused, sdk),
	}
}
