package pagefetch

import (
	"net/url"
	"strings"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/publicerr"
)

// NormalizeArticleURL checks an optional article address, matching the domain when supplied.
func NormalizeArticleURL(raw, domain string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", publicerr.Newf(publicerr.KindInvalidRequest, "that article address cannot be read")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", publicerr.Newf(publicerr.KindInvalidRequest,
			"an article address must start with http or https")
	}
	if parsed.User != nil {
		// Credentials in an address are a sign the visitor pasted something
		// they did not mean to share, and they would travel to the origin.
		return "", publicerr.Newf(publicerr.KindInvalidRequest,
			"remove the credentials from that article address")
	}
	if domain == "" && !helpers.IsBareDomain(parsed.Hostname()) {
		return "", publicerr.Newf(publicerr.KindInvalidRequest, "an article address must name a domain")
	}
	if domain != "" && !SameSite(parsed.Host, domain) {
		return "", publicerr.Newf(publicerr.KindInvalidRequest,
			"that article is on %s, not on %s; a preview reads one site", parsed.Host, domain)
	}
	return parsed.String(), nil
}
