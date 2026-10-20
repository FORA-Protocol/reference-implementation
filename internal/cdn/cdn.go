// Package cdn works out which content delivery network sits in front of a
// publisher's site, because that decides which edge integration they need.
//
// Two sources of evidence, because neither alone is reliable. DNS says who the
// site delegated its name to, which is the strongest signal but is invisible
// when a record is flattened at the apex. Response headers say who served the
// page, which every network volunteers but which a caching proxy in between can
// also produce. Both are collected, and one rule decides between them.
package cdn

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
	"golang.org/x/sync/errgroup"
)

// Provider names a supported content delivery network.
type Provider string

const (
	// ProviderNone means no supported network was recognised. It is an answer,
	// not a failure: the publisher is routed to a conversation with us.
	ProviderNone Provider = "none"
	// ProviderCloudFront is AWS CloudFront, which takes the Lambda@Edge bundle.
	// That function verifies the delivered address itself, the same way the
	// Cloudflare and Fastly packages do; CloudFront's own signed-address check
	// is a separate mechanism and is deliberately left off.
	ProviderCloudFront Provider = "cloudfront"
	// ProviderCloudflare is Cloudflare, which runs the worker.
	ProviderCloudflare Provider = "cloudflare"
	// ProviderFastly is Fastly. The detector recognises it, and self-service
	// setup does not offer it: nothing here builds a package a publisher could
	// install on Fastly Compute. Recognising it is still worth doing -- a site
	// we can name is a better conversation than a site we could not identify.
	ProviderFastly Provider = "fastly"
	// ProviderAkamai is Akamai. It is recognised so the page can say which
	// network it saw, but no edge package is shipped for it: a site on Akamai
	// takes the same conversation as a site on no recognised network, with the
	// difference that we already know what serves it. Which networks have a
	// package is not decided here: the onboarding steps data file says it, once
	// per network, and the preview reads it from there.
	ProviderAkamai Provider = "akamai"
)

// Source says where one piece of evidence came from.
type Source string

const (
	// SourceCNAME is the delegation the site's own name points at.
	SourceCNAME Source = "dns_cname"
	// SourceNS is the name servers of the site's zone.
	SourceNS Source = "dns_ns"
	// SourceHeader is a response header of the fetched page.
	SourceHeader Source = "header"
)

// Evidence is one observation that points at a provider.
type Evidence struct {
	Provider Provider `json:"provider"`
	Source   Source   `json:"source"`
	// Name is what was looked at: the host that was resolved, or the header
	// that was read.
	Name string `json:"name"`
	// Value is what was found. A DNS answer is reported as written, because it
	// is public and it is the evidence itself. A header is reported as
	// "present": its value can carry a request identifier, and naming the
	// header is what tells the publisher where to look.
	Value string `json:"value"`
}

// Detection is the verdict and everything it was drawn from.
type Detection struct {
	Provider Provider   `json:"provider"`
	Evidence []Evidence `json:"evidence"`
}

// Resolver is the DNS the detector reads. It is satisfied by *net.Resolver, so
// production passes the real one and a test passes a fixture; nothing in this
// package builds one, because a package that resolves names on its own cannot
// be tested without a network.
type Resolver interface {
	LookupCNAME(ctx context.Context, host string) (string, error)
	LookupNS(ctx context.Context, name string) ([]*net.NS, error)
}

// Detector answers which network fronts a site.
type Detector struct {
	resolver  Resolver
	perLookup time.Duration
}

// NewDetector builds a detector over the given resolver, bounding each lookup.
// A nil resolver is refused rather than defaulted: a detector that quietly
// resolved through the process's own DNS would be a network call this package
// never declared.
func NewDetector(resolver Resolver, perLookup time.Duration) (*Detector, error) {
	if resolver == nil {
		return nil, errNoResolver
	}
	if perLookup <= 0 {
		perLookup = defaultLookupTimeout
	}
	return &Detector{resolver: resolver, perLookup: perLookup}, nil
}

// defaultLookupTimeout bounds one DNS lookup when the caller states none.
const defaultLookupTimeout = 3 * time.Second

// Detect collects the evidence for host and decides. Page headers may be nil,
// which happens when the page could not be fetched; DNS alone then decides.
//
// A lookup that fails contributes no evidence rather than failing the
// detection. A site with no CNAME is the ordinary case for an apex record, and
// a preview that refused to answer because one of three lookups timed out would
// be worse than one that answers from the other two.
func (d *Detector) Detect(ctx context.Context, host string, page http.Header) Detection {
	host = normalizeName(host)
	evidence := d.lookupEvidence(ctx, host)
	evidence = append(evidence, ClassifyHeaders(page)...)
	return Detection{Provider: Decide(evidence), Evidence: evidence}
}

// lookupEvidence runs the three DNS lookups together and returns their evidence
// in a fixed order, so the same site always produces the same answer rather
// than one that depends on which lookup finished first.
func (d *Detector) lookupEvidence(ctx context.Context, host string) []Evidence {
	names := d.lookupNames(host)
	found := make([][]Evidence, len(names))
	var g errgroup.Group
	for i, name := range names {
		g.Go(func() error {
			found[i] = d.evidenceFor(ctx, name)
			return nil
		})
	}
	// Every leg swallows its own failure, so this cannot return an error; the
	// call is here to wait for all three.
	_ = g.Wait()
	var out []Evidence
	for _, e := range found {
		out = append(out, e...)
	}
	return out
}

// lookupTarget is one DNS question.
type lookupTarget struct {
	name string
	zone bool
}

// lookupNames lists the questions to ask about a host: its own delegation, the
// www form's — which is where a flattened apex hides the answer — and the
// zone's name servers.
func (d *Detector) lookupNames(host string) []lookupTarget {
	targets := []lookupTarget{{name: host}}
	if !strings.HasPrefix(host, "www.") && host != "" {
		targets = append(targets, lookupTarget{name: "www." + host})
	}
	if zone, err := publicsuffix.EffectiveTLDPlusOne(hostWithoutPort(host)); err == nil {
		targets = append(targets, lookupTarget{name: zone, zone: true})
	}
	return targets
}

// evidenceFor asks one question under its own deadline.
func (d *Detector) evidenceFor(ctx context.Context, target lookupTarget) []Evidence {
	ctx, cancel := context.WithTimeout(ctx, d.perLookup)
	defer cancel()
	if target.zone {
		ns, err := d.resolver.LookupNS(ctx, target.name)
		if err != nil {
			return nil
		}
		return ClassifyNS(target.name, ns)
	}
	cname, err := d.resolver.LookupCNAME(ctx, hostWithoutPort(target.name))
	if err != nil {
		return nil
	}
	if e, ok := ClassifyCNAME(target.name, cname); ok {
		return []Evidence{e}
	}
	return nil
}
