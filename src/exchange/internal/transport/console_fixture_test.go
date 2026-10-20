//go:build integration

package transport_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forahttpsig"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// consoleContributorHost is the operator's catalog contributor in this package's
// fixtures: a dedicated host, separate from the Exchange's own domain, whose Web
// Bot Auth directory signs every catalog push on this deployment.
const consoleContributorHost = "catalog-contributor.exchange.example"

// consoleFixture is the publisher-registration surface under test: the real
// service over a real database, mounted behind the real signature middleware,
// served over real HTTP, and called by a client that signs the way the console
// signs.
//
// The composition is exchangetest.Start, the same one the console's own suites
// run against, so the surface these tests prove is the surface the console
// tests talk to. The only thing the fixture controls is what a publisher's site
// serves as its manifest, because the alternative is a test that depends on a
// hostname on the public internet.
type consoleFixture struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	server   *httptest.Server
	client   *http.Client
	seatID   string
	audit    repo.AuditRepo
	manifest stubManifestSource

	// directory and consoleKey are the console's identity, kept so a second
	// Exchange over the same database can be signed for by the same console.
	directory  string
	consoleKey ed25519.PrivateKey
	// contributor is the catalog contributor this deployment's site documents
	// must authorize: consoleContributorHost, unless the fixture shares its
	// seat with a transaction harness whose own caller pushes the catalog.
	contributor string
	// exchange is the Exchange the fixture talks to.
	exchange *exchangetest.Exchange
}

// stubManifestSource is what each publisher site serves at its well-known path.
// It is exchangetest.Manifests under the lower-case names this package's tests
// already call; the fake itself, and the reason it is the one seam, are there.
type stubManifestSource struct{ *exchangetest.Manifests }

func (s stubManifestSource) serve(host string, m *forawellknown.Manifest) { s.Serve(host, m) }

// withdraw takes the document away again, which is how a test reaches the
// states that follow registration.
func (s stubManifestSource) withdraw(host string) { s.Withdraw(host) }

func (s stubManifestSource) fail(host string, err error) { s.Fail(host, err) }

// publisherManifest builds the document a correctly-configured site serves: it
// names this seat, attests the publisher id registered for that site, and
// authorizes the operator's catalog contributor. An empty contributor leaves
// that entry out.
func publisherManifest(domain, publisherID, contributor string) *forawellknown.Manifest {
	return exchangetest.PublisherManifest(harnessExchangeDomain, contributor, domain, publisherID)
}

// startConsoleFixture brings the registration surface up the way production
// does -- a seat in the database, the service over the production repositories,
// the signature middleware bound to ONE console directory -- and a signing
// client whose key that directory publishes.
func startConsoleFixture(t *testing.T) *consoleFixture {
	t.Helper()
	return startConsoleFixtureWith(t, func(*exchangetest.Options) {})
}

// startConsoleFixtureWith is startConsoleFixture over a deployment the test
// adjusts first: its denomination, or a seat its operator never seeded.
func startConsoleFixtureWith(t *testing.T, deployment func(*exchangetest.Options)) *consoleFixture {
	t.Helper()
	ctx := context.Background()
	opts := exchangetest.Options{ExchangeDomain: harnessExchangeDomain}
	deployment(&opts)
	return newConsoleBase(t, ctx, acquireTestDB(t, ctx)).exchangeAs(t, opts)
}

// newConsoleBase is one console over pool: its signing key, the directory that
// publishes that key, and the audit read. It talks to no Exchange yet; over
// and exchangeAs point it at one.
func newConsoleBase(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *consoleFixture {
	t.Helper()
	_, consolePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("console ed25519 gen: %v", err)
	}
	return &consoleFixture{
		ctx:        ctx,
		pool:       pool,
		audit:      repo.NewAuditRepo(sqlc.New(pool)),
		directory:  startConsoleDirectory(t, consolePriv),
		consoleKey: consolePriv,
	}
}

// exchangeAs serves another Exchange over this fixture's database, signed for
// by the same console, and returns a fixture that talks to it. A deployment
// that sells through two seats is two Exchanges over one database, and this is
// how a test reaches the second one. The directory, the contributor and the
// directory client are always the fixture's own.
func (f *consoleFixture) exchangeAs(t *testing.T, o exchangetest.Options) *consoleFixture {
	t.Helper()
	o.DirectoryURL = f.directory
	o.ContributorHost = consoleContributorHost
	o.DirectoryClient = directoryClient()
	ex := exchangetest.Start(t, f.pool, o)
	return f.over(t, ex, consoleContributorHost)
}

// secondSeatDomain is the domain of the second Exchange secondSeatFor serves.
const secondSeatDomain = "second-seat.exchange.example"

// secondSeatFor serves a second Exchange over this fixture's database and
// grants actor publisherID on its seat, so one payee can save and list the
// same site on both seats.
//
// The grant is a tier-2 arrange through the production grant port, because no
// request path grants an existing payee on another seat: RegisterPublisher
// grants a person only the payee it mints for them on that seat.
func (f *consoleFixture) secondSeatFor(t *testing.T, actor consoleActorBody, publisherID string) *consoleFixture {
	t.Helper()
	second := f.exchangeAs(t, exchangetest.Options{ExchangeDomain: secondSeatDomain})
	repotest.SeedGrant(t, second.ctx, second.pool, repo.PublisherGrant{
		TenantID:    second.seatID,
		PublisherID: publisherID,
		OIDCIssuer:  actor.OIDCIssuer,
		OIDCSubject: actor.OIDCSubject,
	})
	return second
}

