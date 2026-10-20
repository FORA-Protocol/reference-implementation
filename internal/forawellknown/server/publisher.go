package server

import (
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
)

// OperatorRelationship is the relationship a publisher's document states for
// the operator's catalog contributor: the party that submits the catalogue on
// the publisher's behalf.
const OperatorRelationship = "operator"

// Publisher describes the discovery document a publisher site serves for one
// Exchange seat. It is the shape the publisher console generates, and the one
// test fixtures build, so a fixture cannot describe a document the console
// never produces.
type Publisher struct {
	// Domain is the publisher site's host.
	Domain string
	// ExchangeDomain is the seat's hostname, matched exactly by the Exchange.
	ExchangeDomain string
	// ExchangeEndpoint is the origin an agent calls.
	ExchangeEndpoint string
	// ResourceOwnerID is the publisher id the site attests for the seat: the
	// account its revenue settles to.
	ResourceOwnerID string
	// ContributorHost is the operator's catalog contributor. Empty leaves the
	// catalog contributors out, which is a document the Exchange refuses to
	// activate a listing on.
	ContributorHost string
	// SupportedProfiles are the extension profiles the Exchange advertises.
	SupportedProfiles []string
}

// BuildPublisher builds and validates the publisher document p describes,
// through Build, and returns its protojson bytes.
func BuildPublisher(p Publisher) ([]byte, error) {
	ext, err := structpb.NewStruct(map[string]any{"resource_owner_id": p.ResourceOwnerID})
	if err != nil {
		return nil, fmt.Errorf("forawellknown/server: build the payee field: %w", err)
	}
	cfg := Config{
		Role:   forawellknown.RolePublisher,
		Domain: p.Domain,
		Exchanges: []*forav1.AuthorizedExchange{{
			Domain:       p.ExchangeDomain,
			Endpoint:     p.ExchangeEndpoint,
			Relationship: forav1.ProviderRelationship_PROVIDER_RELATIONSHIP_DIRECT,
			Ext:          ext,
		}},
		SupportedProfiles: p.SupportedProfiles,
	}
	if p.ContributorHost != "" {
		cfg.CatalogContributors = []*forav1.CatalogContributor{{
			Domain:       p.ContributorHost,
			Relationship: OperatorRelationship,
		}}
	}
	return Build(cfg)
}
