package forawellknown_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

func publisherManifestJSON() []byte {
	m := testutil.Manifest(forawellknown.RolePublisher, "pub.example")
	m.Exchanges = []*forav1.AuthorizedExchange{
		testutil.PublisherExchange("x.example", "https://x.example/fora",
			forav1.ProviderRelationship_PROVIDER_RELATIONSHIP_DIRECT),
	}
	m.CatalogContributors = []*forav1.CatalogContributor{
		{Domain: "verifier.example", Relationship: "verifier"},
	}
	return testutil.MarshalManifest(m)
}

func newPublisherCache(clk clock.Clock) *forawellknown.Cache {
	return forawellknown.NewCache(forawellknown.CacheOptions{
		Client:     testutil.Client(),
		Clk:        clk,
		ExpectRole: forawellknown.RolePublisher,
	})
}

func TestCache_GetAndAuthorizes(t *testing.T) {
	t.Parallel()
	origin := testutil.NewOrigin(publisherManifestJSON())
	defer origin.Close()
	c := newPublisherCache(clock.NewDeterministic(anchor))

	m, err := c.Get(context.Background(), origin.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// agentid.FromDirectory is the rule the Exchange passes in production. The
	// package under test cannot import it — internal/agentid is built on this
	// package, so the dependency would close a cycle — but this file is an
	// external test package, which Go compiles separately and which may import
	// anything that imports forawellknown. Asserting under the production rule is
	// what makes self, listed and stranger say something about the deployment
	// rather than about string equality.
	if !forawellknown.AuthorizesContributor(m, "pub.example", agentid.FromDirectory) {
		t.Error("publisher domain should authorize itself")
	}
	if !forawellknown.AuthorizesContributor(m, "verifier.example", agentid.FromDirectory) {
		t.Error("listed contributor should be authorized")
	}
	if forawellknown.AuthorizesContributor(m, "stranger.example", agentid.FromDirectory) {
		t.Error("unlisted caller must not be authorized")
	}
}

func TestCache_RoleAssert(t *testing.T) {
	t.Parallel()
	// The negative-cache path (404 → ErrNoDocument), including stickiness and
	// expiry, is covered by cache_behaviors_test.go's
	// TestCache_NegativeCacheStickyThenExpires.
	origin := testutil.NewOrigin(testutil.MarshalManifest(
		testutil.Manifest(forawellknown.RoleAgent, "a.example"),
	))
	defer origin.Close()
	c := newPublisherCache(clock.NewDeterministic(anchor))
	if _, err := c.Get(context.Background(), origin.URL); !errors.Is(err, forawellknown.ErrRoleMismatch) {
		t.Fatalf("want ErrRoleMismatch, got %v", err)
	}
}

func TestCache_TTLServesFromCache(t *testing.T) {
	t.Parallel()
	origin := testutil.NewOrigin(publisherManifestJSON())
	defer origin.Close()
	clk := clock.NewDeterministic(anchor)
	c := newPublisherCache(clk)

	if _, err := c.Get(context.Background(), origin.URL); err != nil {
		t.Fatalf("first get: %v", err)
	}
	origin.SetManifestStatus(http.StatusInternalServerError)
	if _, err := c.Get(context.Background(), origin.URL); err != nil {
		t.Fatalf("cached get should not hit failing origin: %v", err)
	}
	clk.Advance(2 * time.Hour)
	if _, err := c.Get(context.Background(), origin.URL); !errors.Is(err, forawellknown.ErrFetch) {
		t.Fatalf("want ErrFetch after TTL expiry, got %v", err)
	}
}