// over returns a fixture that talks to ex, signed for by this fixture's
// console, whose sites must authorize contributor.
func (f *consoleFixture) over(t *testing.T, ex *exchangetest.Exchange, contributor string) *consoleFixture {
	t.Helper()
	return &consoleFixture{
		ctx:         f.ctx,
		pool:        f.pool,
		server:      ex.Server,
		client:      consoleSigningClient(t, ex.Server, f.directory, f.consoleKey),
		seatID:      ex.SeatID,
		audit:       f.audit,
		manifest:    stubManifestSource{ex.Manifests},
		directory:   f.directory,
		consoleKey:  f.consoleKey,
		contributor: contributor,
		exchange:    ex,
	}
}

// startConsoleDirectory serves a console's Web Bot Auth directory, which is
// where the Exchange learns the console's signing key.
func startConsoleDirectory(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	return exchangetest.StartDirectory(t, priv)
}

// directoryClient is the HTTP client the resolver fetches the console's
// directory with. The production client is SSRF-guarded and would refuse the
// loopback address httptest binds, so the fixture injects one that trusts the
// httptest TLS certificate -- the guard is exercised where it belongs, in the
// guard's own tests, not by arranging for this fixture to be refused.
func directoryClient() *http.Client { return testutil.HTTPTestTLSClient(5 * time.Second) }

// consoleSigningClient builds the client the console uses: every request to the
// registration prefix is signed under the FORA covered-component set, naming
// the console's own directory as its Signature-Agent, over the console's clock
// window. Two identical bodies signed in the same wall-clock second still get
// different signatures, because every signature carries a fresh nonce.
func consoleSigningClient(
	t *testing.T, server *httptest.Server, directory string, priv ed25519.PrivateKey,
) *http.Client {
	t.Helper()
	base := server.Client().Transport
	if base == nil {
		base = http.DefaultTransport
	}
	rt, err := forahttpsig.New(base, directory, priv, core.ClockWindow(clock.System{}.Now, consoleSignatureTTL),
		forahttpsig.WithFORATargets(func(r *http.Request) bool {
			return strings.HasPrefix(r.URL.Path, transport.ConsolePathPrefix)
		}))
	if err != nil {
		t.Fatalf("console signing transport: %v", err)
	}
	return &http.Client{Transport: rt, Timeout: 10 * time.Second}
}

// consoleSignatureTTL is the signature lifetime the console signs with.
const consoleSignatureTTL = 2 * time.Minute

// imposterClient builds a caller that signs correctly with a key its OWN
// directory publishes. It is what the fixed-directory rule has to refuse: the
// signature verifies against that directory, so a resolver following the
// request's Signature-Agent header would admit it and let the caller assert any
// actor it liked.
func (f *consoleFixture) imposterClient(t *testing.T) *http.Client {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("imposter ed25519 gen: %v", err)
	}
	return consoleSigningClient(t, f.server, startConsoleDirectory(t, priv), priv)
}

// chargingTerms is the licensing most of these tests register with: charge per
// access, two uses permitted, attribution required. It is a function rather
// than a package-level map because a shared map a test mutated would change
// what every other test registered.
func chargingTerms() map[string]any {
	return map[string]any{
		"pricing_model":        "per_unit",
		"rate":                 "0.02",
		"permitted_functions":  []string{"search", "ai-input"},
		"attribution_required": true,
	}
}

// consoleActorBody is the actor every fixture request asserts: the shared wire
// shape, under the name this package's tests already use.
type consoleActorBody = consoleapi.Actor

// newActor builds a distinct verified person. Subjects are unique per call so
// two actors in one test cannot share a grant by accident.
func newActor(email string) consoleActorBody {
	return consoleActorBody{
		OIDCIssuer:    "https://login.example",
		OIDCSubject:   uuid.NewString(),
		Email:         email,
		EmailVerified: true,
	}
}

// post sends one signed registration request and returns the status and body.
// Tests read both: a refusal is a status plus a reason, and asserting only the
// status would pass on a refusal that named the wrong thing.
func (f *consoleFixture) post(t *testing.T, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, f.server.URL+path, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode %s response: %v", path, err)
	}
	return resp.StatusCode, decoded
}

// registerPublisher drives the registration route and returns the minted id.
func (f *consoleFixture) registerPublisher(t *testing.T, actor consoleActorBody, name string) string {
	t.Helper()
	status, body := f.post(t, consolePath(transport.ConsoleRegisterPublisherPath), map[string]any{
		"actor": actor, "display_name": name,
	})
	if status != http.StatusOK {
		t.Fatalf("RegisterPublisher status = %d, body = %v", status, body)
	}
	id, _ := body["publisher_id"].(string)
	if id == "" {
		t.Fatalf("RegisterPublisher returned no publisher_id: %v", body)
	}
	return id
}

// consolePath strips the method prefix off a route pattern so the constant the
// server mounts is the constant the client calls.
func consolePath(pattern string) string {
	return strings.TrimPrefix(pattern, "POST ")
}
