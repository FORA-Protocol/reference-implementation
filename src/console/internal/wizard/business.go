// The business every domain is registered under, which the wizard's first step
// asks for while the account has none.

package wizard

import (
	"context"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
)

// RegisterPublisher registers the business name, and with it the payee every
// domain this account adds is registered under.
//
// The wizard's business step calls it, before the domain step. It has to come
// first: the discovery document the wizard hands out attests the payee id this
// call mints, and there is no document to publish without one. What it stores
// is a name and a payee, not a claim on any hostname -- a publisher with no
// domains is an ordinary state.
func (s *Service) RegisterPublisher(
	ctx context.Context, actor exchangeclient.Actor, displayName string,
) (exchangeclient.Publisher, error) {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return exchangeclient.Publisher{}, refusal.New(refusal.KindInvalid, "Please enter the name of your business.")
	}
	return s.registry.RegisterPublisher(ctx, actor, name)
}
