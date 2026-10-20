// Package sitedomain holds the one rule for what a publisher's domain may be.
//
// Two services need it and they need the same answer. The Exchange applies it
// because the value keys the site table and is later joined into the URL the
// discovery document is fetched from, so a hostname that does not round-trip
// produces a row whose key does not match what the fetch resolves. The console
// applies it because it asks a publisher to type that value, and it makes a DNS
// query and a request to whatever they typed -- a rule that lived only in the
// Exchange would let a typo become an outbound request first and a refusal
// three steps later.
//
// The sentences are here rather than at either caller, so a publisher reading
// "that is not a hostname" reads the same sentence wherever it was refused.
package sitedomain

import (
	"fmt"
	"net"
	"strings"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

// Normalize returns the domain in the form every lookup and every row keys on,
// or says what is wrong with it in a sentence the publisher who typed it can
// act on.
//
// Normalization happens once, here, so the value written is the value every
// later lookup produces from the same input: lower-cased, trimmed, and without
// the trailing dot that makes a name fully qualified in DNS but a different
// string in a table.
func Normalize(raw string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimSuffix(d, ".")
	switch {
	case d == "":
		return "", fmt.Errorf("domain is required")
	case strings.Contains(d, "/"), strings.Contains(d, "@"), strings.Contains(d, ":"):
		return "", fmt.Errorf(
			"domain %q must be a bare hostname, with no scheme, port or path", raw)
	case net.ParseIP(d) != nil:
		// The document fetch is guarded against private destinations, and a
		// site identified by an address rather than a name has no stable
		// identity to register.
		return "", fmt.Errorf("domain %q must be a hostname, not an IP address", raw)
	case !strings.Contains(d, "."):
		return "", fmt.Errorf("domain %q must be a fully qualified hostname", raw)
	}
	for _, r := range d {
		if r <= ' ' || r > '~' {
			return "", fmt.Errorf(
				"domain %q contains characters that are not allowed in a hostname", raw)
		}
	}
	// The checks above give the commonest mistakes their own sentence. The
	// protocol's bare-domain rule has the final word: a name it refuses here is
	// a name the protocol's own validation would refuse later.
	if !helpers.IsBareDomain(d) {
		return "", fmt.Errorf(
			"domain %q is not a valid hostname: each part between dots must be "+
				"letters, digits or inner hyphens, and the whole name at most %d characters",
			raw, helpers.MaxBareDomainLen)
	}
	return d, nil
}
