package service

import (
	"context"
	"errors"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// PublisherGate answers the two questions every console call asks before it
// touches a publisher's data: which seat this Exchange sells as, and whether
// the person the console asserts holds a grant for the publisher they named.
//
// It is one type, shared by the registration plane and the publisher reads, so
// the rule that decides who may see a publisher is written once. A second copy
// in the read service could drift from this one, and the drift would show up as
// one publisher reading another's revenue.
type PublisherGate struct {
	grants         repo.PublisherGrantReadRepo
	seats          repo.TenantReadRepo
	exchangeDomain string
}

// NewPublisherGate builds the gate over the grant and seat reads. exchangeDomain
// is the hostname that identifies this deployment's single seat.
func NewPublisherGate(
	grants repo.PublisherGrantReadRepo, seats repo.TenantReadRepo, exchangeDomain string,
) *PublisherGate {
	return &PublisherGate{grants: grants, seats: seats, exchangeDomain: exchangeDomain}
}

// RequireGrant is the authorization gate every publisher-scoped operation
// passes, and it returns the seat the operation runs on. A caller with no grant
// for the requested publisher is answered not-found rather than
// permission-denied: the console is reachable by anyone with a login, so telling
// them a publisher id exists would let them enumerate the deployment's payees
// one guess at a time.
func (g *PublisherGate) RequireGrant(ctx context.Context, a Actor, publisherID string) (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(publisherID) == "" {
		return "", exchange.Newf(exchange.KindInvalidRequest, "publisher_id is required")
	}
	seat, err := g.SeatID(ctx)
	if err != nil {
		return "", err
	}
	ok, err := g.grants.Has(ctx, seat, publisherID, a.OIDCIssuer, a.OIDCSubject)
	if err != nil {
		return "", exchange.Wrap(exchange.KindInternal, err, "has publisher grant")
	}
	if !ok {
		return "", exchange.Newf(exchange.KindNotFound, "publisher %q not found", publisherID)
	}
	return seat, nil
}

// ActingPublisher returns the publisher this person already acts for on this
// seat, or "" when they hold no grant yet.
//
// More than one grant cannot exist: the unique index
// publisher_grants_one_actor_idx allows one grant per person per seat, so the
// list holds at most one id.
func (g *PublisherGate) ActingPublisher(ctx context.Context, seat string, a Actor) (string, error) {
	ids, err := g.grants.PublisherIDsForActor(ctx, seat, a.OIDCIssuer, a.OIDCSubject)
	if err != nil {
		return "", exchange.Wrap(exchange.KindInternal, err, "list publisher grants for actor")
	}
	if len(ids) == 0 {
		return "", nil
	}
	return ids[0], nil
}

// SeatID resolves this deployment's single seat: the fora.tenants row whose
// domain is the Exchange's own. Every grant, listing and publisher read is
// scoped to it.
//
// It is read per request rather than cached at boot for the reason the boot
// probe only warns: a deployment may seed its seat after the process starts, so
// a value captured at boot would be empty for the lifetime of a process that
// started first. One indexed lookup is not what makes a console call slow.
//
// A missing seat is an operator-fixable deployment state and says so, in a
// message written for the operator's log. It is not a caller fault: no console
// call could succeed under it, and telling a publisher their form was wrong
// would send them to fix the wrong thing. So it is KindUnavailable, which the
// transport answers as a server-side outage and logs at error level, and its
// text never reaches the publisher.
func (g *PublisherGate) SeatID(ctx context.Context) (string, error) {
	seat, err := g.seats.ByDomain(ctx, g.exchangeDomain)
	if err != nil {
		if errors.Is(err, repo.ErrTenantNotFound) {
			return "", exchange.Newf(exchange.KindUnavailable,
				"this Exchange has no seat registered for its own domain %q; "+
					"seed it before accepting console calls", g.exchangeDomain)
		}
		return "", exchange.Wrap(exchange.KindInternal, err, "resolve exchange seat")
	}
	return seat.ID, nil
}
