// Package wizard is the console's service layer: what the add-a-domain wizard
// and the registered-domain pages decide, apart from how either is served.
//
// The transport in the web package decodes a request, calls one method here and
// encodes the answer. Everything between -- which network serves a domain and
// whether a publisher can set it up, what their own site says about its
// licensing, what an agent would be shown, whether their edge package answers,
// and what a registration sends the Exchange -- is decided here, against narrow
// ports rather than the concrete Exchange client and site reader.
package wizard

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
)

// Reader is what reading a person's registrations takes from the Exchange: the
// registrations themselves, and the denomination every price is shown in.
//
// The denomination is the Exchange's because the Exchange stores the terms a
// publisher agrees; the console asks it rather than carrying a currency of its
// own that could differ. The client reuses the answer for a short while, so
// asking on every request costs one signed call a minute rather than one per
// keystroke.
type Reader interface {
	ListRegistrations(ctx context.Context, actor exchangeclient.Actor) (exchangeclient.Registrations, error)
	TermsDefaults(ctx context.Context) (termspreset.Defaults, error)
}

// Registry is what the wizard takes from the Exchange: the reads, and the calls
// that register a business, ask about a domain, save one, set a saved one's
// terms, and verify one.
type Registry interface {
	Reader
	RegisterPublisher(
		ctx context.Context, actor exchangeclient.Actor, displayName string,
	) (exchangeclient.Publisher, error)
	PreflightDomain(
		ctx context.Context, actor exchangeclient.Actor, publisherID, domain string,
	) (exchangeclient.PreflightResult, error)
	RegisterDomain(ctx context.Context, in exchangeclient.NewSite) (exchangeclient.Registration, error)
	SetDomainTerms(ctx context.Context, in exchangeclient.DomainTerms) (exchangeclient.Registration, error)
	CheckDomain(
		ctx context.Context, actor exchangeclient.Actor, publisherID, domain string,
	) (exchangeclient.CheckResult, error)
}

// Bundler produces the setup files for one site.
type Bundler interface {
	Build(ctx context.Context, s setup.Site) (setup.Bundle, error)
}

// Detector proposes which network serves a site, so the person confirms a
// guess rather than answering a question about their own hosting they may not
// know. It is a narrow port: a detection failure is not an error, it just means
// nothing is preselected.
//
// It takes the site's response headers as well as its name, and both matter.
// DNS is the stronger signal but is silent at an apex, where a name cannot
// carry the delegation that identifies the network; the headers are what
// answer there. A lookup given no headers therefore misses every apex domain
// behind a network that only announces itself in a header, which is most of
// them.
type Detector interface {
	Detect(ctx context.Context, host string, hdr http.Header) (cdn.Detection, error)
}

// SiteReader reads a publisher's own site. Two callers, two methods, because
// they need different amounts of work: the lookup wants the home page's
// response headers and nothing else, and the preview wants a page worth
// showing, which means finding one through the sitemap.
//
// Keeping them apart is not tidiness. Using the second for the first made a
// lookup wait on several requests against a site nobody has warmed up, and on
// a large site it timed out -- reported to the publisher as "we could not tell
// which network serves it", which is a different statement and a false one.
type SiteReader interface {
	Read(ctx context.Context, domain, articleURL string) (pagefetch.Site, error)
	HomeHeaders(ctx context.Context, domain string) (http.Header, error)
	// LicenseDocument returns the site's own licensing document, so the
	// licensing step opens with the publisher's terms rather than ours. It
	// takes the read Read already did, so looking for the document does not
	// fetch robots.txt a second time.
	LicenseDocument(ctx context.Context, domain string, site *pagefetch.Site) ([]byte, string, error)
	// SiteURL is the address of path on domain, in the scheme the reader reads
	// sites with. The package check asks its questions at these addresses, so
	// it reaches the site the way the reads above do.
	SiteURL(domain, path string) string
	AgentProber
}

// Config wires the wizard.
type Config struct {
	Registry Registry
	Bundles  Bundler
	Detector Detector
	// Pages reads a publisher's own site, so the lookup can see the headers it
	// answers with. Nil leaves the lookup on DNS alone.
	Pages SiteReader
	// SupportEmail is the address the domain step shows when the network is
	// not one a publisher can set up here. It is the only way out of that
	// screen, so a deployment that leaves it empty leaves those publishers
	// with a sentence and nobody to send it to.
	SupportEmail string
	// DefaultRate fills the price field in when a publisher chooses to charge.
	DefaultRate string
	// ExchangeDomain is what a previewed offer says it is sold by.
	ExchangeDomain string
}

