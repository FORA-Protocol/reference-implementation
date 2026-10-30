// A domain the wizard's first step saved: where its setup stands, read off its
// registration and its site, what the wizard reopens with, and the last step's
// check that verifies it.

package wizard

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// Resumer works on a domain already saved: what the wizard reopens with, and
// the check that finds where its setup stands and verifies it. It works through
// the wizard's own service, for its ports and its package check.
type Resumer struct {
	svc *Service
}

// NewResumer builds the work on saved domains over the wizard's service.
func NewResumer(svc *Service) *Resumer {
	return &Resumer{svc: svc}
}

// Progress is where the setup of a saved domain stands.
type Progress struct {
	Domain string
	// Verified is set when there is nothing to finish: the domain is verified.
	Verified bool
	// Step is the step the setup stands on: the licensing step while the
	// domain has no terms, the connect step after that. It is empty for a
	// verified domain.
	Step string
	// Connection is what the connect step's checks found. Its file and its
	// package are asked about only when the setup stands on that step, so it
	// is the zero value on any other.
	Connection Connection
}

// reopenStep is the step a saved domain's setup stands on, read off the
// registration alone: the terms first, because the publisher decides them
// without anybody else, and the file and the package after them. A verified
// domain stands on none.
func reopenStep(reg exchangeclient.Registration) string {
	switch {
	case reg.Active():
		return ""
	case reg.Terms == nil:
		return StepTerms
	default:
		return StepConnect
	}
}

// Check is the connect step's question about one of the person's saved
// domains: where its setup stands, and on the connect step, what its site
// answers now and -- once both its file and its package pass -- the
// Exchange's verification (checkConnection). The answer says whether the
// domain is verified after it.
//
// Nothing is kept about where the publisher stopped. The terms are on the
// Exchange, and the site is what has changed since, so those are what is read.
// A domain that is not this person's is refused before anything is asked about
// it, and a verified domain, or one without terms, is not asked about at all.
func (r *Resumer) Check(ctx context.Context, actor exchangeclient.Actor, rawDomain string) (Progress, error) {
	regs, reg, err := r.svc.savedDomain(ctx, actor, rawDomain)
	if err != nil {
		return Progress{}, err
	}
	at := Progress{Domain: reg.Domain, Verified: reg.Active(), Step: reopenStep(reg)}
	if at.Step != StepConnect {
		return at, nil
	}
	at.Connection, at.Verified, err = r.svc.checkConnection(ctx, actor, regs.PublisherID, reg)
	if err != nil {
		return Progress{}, err
	}
	return at, nil
}

// Resume is what the wizard reopens with for a saved domain: what it opens on
// for any domain, with the terms the domain was saved with, the domain's own
// saved answers, and the step it reopens on.
type Resume struct {
	Start
	Site exchangeclient.Registration
	// NetworkLabel is the name of the network the domain was saved behind.
	NetworkLabel string
	// Step is the step the wizard reopens on.
	Step string
}

// Reopen is what the wizard reopens with for one of the person's saved domains,
// built from regs, which must be what the Exchange listed for the signed-in
// person on this request. A verified domain has nothing to finish: Site.Active
// says so, and the page sends the publisher to the domain instead.
//
// The step is decided here, from the registration, so the page opens on it
// rather than asking a first question while it shows another step.
func (r *Resumer) Reopen(
	ctx context.Context, regs exchangeclient.Registrations, rawDomain string,
) (Resume, error) {
	domain, err := Domain(rawDomain)
	if err != nil {
		return Resume{}, err
	}
	reg, err := siteIn(regs, domain)
	if err != nil {
		return Resume{}, err
	}
	start, err := r.svc.Start(ctx)
	if err != nil {
		return Resume{}, err
	}
	start.Terms = controlsOrDefault(reg.Terms)
	return Resume{
		Start: start, Site: reg, NetworkLabel: networkLabel(cdnProvider(reg.CDN)), Step: reopenStep(reg),
	}, nil
}
