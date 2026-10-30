package bootfixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/sitedomain"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
)

// idPattern is the shape of a tenant id and a publisher id: printable, with no
// space, and no longer than the admin plane allows a tenant id (255).
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

// refPattern is the shape of a key reference and a CloudFront key-pair id.
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,254}$`)

// seatDomainPattern is the shape of a seat's domain: the value of the
// Exchange's EXCHANGE_DOMAIN, a lower-case hostname with an optional port.
var seatDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?(:[0-9]{1,5})?$`)

// maxDisplayNameBytes bounds a publisher's display name.
const maxDisplayNameBytes = 200

func checkSeats(in []fileSeat) ([]Seat, error) {
	out := make([]Seat, 0, len(in))
	ids := map[string]bool{}
	domains := map[string]bool{}
	for i, s := range in {
		at := fmt.Sprintf("seats[%d]", i)
		seat, err := checkSeat(at, s)
		if err != nil {
			return nil, err
		}
		if ids[seat.TenantID] {
			return nil, fmt.Errorf("%s.tenant_id: seat %q is declared twice", at, seat.TenantID)
		}
		if domains[seat.Domain] {
			return nil, fmt.Errorf("%s.domain: %q is the domain of two seats", at, seat.Domain)
		}
		ids[seat.TenantID], domains[seat.Domain] = true, true
		out = append(out, seat)
	}
	return out, nil
}

func checkSeat(at string, s fileSeat) (Seat, error) {
	if !idPattern.MatchString(s.TenantID) {
		return Seat{}, fmt.Errorf("%s.tenant_id: %q is not a tenant id", at, s.TenantID)
	}
	if err := checkSeatDomain(s.Domain); err != nil {
		return Seat{}, fmt.Errorf("%s.domain: %w", at, err)
	}
	if !refPattern.MatchString(s.Ed25519KeyRef) {
		return Seat{}, fmt.Errorf("%s.ed25519_key_ref: %q is not a key reference", at, s.Ed25519KeyRef)
	}
	if s.RSAKeyRef != "" && !refPattern.MatchString(s.RSAKeyRef) {
		return Seat{}, fmt.Errorf("%s.rsa_key_ref: %q is not a key reference", at, s.RSAKeyRef)
	}
	if s.AllowBrokerRelay == nil {
		return Seat{}, fmt.Errorf("%s.allow_broker_relay is required", at)
	}
	if s.ActivateNewAgentsByDefault == nil {
		return Seat{}, fmt.Errorf("%s.activate_new_agents_by_default is required", at)
	}
	return Seat{
		TenantID:                   s.TenantID,
		Domain:                     s.Domain,
		Ed25519KeyRef:              s.Ed25519KeyRef,
		RSAKeyRef:                  s.RSAKeyRef,
		AllowBrokerRelay:           *s.AllowBrokerRelay,
		ActivateNewAgentsByDefault: *s.ActivateNewAgentsByDefault,
	}, nil
}

// checkSeatDomain refuses a seat domain that is not a lower-case hostname with
// an optional port. The Exchange finds its seat by comparing this value with
// EXCHANGE_DOMAIN, so it must be written exactly as that setting is.
func checkSeatDomain(domain string) error {
	if !seatDomainPattern.MatchString(domain) {
		return fmt.Errorf("%q must be a lower-case hostname, with an optional port, as EXCHANGE_DOMAIN is written",
			domain)
	}
	if _, port, found := strings.Cut(domain, ":"); found {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%q names port %s, which is not a port number", domain, port)
		}
	}
	return nil
}

// checker checks the publishers, against the seats the file declares and the
// sites already seen, and reads the terms files from terms.
type checker struct {
	seats map[string]Seat
	sites map[string]bool
	terms fs.FS
}

func (c checker) publishers(in []filePublisher) ([]Publisher, error) {
	out := make([]Publisher, 0, len(in))
	ids := map[string]bool{}
	for i, p := range in {
		at := fmt.Sprintf("publishers[%d]", i)
		pub, err := c.publisher(at, p)
		if err != nil {
			return nil, err
		}
		if ids[pub.ID] {
			return nil, fmt.Errorf("%s.publisher_id: publisher %q is declared twice", at, pub.ID)
		}
		ids[pub.ID] = true
		out = append(out, pub)
	}
	return out, nil
}

func (c checker) publisher(at string, p filePublisher) (Publisher, error) {
	if !idPattern.MatchString(p.ID) {
		return Publisher{}, fmt.Errorf("%s.publisher_id: %q is not a publisher id", at, p.ID)
	}
	name := strings.TrimSpace(p.DisplayName)
	if name == "" || len(name) > maxDisplayNameBytes {
		return Publisher{}, fmt.Errorf("%s.display_name must be 1 to %d bytes", at, maxDisplayNameBytes)
	}
	if len(p.Sites) == 0 {
		return Publisher{}, fmt.Errorf("%s.sites: a publisher needs at least one site", at)
	}
	sites := make([]Site, 0, len(p.Sites))
	for i, s := range p.Sites {
		site, err := c.site(fmt.Sprintf("%s.sites[%d]", at, i), s)
		if err != nil {
			return Publisher{}, err
		}
		sites = append(sites, site)
	}
	return Publisher{ID: p.ID, DisplayName: name, Sites: sites}, nil
}

