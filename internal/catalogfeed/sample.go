package catalogfeed

import (
	"fmt"
	"net/url"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// SampleForPage renders the feed line a publisher would push for one page of
// their site, and says in sentences what the Exchange would refuse about it.
//
// The onboarding preview and the publisher console both show a publisher this
// line for a page they read from the publisher's own site, so the steps from a
// page to a line are here once: the entry's path is the escaped path of the
// canonical address, without its query or fragment and with a trailing slash
// kept, and an address with no path is the site root.
//
// An error means no line could be built at all, and each caller words that for
// its own page. A line the check refuses is still returned, with warnings: the
// first says the line would be refused as it stands, and one more names each
// violation.
func SampleForPage(
	domain, canonicalURL, title string, terms []*forav1.LicenseTerm,
) ([]byte, []string, error) {
	entry := &forav1.ResourceEntry{
		Domain: domain,
		Path:   PagePath(canonicalURL),
		Terms:  terms,
	}
	if title != "" {
		entry.Title = &title
	}
	line, report, err := SampleFor(entry)
	if err != nil {
		return nil, nil, err
	}
	if report.OK() {
		return line, nil, nil
	}
	warnings := make([]string, 0, report.Violations()+1)
	warnings = append(warnings,
		"the sample feed line below is not one the Exchange would accept as it stands")
	for _, f := range report.Findings {
		if f.Violation {
			warnings = append(warnings, fmt.Sprintf("sample feed, %s: %s", f.Rule, f.Message))
		}
	}
	return line, warnings, nil
}

// PagePath returns the path a catalogue entry records for a page address: its
// escaped path, without query or fragment. An address with no path, or one
// that does not parse, is the site root.
func PagePath(address string) string {
	parsed, err := url.Parse(address)
	if err != nil || parsed.EscapedPath() == "" {
		return "/"
	}
	return parsed.EscapedPath()
}
