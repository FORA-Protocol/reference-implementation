// The wizard's steps: the business name, the domain and what serves it, the
// files the publisher deploys, and the checks that gate each step. The
// licensing step is in page.go, the last button in finish.go, and reopening a
// saved domain at the step its setup stands on in resume.go.

package wizard

import (
	"context"
	"fmt"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/setupsteps"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
)

// The wizard's steps, by the key the browser restores them by and Progress
// answers in.
const (
	StepName     = "name"
	StepDomain   = "domain"
	StepDocument = "document"
	StepPackage  = "package"
	StepTerms    = "terms"
)

// RegisterPublisher registers the business name, which is the one thing the
// wizard stores before its last step.
//
// It has to be stored, because the discovery document the next steps hand out
// attests the payee id this call mints, and there is no document to publish
// without one. What that stores is a name and a payee, not a claim on any
// hostname -- a publisher with no domains is an ordinary state, the one that
// skipping the domain step ends registration in -- and the domain, which is
// what one party can take from another, is still written only on the last
// step.
func (s *Service) RegisterPublisher(
	ctx context.Context, actor exchangeclient.Actor, displayName string,
) (exchangeclient.Publisher, error) {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return exchangeclient.Publisher{}, refusal.New(refusal.KindInvalid, "Please enter the name of your business.")
	}
	return s.registry.RegisterPublisher(ctx, actor, name)
}

// Lookup is everything the console can find out about a domain before the
// publisher has done any work on it.
//
// The network and the registration are answered together on purpose. They are
// two questions -- who serves this, and could I register it -- but the
// publisher asks both by typing one address, and answering them in one call
// spares the wizard a second round trip while they are still reading the first
// answer. Manifest is what makes the resume work: a publisher whose document is
// already published arrives at the file step with it already passing.
type Lookup struct {
	Domain   string
	Provider cdn.Provider
	// ProviderLabel is the network's own name, so there is one place where
	// "cloudfront" becomes "AWS CloudFront".
	ProviderLabel string
	Supported     bool
	Taken         bool
	// Yours is set when the domain is already one of this person's, and
	// Verified says whether it is verified. Nothing else is looked up then:
	// the publisher finishes or opens that domain rather than adding it again.
	Yours    bool
	Verified bool
	// LimitReached is set when the publisher already holds as many domains
	// saved and not verified as one may, and this is not one of them. Message
	// then says so, and nothing else about the domain is looked up.
	LimitReached bool
	Message      string
	Manifest     exchangeclient.PreflightResult
}

// Lookup works out which network serves a domain and whether it could be
// registered.
//
// The network is not asked of the publisher. Detection reads DNS and one
// response header, and a publisher who does not know what a CNAME is still
// knows their address -- so the address is what they give, and an answer we
// cannot work out ends the wizard rather than turning into a question they
// would have to guess at.
func (s *Service) Lookup(ctx context.Context, actor exchangeclient.Actor, rawDomain string) (Lookup, error) {
	// Checked before anything is looked up. The next steps make a DNS query
	// and a request to whatever was typed, and a publisher who pasted a URL or
	// mistyped a name should read what is wrong with it rather than "we could
	// not work out which network serves that", which is true and would send
	// them to support over a typo.
	domain, err := Domain(rawDomain)
	if err != nil {
		return Lookup{}, err
	}
	regs, err := s.registrations(ctx, actor)
	if err != nil {
		return Lookup{}, err
	}
	out := Lookup{Domain: domain}
	if held, ok := siteNamed(regs, domain); ok {
		// Answered before anything is asked about the domain: it is saved
		// already, and finishing its setup starts from what it was saved
		// with.
		out.Domain, out.Yours, out.Verified = held.Domain, true, held.Active()
		return out, nil
	}
	if atUnverifiedLimit(regs) {
		// Said before anything is asked about the domain. The Exchange refuses
		// the save at the last button anyway; this is what tells the publisher
		// before they publish a file and install a package for nothing.
		out.LimitReached, out.Message = true, unverifiedLimitText
		return out, nil
	}
	publisherID := regs.PublisherID
	out.Provider, out.Supported, out.Message = s.detectNetwork(ctx, domain)
	out.ProviderLabel = networkLabel(out.Provider)
	if !out.Supported {
		// The Exchange is not asked about a domain the publisher cannot set up
		// here. It would fetch their document to answer, which is a request to
		// somebody else's server made for a wizard that has already stopped.
		return out, nil
	}
	res, err := s.registry.PreflightDomain(ctx, actor, publisherID, domain)
	if err != nil {
		return Lookup{}, err
	}
	// Domain taken is the one reason the wizard reads rather than just shows,
	// because it is the only one the publisher cannot repair by editing a file.
	out.Taken = containsFold(res.Reasons, consoleapi.ReasonDomainTaken)
	out.Manifest = res
	return out, nil
}

// CheckManifest asks the Exchange to read the domain's discovery document.
//
// The Exchange reads it, not the console. It is the party that reads it again
// before it verifies the domain, so it has to be the party that answers here: a
// second reader in the console would be a second opinion about the same
// document, and the two would drift.
//
// Nothing is written: this call neither saves the domain nor verifies it.
func (s *Service) CheckManifest(
	ctx context.Context, actor exchangeclient.Actor, rawDomain string,
) (exchangeclient.PreflightResult, error) {
	domain, err := Domain(rawDomain)
	if err != nil {
		return exchangeclient.PreflightResult{}, err
	}
	publisherID, err := s.publisherID(ctx, actor)
	if err != nil {
		return exchangeclient.PreflightResult{}, err
	}
	return s.registry.PreflightDomain(ctx, actor, publisherID, domain)
}