// Service is the add-a-domain wizard's steps -- the domain step's lookup and
// save, the licensing step's read, preview and save, and a saved domain's
// files -- and the registration of the business every step acts for. Where a
// saved domain's setup stands is Resumer's to say; the last step's check of the
// site, and the one verification it asks for, are checkConnection's, which
// Resumer.Check calls.
type Service struct {
	registry Registry
	bundles  Bundler
	detector Detector
	pages    SiteReader
	// supportEmail is who a publisher writes to when their network is not one
	// they can set up here, and it is the deployment's to decide.
	supportEmail string
	// defaultRate is what the price field starts at. It is only a starting
	// point -- the publisher sets what they charge -- but it cannot be zero,
	// which the term builder refuses as a free term written the long way.
	defaultRate string
	// exchangeDomain is what the previewed offer says it is sold by.
	exchangeDomain string
}

// New builds the wizard.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Registry == nil:
		return nil, errors.New("wizard: an Exchange client is required")
	case cfg.Bundles == nil:
		return nil, errors.New("wizard: a setup-file builder is required")
	}
	return &Service{
		registry:       cfg.Registry,
		bundles:        cfg.Bundles,
		detector:       cfg.Detector,
		pages:          cfg.Pages,
		supportEmail:   strings.TrimSpace(cfg.SupportEmail),
		defaultRate:    defaultRateOr(cfg.DefaultRate),
		exchangeDomain: cfg.ExchangeDomain,
	}, nil
}

// defaultRateOr falls back to the smallest amount that is not free.
//
// Zero is not a usable default here: the term builder refuses a per-access
// price of zero because that is a free term written the long way, so a
// deployment that configured nothing would hand every publisher a form that
// refuses itself.
func defaultRateOr(configured string) string {
	return cmp.Or(strings.TrimSpace(configured), "0.01")
}

// Start is everything the wizard needs before its first call.
type Start struct {
	SupportEmail string
	DefaultRate  string
	Currency     string
	// Unit is what one charge is for, said the way a price reads it: "fetch".
	Unit string
	// Terms is what the licensing step opens on before anything is read from
	// the publisher's site.
	Terms     termspreset.Controls
	Functions []Choice
}

// Start is what the wizard opens on: the denomination the price field is in,
// read from the Exchange, and the terms and uses the licensing step offers.
func (s *Service) Start(ctx context.Context) (Start, error) {
	d, err := s.registry.TermsDefaults(ctx)
	if err != nil {
		return Start{}, err
	}
	return Start{
		SupportEmail: s.supportEmail,
		DefaultRate:  s.defaultRate,
		Currency:     d.Vocab().Currency,
		Unit:         termspreset.SingularUnit(d.Vocab().Unit),
		Terms:        defaultTerms(),
		Functions:    functionChoices(),
	}, nil
}

// registrations reads what this person has registered, refusing a person who
// has not registered a business: every step of the wizard acts for that
// business.
func (s *Service) registrations(
	ctx context.Context, actor exchangeclient.Actor,
) (exchangeclient.Registrations, error) {
	regs, err := s.registry.ListRegistrations(ctx, actor)
	if err != nil {
		return exchangeclient.Registrations{}, err
	}
	if !regs.Registered {
		// The wizard's business step registers the name before any step that
		// acts for it, so a call without one is a direct call, a tab left open
		// across a sign-in as somebody else, or a page opened before an
		// operator removed the grant. A reload settles each of them: the page
		// then opens on the business step.
		return exchangeclient.Registrations{}, refusal.New(refusal.KindNotRegistered,
			"Your business is not registered yet. Please reload this page and start again.")
	}
	return regs, nil
}

// savedDomain finds one of the person's saved domains by what the browser sent:
// the hostname rule first, then what the Exchange lists for them. A domain
// that is not theirs is refused before anything is asked about it.
//
// The payee comes back with it, read from the Exchange on every call rather
// than carried in the session or sent by the browser: it decides which payee
// a generated document attests, and a value the browser could set is a value
// the browser could change.
func (s *Service) savedDomain(
	ctx context.Context, actor exchangeclient.Actor, rawDomain string,
) (exchangeclient.Registrations, exchangeclient.Registration, error) {
	domain, err := Domain(rawDomain)
	if err != nil {
		return exchangeclient.Registrations{}, exchangeclient.Registration{}, err
	}
	regs, err := s.registrations(ctx, actor)
	if err != nil {
		return exchangeclient.Registrations{}, exchangeclient.Registration{}, err
	}
	reg, err := siteIn(regs, domain)
	return regs, reg, err
}
