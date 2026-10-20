package forasdk

import (
	"context"
	"errors"
	"fmt"

	connectrpc "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/connect"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/account"
)

// Register opens the calling agent's account at the Exchange the request names,
// carrying the business detail the caller supplied as the registration data.
//
// The destination is read off the request rather than taken as an argument, the
// same way the usage report reads it off the report. That is not a style choice:
// a parameter is something a configured origin could arrive as, and an account is
// per-Exchange, so a configured origin would confine every agent this service
// signs for to one Exchange. Reading it off the message the signature covers also
// means the value deciding where the call goes is the value the receiving
// Exchange checks it against.
//
// Leaving TermsDigest unset is what asks the SDK to read the Exchange's own
// manifest, echo the terms revision it publishes, and pre-check the payload
// against the schema it publishes before anything is signed. A caller that sets
// the field is managing its own requirements and gets neither.
func (c *Client) Register(
	ctx context.Context, req *forav1.RegisterRequest,
) (*forav1.RegisterResponse, error) {
	opts, err := c.callOptions(ctx, "register")
	if err != nil {
		return nil, err
	}
	resp, err := connect.NewClient(homeExchangePlaceholder, opts...).Register(ctx, req)
	if err != nil {
		return nil, fieldsRefused(req.GetExchange(), err)
	}
	return resp, nil
}

// AccountStatus reports the calling agent's account state at the Exchange the
// request names.
//
// GetAccountStatusRequest carries no field identifying the caller — the Exchange
// resolves the account from the verified signature — so `exchange` is the only
// thing saying which account is being asked about.
//
// The method is named for the port it satisfies rather than for the SDK verb it
// calls. The account service depends on AccountStatus, and renaming that port to
// follow this leg would push a wire-level name up into the service that has no
// use for one.
func (c *Client) AccountStatus(
	ctx context.Context, req *forav1.GetAccountStatusRequest,
) (*forav1.GetAccountStatusResponse, error) {
	opts, err := c.callOptions(ctx, "get account status")
	if err != nil {
		return nil, err
	}
	resp, err := connect.NewClient(homeExchangePlaceholder, opts...).GetAccountStatus(ctx, req)
	if err != nil {
		return nil, noAccount(req.GetExchange(), err)
	}
	return resp, nil
}

// noAccount states the Exchange's "this agent holds no account here" answer as
// the sentinel the account service branches on, so a normal outcome does not
// reach it as a failure.
//
// It reads the Connect code off the chain rather than the SDK's own class,
// because the class says only that a peer refused. The SDK keeps the Connect
// error underneath its typed failure, so errors.As still reaches the code after
// the classification.
//
// This deployment's own Exchange answers NotFound here, which the protocol does
// not require: the contract's no-account answer is an ordinary response carrying
// an empty account handle. Both are the same answer and the account service
// treats them alike; this is the half that recognises a peer answering the way
// this deployment does.
func noAccount(exchange string, err error) error {
	var connErr *connectrpc.Error
	if errors.As(err, &connErr) && connErr.Code() == connectrpc.CodeNotFound {
		return fmt.Errorf("%w %s: %w", account.ErrNoAccount, exchange, err)
	}
	return err
}

// fieldsRefused states the SDK's own pre-check refusal as the sentinel the
// account service branches on: the payload did not match the schema this
// Exchange publishes, and nothing was signed or sent.
//
// It reads the SDK's transport class here, in the leg that speaks that
// vocabulary, for the reason noAccount reads a Connect code here. The class is
// load-bearing rather than a redundant guard: an Exchange refusing the same
// payload answers with the same shape of typed detail, and only the class says
// whether a peer replied. Asking that question one layer up would put the SDK's
// wire vocabulary into the account port's contract, where every other promise is
// stated in this repository's own.
//
// The refusal is WRAPPED rather than replaced, so the typed detail carrying the
// offending members stays reachable through the chain — the service renders those
// members by name, and the protocol's error contract is explicit that a caller
// branches on the typed reason rather than on prose.
func fieldsRefused(exchange string, err error) error {
	var callErr *connect.CallError
	if !errors.As(err, &callErr) || callErr.Kind != connect.CallMalformed {
		return err
	}
	failure := callErr.Detail.GetRegistrationFailure()
	if failure == nil {
		return err
	}
	return &account.FieldsRefusedError{
		Fields: failure.GetFieldErrors(),
		Err:    fmt.Errorf("%w %s: %w", account.ErrFieldsRefused, exchange, err),
	}
}