// CheckEdge asks the publisher's own site what it now answers.
//
// Three requests to public addresses on their site: what an agent without a
// licence gets, what an ordinary visitor gets, and whether the discovery
// document is still served now that a package sits in front of it. All three
// used to be curl commands in the written instructions, which was asking a
// publisher to do our work and read a status code.
//
// The document question belongs here rather than at the previous step. The
// package is what could break it, so asking before it is deployed answers a
// question nobody has yet.
func (s *Service) CheckEdge(ctx context.Context, rawDomain, paths string) (EdgeReport, error) {
	domain, err := Domain(rawDomain)
	if err != nil {
		return EdgeReport{}, err
	}
	return s.edgeReport(ctx, domain, splitPaths(paths)), nil
}

// edgeReport asks the site about its package and says what the answer means.
func (s *Service) edgeReport(ctx context.Context, domain string, paths []string) EdgeReport {
	report := s.inspectEdge(ctx, domain, paths)
	report.Message = edgeMessage(report.State, domain)
	return report
}

// Draft is a domain's answers as the wizard holds them before registration.
type Draft struct {
	Domain string
	CDN    string
	// Paths is the comma-separated field the publisher filled in.
	Paths string
}

// Files is a domain's setup files, and what to do with the edge configuration
// on its network.
type Files struct {
	Site      setup.Site
	Bundle    setup.Bundle
	EdgeSteps []setupsteps.Step
}

// Files builds the files for a domain that is not saved yet.
//
// Everything they are built from is in the answers plus the payee id, so the
// files exist before the domain is saved: the wizard hands the publisher the
// discovery document to publish on the step before they press the last button.
//
// It rebuilds the bundle rather than reading a stored one: the answers are the
// source, and the same answers produce the same file a week later.
func (s *Service) Files(ctx context.Context, actor exchangeclient.Actor, d Draft) (Files, error) {
	site, err := s.draftSite(ctx, actor, d)
	if err != nil {
		return Files{}, err
	}
	bundle, err := build(ctx, s.bundles, site, "Your files could not be produced. Nothing was changed.")
	if err != nil {
		return Files{}, err
	}
	return Files{Site: site, Bundle: bundle, EdgeSteps: edgeSteps(site)}, nil
}

// build produces a site's bundle, or the refusal a publisher reads when it
// cannot be produced. The refusal's sentence is the caller's, because what is
// unaffected differs: nothing was changed mid-wizard, and a registration is
// untouched on its own page.
func build(ctx context.Context, b Bundler, site setup.Site, message string) (setup.Bundle, error) {
	bundle, err := b.Build(ctx, site)
	if err != nil {
		reqctx.FromContext(ctx).ErrorContext(ctx, "console.bundle_failed",
			"domain", site.Domain, "err", err)
		return setup.Bundle{}, refusal.Wrap(refusal.KindBuildFailed, message, err)
	}
	return bundle, nil
}

// draftSite assembles what the builders take from the wizard's answers.
//
// The payee comes from the Exchange and never from the request. It is the value
// the generated document attests, and the whole proof of control rests on the
// publisher being unable to choose it.
func (s *Service) draftSite(ctx context.Context, actor exchangeclient.Actor, d Draft) (setup.Site, error) {
	publisherID, err := s.publisherID(ctx, actor)
	if err != nil {
		return setup.Site{}, err
	}
	return draftSiteFor(publisherID, d)
}

// draftSiteFor is draftSite for a payee already read from the Exchange.
func draftSiteFor(publisherID string, d Draft) (setup.Site, error) {
	domain, err := Domain(d.Domain)
	if err != nil {
		return setup.Site{}, err
	}
	network := strings.TrimSpace(d.CDN)
	if !supported(cdn.Provider(network)) {
		return setup.Site{}, refusal.New(refusal.KindInvalid, "We cannot set your domain up on that CDN.")
	}
	// The file steps come before the licensing step, so nothing has been
	// chosen yet. The sample feed a mid-wizard download can carry is priced by
	// the terms the licensing step opens on, not by an empty set of controls,
	// which would build a term that permits everything.
	return setup.Site{
		Domain:           domain,
		PublisherID:      publisherID,
		CDN:              network,
		ContentPathScope: splitPaths(d.Paths),
		Terms:            defaultTerms(),
	}, nil
}

// unverifiedLimitText is what the domain step says to a publisher who cannot
// save another domain until one of theirs is verified.
var unverifiedLimitText = fmt.Sprintf("You already have %d domains that are not verified. "+
	"Finish setting one of them up before you add another.", consoleapi.MaxUnverifiedDomains)

// atUnverifiedLimit reports whether saving one more domain would be more than
// the publisher may hold without verifying: they already hold
// consoleapi.MaxUnverifiedDomains such domains. A domain they already hold is
// answered before this is asked.
func atUnverifiedLimit(regs exchangeclient.Registrations) bool {
	held := 0
	for _, site := range regs.Sites {
		if !site.Active() {
			held++
		}
	}
	return held >= consoleapi.MaxUnverifiedDomains
}
