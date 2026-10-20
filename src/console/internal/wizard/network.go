// Working out which network serves a domain, and which of those a publisher can
// set up by themselves. The wizard's domain step is the only caller: a domain
// whose network is not on this list cannot be registered here at all, so the
// answer decides whether the wizard goes on.

package wizard

import (
	"context"
	"net/http"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/setupsteps"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/sitedomain"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
)

// Domain applies the shared hostname rule to what the wizard sent, and returns
// the refusal a publisher reads when the value fails it.
//
// Every step that takes a domain runs this before anything else, because each
// of them makes a request to whatever it is given, and a publisher who pasted a
// URL or mistyped a name should read what is wrong with it rather than whatever
// the request to it went on to say.
func Domain(raw string) (string, error) {
	domain, err := sitedomain.Normalize(raw)
	if err != nil {
		return "", refusal.Wrap(refusal.KindInvalid, domainRefusal(err), err)
	}
	return domain, nil
}

// domainRefusal turns the shared rule's sentence into one addressed to the
// person who typed it. The rule states the fault about a value; a publisher is
// reading it about the field in front of them.
func domainRefusal(err error) string {
	return strings.ToUpper(err.Error()[:1]) + err.Error()[1:] +
		". Enter the hostname only, with no https:// in front and no path after it."
}

// detectNetwork proposes which network serves the domain, and says what the
// publisher reads when the answer is not one they can set up themselves.
//
// Two outcomes end the wizard and they share a sentence: a network we recognise
// but have no package for, and nothing recognised at all. Naming the first by
// name would tell the publisher we know what serves their site and that the gap
// is ours, which is true -- but the repair is the same conversation either way,
// and one sentence is one thing to keep accurate.
func (s *Service) detectNetwork(
	ctx context.Context, domain string,
) (provider cdn.Provider, ok bool, message string) {
	if s.detector == nil {
		return cdn.ProviderNone, false, undetectedText
	}
	found, err := s.detector.Detect(ctx, domain, s.siteHeaders(ctx, domain))
	if err != nil {
		reqctx.FromContext(ctx).WarnContext(ctx, "console.detect_failed",
			"domain", domain, "err", err)
		return cdn.ProviderNone, false, undetectedText
	}
	switch {
	case supported(found.Provider):
		return found.Provider, true, ""
	case found.Provider == cdn.ProviderNone:
		// Nothing recognised is not a CDN called "none".
		return cdn.ProviderNone, false, undetectedText
	default:
		return found.Provider, false, unsupportedText(found.Provider)
	}
}

// The two ways the domain step ends, and they are different answers that a
// publisher acts on differently.
//
// Recognising a network and having a package for it are separate things. A
// site behind Fastly is identified exactly, and saying so tells the publisher
// we know what serves them and that the gap is ours; telling them instead that
// we could not work it out reads as a broken check and sends them to look at
// their DNS. Finding nothing is the other answer, and it is the one where
// there is something on their side to look at.
const undetectedText = "We could not detect a CDN in front of this domain."

func unsupportedText(p cdn.Provider) string {
	return networkLabel(p) + " serves this domain, and we do not support " +
		networkLabel(p) + " yet."
}

// siteHeaders asks the publisher's home page for the headers it answers with,
// which is the half of the lookup DNS cannot supply.
//
// One request, and the page itself is not read: see pagefetch.HomeHeaders. A
// site that does not answer yields no headers rather than an error -- that is
// the ordinary state of a site being set up, and it leaves the lookup on DNS
// alone.
func (s *Service) siteHeaders(ctx context.Context, domain string) http.Header {
	if s.pages == nil {
		return nil
	}
	header, err := s.pages.HomeHeaders(ctx, domain)
	if err != nil {
		reqctx.FromContext(ctx).InfoContext(ctx, "console.site_headers_unavailable",
			"domain", domain, "err", err)
		return nil
	}
	return header
}

// networkLabel names a network the way its own documentation does, so a
// publisher reads the name they would recognise rather than the token this
// code keys on. It is the one copy: the browser shows the label the server
// sent rather than mapping the token a second time.
func networkLabel(p cdn.Provider) string {
	switch p {
	case cdn.ProviderCloudflare:
		return "Cloudflare"
	case cdn.ProviderCloudFront:
		return "AWS CloudFront"
	case cdn.ProviderFastly:
		return "Fastly"
	default:
		return string(p)
	}
}

// supported reports whether a publisher can complete setup on this network by
// themselves. The answer is cdn.SelfServe, the list the Exchange admits
// registrations from, so the wizard cannot go on for a network the last button
// would be refused on. The reason Fastly is not on it is written there.
func supported(p cdn.Provider) bool {
	return cdn.Supported(string(p))
}

// cdnProvider maps the integration recorded at registration onto the step text.
//
// An unrecognised value yields the no-integration steps rather than an error:
// registration only admits the three, so reaching here with anything else
// means the stored value predates the current set, and showing the generic
// steps is more use than refusing to render anything at all.
func cdnProvider(name string) cdn.Provider {
	switch cdn.Provider(name) {
	case cdn.ProviderCloudflare:
		return cdn.ProviderCloudflare
	case cdn.ProviderCloudFront:
		return cdn.ProviderCloudFront
	default:
		return cdn.ProviderNone
	}
}

// edgeSteps is what to do with the edge configuration on this network. Only
// the step that needs the publisher's own CDN account survives: the rest
// describe what the wizard has already walked them through.
func edgeSteps(s setup.Site) []setupsteps.Step {
	all := setupsteps.RunnableByHand(setupsteps.For(cdnProvider(s.CDN), s.Domain, nil))
	out := make([]setupsteps.Step, 0, 1)
	for _, step := range all {
		if step.Integration != "" {
			out = append(out, step)
		}
	}
	return out
}

// splitPaths turns the comma-separated field into the list the Exchange stores.
func splitPaths(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := make([]string, 0, strings.Count(raw, ",")+1)
	for p := range strings.SplitSeq(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
