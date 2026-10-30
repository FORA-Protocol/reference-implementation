//go:build e2e

package e2econtrol

import (
	"context"
	"errors"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sor"
)

// AccountActivePath is an agent account's active flag in the Exchange's
// reference System of Record (sor.agent_accounts.active), named by the account's
// billing_ref. GET reads the stored flag and PUT replaces it; each answers with
// the flag now stored, read back from the store. An account that does not exist
// is 404.
//
// The Exchange never changes this flag: the operator changes it inside the SoR,
// out of band, which on the reference Postgres SoR is sor.PostgresAdapter's
// off-port SetActive. This route is that operator action, so the e2e seed can
// approve the credit-less buyer's account it opened held. It reads and writes the
// store directly, not through the Exchange's read-through cache, so a purchase can
// still see the old flag until its cache entry expires, exactly as after the
// operator's own update.
const AccountActivePath = "/e2e/v1/accounts/{billing_ref}/active"

// AccountSwitch is the reference SoR's out-of-band switch and its uncached read.
type AccountSwitch interface {
	SetActive(ctx context.Context, billingRef string, active bool) error
	IsActive(ctx context.Context, billingRef string) (bool, error)
}

// accountActive serves AccountActivePath over the SoR store.
func accountActive(accounts AccountSwitch) flagResource {
	return flagResource{
		pathKey: "billing_ref",
		subject: "billing_ref",
		member:  "active",
		event:   "exchange.e2e_control.account_active",
		write: func(ctx context.Context, billingRef string, active bool) error {
			return noSubjectIf(accounts.SetActive(ctx, billingRef, active))
		},
		read: func(ctx context.Context, billingRef string) (bool, error) {
			active, err := accounts.IsActive(ctx, billingRef)
			return active, noSubjectIf(err)
		},
	}
}

// noSubjectIf turns the SoR's "no such account" into errNoSubject, which the
// routes answer 404.
func noSubjectIf(err error) error {
	if errors.Is(err, sor.ErrAccountNotFound) {
		return errNoSubject
	}
	return err
}