func (c checker) site(at string, s fileSite) (Site, error) {
	normalized, err := sitedomain.Normalize(s.Domain)
	if err != nil {
		return Site{}, fmt.Errorf("%s.domain: %w", at, err)
	}
	if normalized != s.Domain {
		return Site{}, fmt.Errorf("%s.domain: write %q as %q", at, s.Domain, normalized)
	}
	if c.sites[s.Domain] {
		return Site{}, fmt.Errorf("%s.domain: site %q is declared twice", at, s.Domain)
	}
	scheme, keyPairID, err := siteSigning(s)
	if err != nil {
		return Site{}, fmt.Errorf("%s.%w", at, err)
	}
	provider, err := siteCDN(s.CDN)
	if err != nil {
		return Site{}, fmt.Errorf("%s.cdn: %w", at, err)
	}
	listings, err := c.listings(at, scheme, s.Listings)
	if err != nil {
		return Site{}, err
	}
	c.sites[s.Domain] = true
	return Site{
		Domain:              s.Domain,
		SigningScheme:       scheme,
		CDN:                 provider,
		CloudFrontKeyPairID: keyPairID,
		Listings:            listings,
	}, nil
}

// siteSigning checks the site's signing scheme and its CloudFront key-pair id,
// which an AWS_CLOUDFRONT_RSA site needs and no other site may carry.
func siteSigning(s fileSite) (signing.Scheme, string, error) {
	switch scheme := signing.Scheme(s.SigningScheme); scheme {
	case signing.SchemeEd25519:
		if s.CloudFrontKeyPairID != "" {
			return "", "", errors.New("cloudfront_key_pair_id: only an AWS_CLOUDFRONT_RSA site carries one")
		}
		return scheme, "", nil
	case signing.SchemeCFRSA:
		if !refPattern.MatchString(s.CloudFrontKeyPairID) {
			return "", "", errors.New("cloudfront_key_pair_id is required for an AWS_CLOUDFRONT_RSA site")
		}
		return scheme, s.CloudFrontKeyPairID, nil
	default:
		return "", "", fmt.Errorf("signing_scheme: %q is neither %s nor %s",
			s.SigningScheme, signing.SchemeEd25519, signing.SchemeCFRSA)
	}
}

// siteCDN checks the integration a site records, which is optional. The values
// are the ones fora.publisher_domains.cdn holds.
func siteCDN(name string) (cdn.Provider, error) {
	switch provider := cdn.Provider(name); provider {
	case "", cdn.ProviderCloudflare, cdn.ProviderFastly, cdn.ProviderCloudFront:
		return provider, nil
	default:
		return "", fmt.Errorf("%q is not one of %s, %s or %s",
			name, cdn.ProviderCloudflare, cdn.ProviderFastly, cdn.ProviderCloudFront)
	}
}

// listings checks a site's listings. Each names a seat the file declares, and a
// seat that sells an AWS_CLOUDFRONT_RSA site names the RSA key its delivery URLs
// are signed with. Only that the reference is named is checked here: the key
// itself is resolved when the Exchange first signs a CloudFront URL.
func (c checker) listings(at string, scheme signing.Scheme, in []fileListing) ([]Listing, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%s.listings: a site needs at least one listing", at)
	}
	out := make([]Listing, 0, len(in))
	seen := map[string]bool{}
	for i, l := range in {
		where := fmt.Sprintf("%s.listings[%d]", at, i)
		seat, declared := c.seats[l.TenantID]
		if !declared {
			return nil, fmt.Errorf("%s.tenant_id: %q is not a seat this fixture declares", where, l.TenantID)
		}
		if scheme == signing.SchemeCFRSA && seat.RSAKeyRef == "" {
			return nil, fmt.Errorf("%s.tenant_id: seat %q names no rsa_key_ref, "+
				"and this %s site's delivery URLs are signed with it", where, l.TenantID, signing.SchemeCFRSA)
		}
		if seen[l.TenantID] {
			return nil, fmt.Errorf("%s.tenant_id: the site is listed on seat %q twice", where, l.TenantID)
		}
		seen[l.TenantID] = true
		terms, err := c.readTerms(l.TermsFile)
		if err != nil {
			return nil, fmt.Errorf("%s.terms_file: %w", where, err)
		}
		out = append(out, Listing{
			TenantID:         l.TenantID,
			TermsFile:        l.TermsFile,
			Terms:            terms,
			ContentPathScope: append([]string{}, l.ContentPathScope...),
		})
	}
	return out, nil
}

// readTerms reads a terms file next to the fixture and checks that it holds one
// JSON object. A name that leaves the directory is refused before it is opened.
func (c checker) readTerms(name string) ([]byte, error) {
	if name == "" || name == "." || !fs.ValidPath(name) {
		return nil, fmt.Errorf("%q must name a file in the boot fixture's directory", name)
	}
	raw, err := readFile(c.terms, name, maxTermsBytes)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%q must hold one JSON object", name)
	}
	return raw, nil
}
