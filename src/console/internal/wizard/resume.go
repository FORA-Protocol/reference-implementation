// Finishing the setup of a domain that is saved and not verified: where its
// setup stands, read off the site itself, and what the wizard reopens with.

package wizard

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// Resumer reopens the wizard on a domain already saved: where its setup
// stands, and what the wizard reopens with. It works through the wizard's own
// service, whose ports and package check both answers need.
type Resumer struct {
	svc *Service
}

// NewResumer builds the reopening of saved domains over the wizard's service.
func NewResumer(svc *Service) *Resumer {
	return &Resumer{svc: svc}
}

// Progress is where the setup of a saved domain stands.
type Progress struct {
	Domain string
	// Verified is set when there is nothing to finish: the domain is verified.
	Verified bool
	// Step is the first step whose check does not pass, or the licensing step
	// when both pass: the step the wizard reopens on.
	Step string
	// Document is the Exchange's reading of the domain's discovery document.
	Document exchangeclient.PreflightResult
	// Edge is what the site answered about its package. It is asked only once
	// the document passes, so it is the zero report before that.
	Edge EdgeReport
}

// Progress works out where the setup of one of the person's saved domains
// stands, by asking the site what it answers now.
//
// Nothing is kept about where the publisher stopped. Their site is what has
// changed since, so it is what is asked. The document comes first, as in the
// wizard; the package is asked about only once the document passes, because
// the package check asks for the document too and would fail on it for the
// same reason. A domain that is not this person's is refused before anything
// is asked about it.
func (r *Resumer) Progress(ctx context.Context, actor exchangeclient.Actor, rawDomain string) (Progress, error) {
	domain, err := Domain(rawDomain)
	if err != nil {
		return Progress{}, err
	}
	regs, err := r.svc.registrations(ctx, actor)
	if err != nil {
		return Progress{}, err
	}
	reg, ok := siteNamed(regs, domain)
	if !ok {
		return Progress{}, notYours()
	}
	out := Progress{Domain: reg.Domain}
	if reg.Active() {
		out.Verified = true
		return out, nil
	}
	out.Document, err = r.svc.registry.PreflightDomain(ctx, actor, regs.PublisherID, reg.Domain)
	if err != nil {
		return Progress{}, err
	}
	if !out.Document.Passed {
		out.Step = StepDocument
		return out, nil
	}
	out.Edge = r.svc.edgeReport(ctx, reg.Domain, reg.ContentPathScope)
	if !out.Edge.CanAdvance() {
		out.Step = StepPackage
		return out, nil
	}
	out.Step = StepTerms
	return out, nil
}

// Resume is what the wizard reopens with for a saved domain: what it opens on
// for any domain, with the terms the domain was saved with, and the domain's
// own saved answers.
type Resume struct {
	Start
	Site exchangeclient.Registration
	// NetworkLabel is the name of the network the domain was saved behind.
	NetworkLabel string
}

// Reopen reads what the wizard reopens with for one of the person's saved
// domains. A verified domain has nothing to finish: Site.Active says so, and
// the page sends the publisher to the domain instead.
func (r *Resumer) Reopen(ctx context.Context, actor exchangeclient.Actor, rawDomain string) (Resume, error) {
	domain, err := Domain(rawDomain)
	if err != nil {
		return Resume{}, err
	}
	start, regs, err := r.svc.startWith(ctx, actor)
	if err != nil {
		return Resume{}, err
	}
	reg, ok := siteNamed(regs, domain)
	if !ok {
		return Resume{}, notYours()
	}
	start.Terms = controlsOrDefault(reg.Terms)
	return Resume{Start: start, Site: reg, NetworkLabel: networkLabel(cdnProvider(reg.CDN))}, nil
}
