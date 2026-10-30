package service

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// The drift of a stored row is the list of its settings that differ from what
// the boot fixture declares. Apply keeps the stored values and reports the list,
// so an operator sees a fixture that no longer describes the deployment.

// driftCheck is one setting compared: its name, and whether the stored value
// differs from the declared one.
type driftCheck struct {
	field   string
	differs bool
}

// driftOf names the settings that differ, in the order they are checked.
func driftOf(checks ...driftCheck) []string {
	var drift []string
	for _, c := range checks {
		if c.differs {
			drift = append(drift, c.field)
		}
	}
	return drift
}

func seatDrift(declared repo.NewSeat, stored repo.Tenant) []string {
	return driftOf(
		driftCheck{"ed25519_key_ref", stored.Ed25519KeyRef != declared.Ed25519KeyRef},
		driftCheck{"rsa_key_ref", stored.RSAKeyRef != declared.RSAKeyRef},
		driftCheck{"allow_broker_relay", stored.AllowBrokerRelay != declared.AllowBrokerRelay},
		driftCheck{
			"activate_new_agents_by_default",
			stored.ActivateNewAgentsByDefault != declared.ActivateNewAgentsByDefault,
		},
	)
}

func publisherDrift(declared repo.NewPublisher, stored repo.Publisher) []string {
	return driftOf(driftCheck{"display_name", stored.DisplayName != declared.DisplayName})
}

func siteDrift(declared repo.NewProvisionedSite, stored repo.PublisherDomain) []string {
	return driftOf(
		driftCheck{"signing_scheme", stored.SigningScheme != declared.SigningScheme},
		driftCheck{"cdn", stored.CDN != declared.CDN},
		driftCheck{"cloudfront_key_pair_id", stored.CloudFrontKeyPairID != declared.CloudFrontKeyPairID},
	)
}

// listingDrift reports a stored listing that is not ACTIVE as drift in its
// state: the fixture declares every listing ACTIVE and never activates one.
func listingDrift(declared repo.NewActiveListing, stored repo.PublisherDomainListing) []string {
	return driftOf(
		driftCheck{"state", stored.State != repo.ListingStateActive},
		driftCheck{"terms", !sameJSON(declared.Terms, stored.Terms)},
		driftCheck{"content_path_scope", !slices.Equal(declared.ContentPathScope, stored.ContentPathScope)},
	)
}

// sameJSON compares two JSON documents as values, so neither key order nor
// spacing counts as a difference: Postgres stores jsonb in its own form, not as
// the bytes written. A document that does not parse is compared as bytes.
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}
