// The wizard's domain step -- the domain and what serves it -- and the files a
// saved domain is set up with. The business step is in business.go, the
// licensing step in page.go, the last step's checks in connection.go, and
// reopening a saved domain at the step it stands on in resume.go.

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

// The wizard's steps, by the key the page walks them by and the page that
// finishes a saved domain's setup reopens it on.
//
// StepName is the business name, shown only to an account that has not named
// its business, before the others.
const (
	StepName    = "name"
	StepDomain  = "domain"
	StepTerms   = "terms"
	StepConnect = "connect"
)

// Lookup is everything the console can find out about a domain before the
// publisher saves it.
//
// The network and the registration are answered together on purpose. They are
// two questions -- who serves this, and could I register it -- but the
// publisher asks both by typing one address, and answering them in one call
// spares the wizard a second round trip while they are still reading the first
// answer.
type Lookup struct {
	Domain   string
	Provider cdn.Provider
	// ProviderLabel is the network's own name, so there is one place where
	// "cloudfront" becomes "AWS CloudFront".
	ProviderLabel string
	Supported     bool
	// Taken is set when another publisher holds the domain. Message then says
	// so.
	Taken bool
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
}

// Lookup works out which network serves a domain and whether it could be
// saved.
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
		// the save anyway; this tells the publisher before they press Next.
		out.LimitReached, out.Message = true, unverifiedLimitText
		return out, nil
	}
	out.Provider, out.Supported, out.Message = s.detectNetwork(ctx, domain)
	out.ProviderLabel = networkLabel(out.Provider)
	if !out.Supported {
		// The Exchange is not asked about a domain the publisher cannot set up
		// here. It would fetch their document to answer, which is a request to
		// somebody else's server made for a wizard that has already stopped.
		return out, nil
	}
	res, err := s.registry.PreflightDomain(ctx, actor, regs.PublisherID, domain)
	if err != nil {
		return Lookup{}, err
	}
	// Domain taken is the one reason the domain step reads: it is the only one
	// the publisher cannot repair, and saving the domain would be refused for
	// it. The others are about the file, which the last step checks.
	if containsFold(res.Reasons, consoleapi.ReasonDomainTaken) {
		out.Taken, out.Message = true, reasonText[consoleapi.ReasonDomainTaken]
	}
	return out, nil
}

// Draft is the domain step's answers: the address, the network the lookup
// found behind it, and the licensed paths field as the publisher filled it in.
type Draft struct {
	Domain string
	CDN    string
	// Paths is the comma-separated field the publisher filled in.
	Paths string
}

// SaveDomain is the domain step's Next: it saves the domain on the Exchange,
// with no terms yet, and answers with the domain as it is saved.
//
// Saving holds the hostname for this publisher from here on, and the domain is
// listed as not verified until it has terms and its file and its package both
// answer, so a publisher who leaves after this step loses nothing. Its terms are asked on
// the next step, and the import's answer beside them.
//
// The Exchange decides what cannot be saved -- a domain another publisher
// holds, one too many domains not verified -- and nothing is written then. A
// domain this publisher saved before is answered as it was saved, whatever was
// typed this time: its address, network and paths are fixed by the first save.
func (s *Service) SaveDomain(
	ctx context.Context, actor exchangeclient.Actor, d Draft,
) (exchangeclient.Registration, error) {
	domain, err := Domain(d.Domain)
	if err != nil {
		return exchangeclient.Registration{}, err
	}
	network := strings.TrimSpace(d.CDN)
	if !supported(cdn.Provider(network)) {
		return exchangeclient.Registration{}, refusal.New(refusal.KindInvalid,
			"We cannot set your domain up on that CDN.")
	}
	regs, err := s.registrations(ctx, actor)
	if err != nil {
		return exchangeclient.Registration{}, err
	}
	return s.registry.RegisterDomain(ctx, exchangeclient.NewSite{
		Actor:            actor,
		PublisherID:      regs.PublisherID,
		Domain:           domain,
		CDN:              network,
		ContentPathScope: splitPaths(d.Paths),
	})
}

// Files is a saved domain's setup files, what to do with the edge
// configuration on its network, and where its package runs once it answers.
type Files struct {
	Site      setup.Site
	Bundle    setup.Bundle
	EdgeSteps []setupsteps.Step
	// Deployed says where the package runs once it answers, for the last
	// step to show beside it then.
	Deployed string
}

// Files builds the files for one of the person's saved domains, from the
// registration the Exchange holds, never from anything the browser sent. They
// are the files the domain's own page offers.
func (s *Service) Files(ctx context.Context, actor exchangeclient.Actor, rawDomain string) (Files, error) {
	regs, reg, err := s.savedDomain(ctx, actor, rawDomain)
	if err != nil {
		return Files{}, err
	}
	site := siteOf(regs.PublisherID, reg)
	bundle, err := build(ctx, s.bundles, site)
	if err != nil {
		return Files{}, err
	}
	return Files{
		Site: site, Bundle: bundle, EdgeSteps: edgeSteps(site), Deployed: deployedText(cdnProvider(site.CDN)),
	}, nil
}

// build produces a saved domain's bundle, or the refusal a publisher reads
// when it cannot be produced. The domain is saved either way, which is what
// the sentence says.
func build(ctx context.Context, b Bundler, site setup.Site) (setup.Bundle, error) {
	bundle, err := b.Build(ctx, site)
	if err != nil {
		reqctx.FromContext(ctx).ErrorContext(ctx, "console.bundle_failed",
			"domain", site.Domain, "err", err)
		return setup.Bundle{}, refusal.Wrap(refusal.KindBuildFailed,
			"Something went wrong on our side. Your registration is unaffected.", err)
	}
	return bundle, nil
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
