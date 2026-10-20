package forasdk_test

import (
	"context"
	"errors"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
	"google.golang.org/protobuf/types/known/structpb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/account"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/forasdk"
)

// TestRegister_StatesItsOwnPrecheckRefusalInThePortsVocabulary pins the
// translation, which nothing else in the tree can see.
//
// The account service classifies a refused payload on this repository's own
// sentinel, and this leg is what puts it there. Everything above works from the
// translated shape: the service's table stages it by hand, and the tool layer
// renders the same member names for a LOCAL refusal and a REMOTE one, so a leg
// that stopped translating would leave both of those green while the service
// reclassified every local refusal as "a peer refused this".
//
// That is not only a wrong label. A locally refused payload is held back from
// the operator log, because the members it names are the operator's own business
// details; a peer's refusal is logged. Losing the distinction moves the payload
// into a log line, which is the one thing this service promises never to do.
//
// No peer and no origin: the pre-check runs before anything is signed or sent,
// so the requirements reader is the only collaborator that has to answer.
func TestRegister_StatesItsOwnPrecheckRefusalInThePortsVocabulary(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		requirements connect.RegistrationRequirementsReader
		// wantRefused is whether the leg must state this as the port's own
		// refused-payload error.
		wantRefused bool
		// why says what breaks when it does not.
		why string
	}{
		"a payload the published schema rejects": {
			requirements: schemaRequirements(t),
			wantRefused:  true,
			why: "the service would report a locally refused payload as a peer's refusal, " +
				"which names the wrong party and puts the submitted members in a log line",
		},
		"a manifest that could not be read": {
			requirements: failingRequirements{},
			wantRefused:  false,
			why: "nothing was pre-checked, so calling this a refused payload would name " +
				"members that were never examined",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := completeConfig(t)
			cfg.Requirements = tc.requirements
			client, err := forasdk.New(cfg)
			if err != nil {
				t.Fatalf("forasdk.New: %v", err)
			}

			_, err = client.Register(context.Background(), &forav1.RegisterRequest{
				Ver:              helpers.ProtocolVersion,
				Exchange:         "exchange.example",
				RegistrationData: mustStruct(t, map[string]any{"vat_id": "DE123456789"}),
			})
			if err == nil {
				t.Fatal("the registration succeeded, so there is no refusal to state")
			}

			var refused *account.FieldsRefusedError
			switch got := errors.As(err, &refused); {
			case got != tc.wantRefused:
				t.Fatalf("err = %v states the port's refused-payload error = %v, want %v -- %s",
					err, got, tc.wantRefused, tc.why)
			case !tc.wantRefused:
				return
			}
			if !errors.Is(err, account.ErrFieldsRefused) {
				t.Errorf("err = %v does not match the sentinel through Unwrap, so the "+
					"service cannot classify it with errors.Is", err)
			}
			// The members survive the wrap. They are what the tool layer renders,
			// and an agent that is told only "invalid" cannot fix it in one retry.
			if len(refused.Fields) == 0 {
				t.Error("the refusal names no members, so the translation dropped the typed " +
					"detail the tool layer renders from")
			}
		})
	}
}

// schemaRequirements answers with a compiled schema the test payload fails, so
// the SDK's own pre-check refuses before anything is signed.
func schemaRequirements(t *testing.T) connect.RegistrationRequirementsReader {
	t.Helper()
	schema, verdict := helpers.CompileRegistrationSchema([]byte(
		`{"type":"object","required":["company_name"],` +
			`"properties":{"company_name":{"type":"string"}}}`,
	))
	if verdict != helpers.SchemaAccepted {
		t.Fatalf("the fixture schema was refused as %s, so the pre-check would be skipped", verdict)
	}
	return fixedRequirements{reqs: resolvers.RegistrationRequirements{
		Schema:  schema,
		Verdict: helpers.SchemaAccepted,
	}}
}

type fixedRequirements struct {
	reqs resolvers.RegistrationRequirements
}

func (f fixedRequirements) ResolveRegistrationRequirements(
	context.Context, string,
) (resolvers.RegistrationRequirements, error) {
	return f.reqs, nil
}

// failingRequirements stands for a manifest that could not be read at all.
type failingRequirements struct{}

func (failingRequirements) ResolveRegistrationRequirements(
	context.Context, string,
) (resolvers.RegistrationRequirements, error) {
	return resolvers.RegistrationRequirements{}, errors.New("forasdk_test: no manifest served")
}

func mustStruct(t *testing.T, fields map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	return s
}
