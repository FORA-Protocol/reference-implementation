package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/bootfixture"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
)

// Planning a boot fixture is pure: it checks the listings' terms and path scopes
// by registration's own rules and shapes the rows Apply writes. Apply itself is
// driven end to end through the transport package's integration tests.

var planDefaults = termspreset.Defaults{Currency: "EUR"}

// fixtureWith is a checked fixture with one seat and one listed site, whose
// listing carries terms and scope. The site is on the CloudFront RSA scheme, so
// the seat names an RSA key reference, as bootfixture.Parse requires.
func fixtureWith(terms string, scope ...string) bootfixture.Fixture {
	return bootfixture.Fixture{
		Path: "/boot/fixture.yml", SHA256: "abc",
		Seats: []bootfixture.Seat{{
			TenantID: "seat", Domain: "exchange.example", Ed25519KeyRef: "k", RSAKeyRef: "cf-rsa-primary",
			AllowBrokerRelay: true, ActivateNewAgentsByDefault: true,
		}},
		Publishers: []bootfixture.Publisher{{
			ID: "pub-1", DisplayName: "Stoa Press",
			Sites: []bootfixture.Site{{
				Domain: "media.publisher.example", SigningScheme: signing.SchemeCFRSA, CloudFrontKeyPairID: "KEXAMPLE",
				Listings: []bootfixture.Listing{{
					TenantID: "seat", TermsFile: "terms.json", Terms: []byte(terms), ContentPathScope: scope,
				}},
			}},
		}},
	}
}

func TestPlanBootFixtureShapesTheRowsApplyCreates(t *testing.T) {
	plan, err := PlanBootFixture(fixtureWith(
		`{"pricing_model":"free","permitted_functions":["search"," search ","crawl"],"attribution_required":false}`,
		" /articles/ ", "",
	), planDefaults)
	if err != nil {
		t.Fatalf("PlanBootFixture: %v", err)
	}
	want := BootPlan{
		Path: "/boot/fixture.yml", SHA256: "abc",
		Seats: []repo.NewSeat{{
			TenantID: "seat", Domain: "exchange.example", Ed25519KeyRef: "k", RSAKeyRef: "cf-rsa-primary",
			AllowBrokerRelay: true, ActivateNewAgentsByDefault: true,
		}},
		Publishers: []PlannedPublisher{{
			Publisher: repo.NewPublisher{ID: "pub-1", DisplayName: "Stoa Press"},
			Sites: []PlannedSite{{
				Site: repo.NewProvisionedSite{
					Domain: "media.publisher.example", PublisherID: "pub-1",
					SigningScheme: "AWS_CLOUDFRONT_RSA", CloudFrontKeyPairID: "KEXAMPLE",
				},
				Listings: []repo.NewActiveListing{{
					TenantID: "seat", Domain: "media.publisher.example", ContentPathScope: []string{"/articles/"},
					Terms: []byte(`{"pricing_model":"free","permitted_functions":["search","crawl"],` +
						`"attribution_required":false}`),
				}},
			}},
		}},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan = %+v\nwant %+v", plan, want)
	}
}

func TestPlanBootFixtureRefusesTermsOrAScopeRegistrationWouldRefuse(t *testing.T) {
	const free = `{"pricing_model":"free","permitted_functions":["search"],"attribution_required":false}`
	cases := map[string]struct {
		fixture bootfixture.Fixture
		names   string
	}{
		"an unknown terms member": {
			fixtureWith(`{"pricing_model":"free","permitted_functions":["search"],"discount":"10%"}`), "terms_file",
		},
		"no permitted use":           {fixtureWith(`{"pricing_model":"free","permitted_functions":[]}`), "terms_file"},
		"an unknown pricing model":   {fixtureWith(`{"pricing_model":"auction","permitted_functions":["search"]}`), "terms_file"},
		"a second JSON value":        {fixtureWith(free + `{}`), "terms_file"},
		"a scope entry without a /":  {fixtureWith(free, "articles/"), "content_path_scope"},
		"a per-unit price with none": {fixtureWith(`{"pricing_model":"per_unit","permitted_functions":["search"]}`), "terms_file"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := PlanBootFixture(tc.fixture, planDefaults)
			var exErr *exchange.Error
			if !errors.As(err, &exErr) || exErr.Kind != exchange.KindInvalidRequest {
				t.Fatalf("err = %v, want an InvalidRequest", err)
			}
			if !strings.Contains(err.Error(), tc.names) || !strings.Contains(err.Error(), "media.publisher.example") {
				t.Fatalf("err = %q, want it to name the site and %s", err, tc.names)
			}
		})
	}
}

func TestDriftNamesEachStoredSettingThatDiffers(t *testing.T) {
	seat := repo.NewSeat{TenantID: "s", Domain: "d", Ed25519KeyRef: "k", AllowBrokerRelay: true}
	stored := repo.Tenant{ID: "s", Domain: "d", Ed25519KeyRef: "k", ActivateNewAgentsByDefault: true}
	if got, want := seatDrift(seat, stored), []string{"allow_broker_relay", "activate_new_agents_by_default"}; !reflect.DeepEqual(got, want) {
		t.Errorf("seatDrift = %v, want %v", got, want)
	}
	if got := seatDrift(seat, repo.Tenant{ID: "s", Domain: "d", Ed25519KeyRef: "k", AllowBrokerRelay: true}); got != nil {
		t.Errorf("seatDrift of an identical seat = %v, want none", got)
	}

	site := repo.NewProvisionedSite{Domain: "x.example", PublisherID: "p", SigningScheme: "ED25519", CDN: "fastly"}
	if got := siteDrift(site, repo.PublisherDomain{Domain: "x.example", PublisherID: "p", SigningScheme: "ED25519"}); !reflect.DeepEqual(got, []string{"cdn"}) {
		t.Errorf("siteDrift = %v, want [cdn]", got)
	}

	listing := repo.NewActiveListing{
		TenantID: "s", Domain: "x.example", ContentPathScope: []string{},
		Terms: []byte(`{"pricing_model":"free","permitted_functions":["search"]}`),
	}
	sameTermsOtherOrder := repo.PublisherDomainListing{
		State: repo.ListingStateActive, ContentPathScope: []string{},
		Terms: []byte(`{"permitted_functions": ["search"], "pricing_model": "free"}`),
	}
	if got := listingDrift(listing, sameTermsOtherOrder); got != nil {
		t.Errorf("listingDrift for the same terms in another key order = %v, want none", got)
	}
	pending := sameTermsOtherOrder
	pending.State, pending.ContentPathScope = repo.ListingStatePendingManifest, []string{"/a/"}
	if got, want := listingDrift(listing, pending), []string{"state", "content_path_scope"}; !reflect.DeepEqual(got, want) {
		t.Errorf("listingDrift of a pending, narrower listing = %v, want %v", got, want)
	}
}
