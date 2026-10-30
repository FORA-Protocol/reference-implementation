package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/bootfixture"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// BootPlan is a boot fixture (ADR-029) checked by the rules the Exchange applies
// to the same rows when a publisher registers them, as the rows Apply creates:
// the seats, then each payee with its sites, then each site's listings.
type BootPlan struct {
	// Path and SHA256 name the fixture the plan was made from; the audit rows of
	// what Apply creates carry both.
	Path       string
	SHA256     string
	Seats      []repo.NewSeat
	Publishers []PlannedPublisher
}

// PlannedPublisher is a payee and its sites, ready to create.
type PlannedPublisher struct {
	Publisher repo.NewPublisher
	Sites     []PlannedSite
}

// PlannedSite is a site and the listings it is offered through, ready to create.
type PlannedSite struct {
	Site     repo.NewProvisionedSite
	Listings []repo.NewActiveListing
}

// PlanBootFixture checks a boot fixture by registration's own rules and returns
// the rows Apply creates. Each listing's terms must decode strictly into the
// shape the console stores a listing's terms in, and build into license terms in
// the deployment's denomination (defaults); they are stored as registration
// stores them. A listing's content path scope is normalized as registration's
// is. It reads and writes nothing, so a bad fixture stops a boot before the
// Exchange connects to its database.
func PlanBootFixture(f bootfixture.Fixture, defaults termspreset.Defaults) (BootPlan, error) {
	plan := BootPlan{Path: f.Path, SHA256: f.SHA256, Seats: make([]repo.NewSeat, 0, len(f.Seats))}
	for _, s := range f.Seats {
		plan.Seats = append(plan.Seats, repo.NewSeat{
			TenantID:                   s.TenantID,
			Domain:                     s.Domain,
			Ed25519KeyRef:              s.Ed25519KeyRef,
			RSAKeyRef:                  s.RSAKeyRef,
			AllowBrokerRelay:           s.AllowBrokerRelay,
			ActivateNewAgentsByDefault: s.ActivateNewAgentsByDefault,
		})
	}
	plan.Publishers = make([]PlannedPublisher, 0, len(f.Publishers))
	for _, p := range f.Publishers {
		planned := PlannedPublisher{
			Publisher: repo.NewPublisher{ID: p.ID, DisplayName: p.DisplayName},
			Sites:     make([]PlannedSite, 0, len(p.Sites)),
		}
		for _, s := range p.Sites {
			site, err := planSite(p.ID, s, defaults)
			if err != nil {
				return BootPlan{}, err
			}
			planned.Sites = append(planned.Sites, site)
		}
		plan.Publishers = append(plan.Publishers, planned)
	}
	return plan, nil
}

func planSite(publisherID string, s bootfixture.Site, defaults termspreset.Defaults) (PlannedSite, error) {
	planned := PlannedSite{
		Site: repo.NewProvisionedSite{
			Domain:              s.Domain,
			PublisherID:         publisherID,
			SigningScheme:       string(s.SigningScheme),
			CDN:                 string(s.CDN),
			CloudFrontKeyPairID: s.CloudFrontKeyPairID,
		},
		Listings: make([]repo.NewActiveListing, 0, len(s.Listings)),
	}
	for _, l := range s.Listings {
		where := fmt.Sprintf("site %q on seat %q", s.Domain, l.TenantID)
		terms, err := planTerms(l.Terms, defaults)
		if err != nil {
			return PlannedSite{}, exchange.Wrap(exchange.KindInvalidRequest, err,
				fmt.Sprintf("%s: terms_file %q", where, l.TermsFile))
		}
		scope, err := normalizePathScope(l.ContentPathScope)
		if err != nil {
			return PlannedSite{}, exchange.Wrap(exchange.KindInvalidRequest, err, where+": content_path_scope")
		}
		planned.Listings = append(planned.Listings, repo.NewActiveListing{
			TenantID:         l.TenantID,
			Domain:           s.Domain,
			ContentPathScope: scope,
			Terms:            terms,
		})
	}
	return planned, nil
}

// planTerms decodes a listing's terms strictly, refusing a member the stored
// shape does not name, and returns them as registration stores validated terms.
func planTerms(raw []byte, defaults termspreset.Defaults) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var terms Terms
	if err := dec.Decode(&terms); err != nil {
		return nil, fmt.Errorf("decode the terms: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("the terms file holds more than one JSON value")
	}
	valid, err := validateTerms(terms, defaults)
	if err != nil {
		return nil, err
	}
	return encodeTerms(valid)
}
