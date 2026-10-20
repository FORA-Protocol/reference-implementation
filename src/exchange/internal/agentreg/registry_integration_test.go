//go:build integration

package agentreg_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentid/agentidtest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/agentreg"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

// fixtureKey describes one Ed25519 key to publish in the unified AGENT
// manifest, with a half-open [validFrom, validUntil) validity window.
type fixtureKey struct {
	kid        string
	pub        ed25519.PublicKey
	validFrom  time.Time
	validUntil time.Time
}

type fixtureOrigin struct {
	server  *httptest.Server
	mu      sync.Mutex
	agentID string
	keys    []fixtureKey
	hits    int64 // WBA-directory fetches served (guarded by mu)
}

func newFixtureOrigin(t *testing.T, agentID string, keys []fixtureKey) *fixtureOrigin {
	t.Helper()
	o := &fixtureOrigin{agentID: agentID, keys: keys}
	o.server = httptest.NewServer(http.HandlerFunc(o.handle))
	t.Cleanup(o.server.Close)
	return o
}

func (o *fixtureOrigin) handle(w http.ResponseWriter, r *http.Request) {
	// After the WBA split the registry discovers a caller's key from its WBA
	// directory (the WBA file carries only keys — no role, no domain).
	if r.URL.Path != forawellknown.WBAPath {
		http.NotFound(w, r)
		return
	}
	o.mu.Lock()
	o.hits++
	keys := append([]fixtureKey(nil), o.keys...)
	o.mu.Unlock()

	// Served as every key directory is: the directory media type and a response
	// signature by each listed key, covering the authority the request named —
	// the agent's own host, which the rewriter keeps in the Host header.
	body := agentWBAJSON(keys)
	signers := make([]helpers.Signer, 0, len(keys))
	for _, k := range keys {
		if priv, ok := privateHalves.Load(string(k.pub)); ok {
			signers = append(signers, testutil.DirectorySigner(priv.(ed25519.PrivateKey)))
		}
	}
	if len(signers) > 0 {
		sig, err := helpers.SignDirectoryResponse(r.Context(), strings.ToLower(r.Host), body, signers,
			1_000_000_000, 4_000_000_000)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sig.Apply(w.Header())
	}
	w.Header().Set("Content-Type", forawellknown.WBAMediaType)
	_, _ = w.Write(body)
}

// privateHalves maps each generated public key to its private half, so the
// fixture origin can sign its directory response with the keys it lists.
var privateHalves sync.Map

// hitCount returns how many WBA-directory fetches the origin has served, so a
// debounce test can assert a burst of refreshes coalesced into a single fetch.
func (o *fixtureOrigin) hitCount() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.hits
}

func (o *fixtureOrigin) setKeys(agentID string, keys []fixtureKey) {
	o.mu.Lock()
	o.agentID = agentID
	o.keys = keys
	o.mu.Unlock()
}

func mustEd25519(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 gen: %v", err)
	}
	privateHalves.Store(string(pub), priv)
	return pub
}

// hostRewriter routes requests whose URL host is a registered agent identity to
// the test origin serving that agent's manifest. Production fetches the manifest
// from agent_id's own host (ADR-009 D3/D4); the rewriter lets the test bind a
// logical agent_id to an ephemeral httptest server without DNS, mirroring the
// transport harness's registerHost pattern.
type hostRewriter struct {
	mu      sync.Mutex
	targets map[string]string // agent host → "http://127.0.0.1:port"
}

func newHostRewriter() *hostRewriter { return &hostRewriter{targets: map[string]string{}} }

func (h *hostRewriter) set(host, target string) {
	h.mu.Lock()
	h.targets[host] = target
	h.mu.Unlock()
}

func (h *hostRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	h.mu.Lock()
	target, ok := h.targets[req.URL.Host]
	h.mu.Unlock()
	if ok {
		t, err := url.Parse(target)
		if err != nil {
			return nil, err
		}
		req = req.Clone(req.Context())
		req.URL.Scheme = t.Scheme
		req.URL.Host = t.Host
	}
	return http.DefaultTransport.RoundTrip(req)
}

func newTestQueries(t *testing.T) *sqlc.Queries {
	t.Helper()
	return sqlc.New(acquireTestDB(t, context.Background()))
}

// setupRegistry builds the registry over a fresh database and returns it beside
// the raw queries the presence assertions still need.
//
// The queries are handed back rather than the repository because two of the
// assertions in this package cannot be made through it. repo.Agent carries no
// RegisteredAt, which the upsert-idempotency test compares across three reads,
// and agentFromRow puts DiscoveryUrl through textOrEmpty, which collapses SQL
// NULL to the empty string so "the column was null" stops being distinguishable.
// Both are gaps in the read surface rather than facts about these tests; widening
// repo.Agent is a production change and does not belong to the branch that
// noticed. Everything that asserts a row is ABSENT goes through requireNoAgentRow
// below, which does use the repository.
func setupRegistry(t *testing.T, clock agentreg.Clock) (agentreg.Registry, *sqlc.Queries, *hostRewriter) {
	t.Helper()
	return setupRegistryWith(t, clock, 0)
}

// requireNoAgentRow asserts, through the repository production writes with, that
// the registration left no row behind.
//
// Through the repository and not the raw query, because the two ask different
// questions. AgentRepo.ByID reduces the id through agentKey before it reaches
// SQL -- the canonicalisation the repo owns precisely so no caller has to
// remember it -- and answers ErrAgentNotFound rather than whatever error came
// back. "No row under the key production would have written" is the claim these
// tests mean to make; "the raw query returned some error" is weaker and is keyed
// differently from the read production performs.
func requireNoAgentRow(t *testing.T, ctx context.Context, q *sqlc.Queries, agentID, why string) {
	t.Helper()
	if _, err := repo.NewAgentRepo(q).ByID(ctx, agentID); !errors.Is(err, repo.ErrAgentNotFound) {
		t.Fatalf("a row persists under %q although %s: ByID = %v, want ErrAgentNotFound",
			agentID, why, err)
	}
}

// TestRegistry_FetchesTheDirectoryOverHTTPSOnly pins that Gate-1 self-signup
// reads a key directory over https and nothing else. The agent's directory is
// served only over plaintext, at the agent's own host:port, and the registry is
// given a plain client with no rewriter: its fetch goes to https at that
// authority, never reaches the plaintext handler, and registers nothing. Before
// the Web Bot Auth profile a deployment scheme setting could move this fetch to
// http; no setting can now.
func TestRegistry_FetchesTheDirectoryOverHTTPSOnly(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	q := newTestQueries(t)
	ctx := t.Context()

	pub := mustEd25519(t)
	origin := newFixtureOrigin(t, "placeholder", nil)
	originURL, err := url.Parse(origin.server.URL)
	if err != nil {
		t.Fatalf("parse origin url: %v", err)
	}
	agentID := originURL.Host // 127.0.0.1:PORT, served over plaintext only
	origin.setKeys(agentID, []fixtureKey{{
		kid: "k1", pub: pub,
		validFrom:  now.Add(-time.Hour),
		validUntil: now.Add(24 * time.Hour),
	}})

	reg := agentreg.New(agentreg.Config{
		Repo:  repo.NewAgentRepo(q),
		HTTP:  &http.Client{},
		Clock: &fixedClock{now: now},
	})
	err = reg.RegisterFromDirectory(ctx, agentID, agentID)
	if !errors.Is(err, resolvers.ErrDirectoryUnavailable) || agentreg.IsCallerFault(err) {
		t.Fatalf("a plaintext-only directory: want a transient read failure, got %v", err)
	}
	if hits := origin.hitCount(); hits != 0 {
		t.Fatalf("the plaintext directory was read %d times; a key directory is fetched over https only", hits)
	}
	requireNoAgentRow(t, ctx, q, agentID, "the directory is not served over https")
}

func TestRegistry_HappyPath(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reg, q, rw := setupRegistry(t, &fixedClock{now: now})
	ctx := t.Context()

	pub := mustEd25519(t)
	agentID := "agent.fixture.test"
	origin := newFixtureOrigin(t, agentID, []fixtureKey{{
		kid: "k1", pub: pub,
		validFrom:  now.Add(-time.Hour),
		validUntil: now.Add(24 * time.Hour),
	}})
	rw.set(agentID, origin.server.URL)

	if err := reg.RegisterFromDirectory(ctx, agentID, agentID); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := reg.LookupPublicKey(ctx, agentID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !pub.Equal(got) {
		t.Fatalf("lookup key mismatch")
	}

	row, err := q.GetAgent(ctx, agentID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	// The stored discovery_url is the identity-anchored discovery URL derived from
	// agent_id, not whatever the caller passed.
	const wantURL = "https://agent.fixture.test/.well-known/http-message-signatures-directory"
	if !row.DiscoveryUrl.Valid || row.DiscoveryUrl.String != wantURL {
		t.Fatalf("discovery_url = %+v, want %q", row.DiscoveryUrl, wantURL)
	}
}

// TestRegistry_DiscoveryURLNamingNoHostRefused covers the arm of the anchoring
// check that the rest of the suite cannot reach. Every other call here passes
// either identical arguments — which makes the agent_id arm fire first — or a
// discovery_url that is a perfectly good host on the wrong domain, which is a
// mismatch rather than a non-host.
//
// Without it, the discovery_url arm is proven only by a handler test that feeds
// the sentinel to a stub registry: that shows the handler renders the sentinel,
// not that the registry produces it. Revert this arm to ErrMalformedDirectory and
// the whole suite still passes, while the endpoint goes back to blaming a
// manifest that was never fetched — the wrong diagnosis the split exists to end.
func TestRegistry_DiscoveryURLNamingNoHostRefused(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reg, q, _ := setupRegistry(t, &fixedClock{now: now})
	ctx := t.Context()

	const agentID = "agent.fixture.test"
	for _, badURL := range []string{
		`agent2="https://agent.fixture.test"`, // the sf-dictionary form
		"//agent.fixture.test",                // authority parses empty
		"https://",                            // scheme, no host
	} {
		t.Run(badURL, func(t *testing.T) {
			err := reg.RegisterFromDirectory(ctx, agentID, badURL)
			if !errors.Is(err, agentreg.ErrNotAHost) {
				t.Fatalf("RegisterFromDirectory(%q, %q) = %v; want ErrNotAHost — nothing was "+
					"fetched, so blaming the manifest would send the caller to inspect a "+
					"document that was never retrieved", agentID, badURL, err)
			}
			if errors.Is(err, agentreg.ErrMalformedDirectory) {
				t.Errorf("refusal also matches ErrMalformedDirectory; the split is what "+
					"gives this fault its own diagnosis (err=%v)", err)
			}
			requireNoAgentRow(t, ctx, q, agentID, "the registration was refused")
		})
	}
}

func TestRegistry_DiscoveryURLNotAnchored(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reg, q, rw := setupRegistry(t, &fixedClock{now: now})
	ctx := t.Context()

	// An attacker-controlled origin self-asserts domain == victim agent_id.
	pub := mustEd25519(t)
	const victimID = "agent.fixture.test"
	origin := newFixtureOrigin(t, victimID, []fixtureKey{{
		kid: "k1", pub: pub,
		validFrom:  now.Add(-time.Hour),
		validUntil: now.Add(24 * time.Hour),
	}})
	rw.set("attacker.example", origin.server.URL)

	// Registering victimID against a discovery_url hosted by attacker.example must
	// be refused before any fetch — discovery is anchored to agent_id (ADR-009).
	err := reg.RegisterFromDirectory(ctx, victimID, "https://attacker.example/.well-known/fora.json")
	if !errors.Is(err, agentreg.ErrAgentIDMismatch) {
		t.Fatalf("want ErrAgentIDMismatch for unanchored discovery_url, got %v", err)
	}
	requireNoAgentRow(t, ctx, q, victimID, "the discovery host is not anchored to it")
}

// NOTE: the former TestRegistry_AgentIDMismatch is intentionally removed. After
// the WBA split the directory carries only keys — no self-asserted domain — so a
// "manifest body claims a different domain" mismatch cannot occur. The fetch
// LOCATION is the sole anchor: RegisterFromDirectory fetches the WBA directory
// from agent_id's own well-known path (guarded by requireAnchoredHost, covered by
// TestRegistry_DiscoveryURLNotAnchored), and pins whatever currently-valid key it
// publishes there.

func TestRegistry_MultipleKeysPicksCurrent(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reg, _, rw := setupRegistry(t, &fixedClock{now: now})
	ctx := t.Context()

	oldPub := mustEd25519(t)
	currentPub := mustEd25519(t)
	futurePub := mustEd25519(t)
	agentID := "agent.rotate.test"

	origin := newFixtureOrigin(t, agentID, []fixtureKey{
		{
			kid: "old", pub: oldPub,
			validFrom:  now.Add(-72 * time.Hour),
			validUntil: now.Add(-1 * time.Hour),
		},
		{
			kid: "current", pub: currentPub,
			validFrom:  now.Add(-1 * time.Hour),
			validUntil: now.Add(24 * time.Hour),
		},
		{
			kid: "future", pub: futurePub,
			validFrom:  now.Add(24 * time.Hour),
			validUntil: now.Add(48 * time.Hour),
		},
	})
	rw.set(agentID, origin.server.URL)

	if err := reg.RegisterFromDirectory(ctx, agentID, agentID); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := reg.LookupPublicKey(ctx, agentID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !currentPub.Equal(got) {
		t.Fatalf("wrong key selected; want current key")
	}
}

// TestRegistry_RefreshDirectoryKey_RepinsRotatedKey proves the fix for the
// key-rotation lockout: once an agent is registered, RefreshDirectoryKey
// re-fetches its WBA directory and re-pins the currently-valid key, so a caller
// that rotated its signing key is recognized without operator DB surgery.
func TestRegistry_RefreshDirectoryKey_RepinsRotatedKey(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reg, _, rw := setupRegistry(t, &fixedClock{now: now})
	ctx := t.Context()

	keyA := mustEd25519(t)
	keyB := mustEd25519(t)
	agentID := "agent.repin.test"
	origin := newFixtureOrigin(t, agentID, []fixtureKey{{
		kid: "a", pub: keyA,
		validFrom:  now.Add(-time.Hour),
		validUntil: now.Add(24 * time.Hour),
	}})
	rw.set(agentID, origin.server.URL)

	if err := reg.RegisterFromDirectory(ctx, agentID, agentID); err != nil {
		t.Fatalf("initial register: %v", err)
	}
	if got, _ := reg.LookupPublicKey(ctx, agentID); !keyA.Equal(got) {
		t.Fatal("keyA must be pinned after initial registration")
	}

	// The agent rotates: its directory now publishes keyB instead of keyA.
	origin.setKeys(agentID, []fixtureKey{{
		kid: "b", pub: keyB,
		validFrom:  now.Add(-time.Hour),
		validUntil: now.Add(24 * time.Hour),
	}})

	if err := reg.RefreshDirectoryKey(ctx, agentID); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got, _ := reg.LookupPublicKey(ctx, agentID); !keyB.Equal(got) {
		t.Fatal("RefreshDirectoryKey must re-pin the rotated key keyB")
	}
}

// TestRegistry_RefreshDirectoryKey_Debounced proves the re-fetch is bounded: a
// burst of refreshes for one directory within the debounce window coalesces into
// a single WBA-directory fetch, so a stream of key-mismatch requests cannot
// amplify into a fetch storm against the directory host. The clock is fixed, so
// every refresh falls inside the one-minute window.
//
// The burst deliberately uses a DIFFERENT SPELLING of the one directory each
// time. The window is documented as one fetch per window per directory, and a
// debounce keyed on the caller's text would make it one fetch per window per
// SPELLING instead — one victim host, as many fetch budgets as the caller cares
// to invent, which is no bound at all. One of the two production callers passes
// an unverified caller_id straight off a catalog push, so the spellings really
// are caller-chosen. Every entry below is the same directory to DNS.
func TestRegistry_RefreshDirectoryKey_Debounced(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	q := newTestQueries(t)
	rw := newHostRewriter()
	reg := agentreg.New(agentreg.Config{
		Repo:            repo.NewAgentRepo(q),
		HTTP:            &http.Client{Transport: rw},
		Clock:           &fixedClock{now: now},
		RefreshDebounce: time.Minute,
	})
	ctx := t.Context()

	keyA := mustEd25519(t)
	agentID := "agent.debounce.test"
	origin := newFixtureOrigin(t, agentID, []fixtureKey{{
		kid: "a", pub: keyA,
		validFrom:  now.Add(-time.Hour),
		validUntil: now.Add(24 * time.Hour),
	}})
	rw.set(agentID, origin.server.URL)

	if err := reg.RegisterFromDirectory(ctx, agentID, agentID); err != nil {
		t.Fatalf("register: %v", err)
	}
	base := origin.hitCount()

	spellings := []string{
		agentID,
		"https://" + agentID,          // scheme
		"HTTPS://Agent.Debounce.Test", // scheme and case
		agentID + ".",                 // trailing root dot
		"https://" + agentID + ":443", // the port https already implies
		"https://" + agentID + ":0443",
	}
	for _, spelling := range spellings {
		if err := reg.RefreshDirectoryKey(ctx, spelling); err != nil {
			t.Fatalf("refresh %q: %v", spelling, err)
		}
	}
	if fetched := origin.hitCount() - base; fetched != 1 {
		t.Fatalf("debounce: want exactly 1 directory fetch for %d refreshes of one directory, got %d — "+
			"the anti-amplification bound is per-directory, not per-spelling", len(spellings), fetched)
	}
}

// TestRegistry_StoresCanonicalIdentity pins what the agents table is keyed on. A
// caller spells its own identity however it likes — a signed Signature-Agent
// header on one path, an unverified caller_id on another — and keying the column
// on that text gave one host a row per spelling, each with its own TOFU key pin
// and, downstream, its own billing ref. Registration must therefore store the
// canonical host, not what the caller typed.
//
// The spelling used here folds on every axis at once, because the axes were closed
// one at a time and the bug stayed reachable through whichever remained.
func TestRegistry_StoresCanonicalIdentity(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reg, q, rw := setupRegistry(t, &fixedClock{now: now})
	ctx := t.Context()

	pub := mustEd25519(t)
	const canonical = "agent.spelling.test"
	const asSpelled = "https://Agent.Spelling.Test:0443"
	origin := newFixtureOrigin(t, canonical, []fixtureKey{{
		kid: "k1", pub: pub,
		validFrom:  now.Add(-time.Hour),
		validUntil: now.Add(24 * time.Hour),
	}})
	// Only the canonical host is routed to the origin, so a fetch URL rebuilt from
	// the caller's spelling could not have been served at all.
	rw.set(canonical, origin.server.URL)

	if err := reg.RegisterFromDirectory(ctx, asSpelled, asSpelled); err != nil {
		t.Fatalf("register %q: %v", asSpelled, err)
	}

	// Read back through the production repository interface, not raw sqlc — the
	// documented tier-2 fallback, because the Exchange exposes no RPC that lists or
	// reads an agent row. The assertion is on the returned Agent.ID, which is the
	// COLUMN VALUE the row came back with rather than an echo of the argument;
	// neither this surface nor the package's LookupPublicKey can be asked "is there
	// a row under this exact string", since both canonicalize their own input by
	// design (repo.AgentRepo owns that invariant now).
	agents := repo.NewAgentRepo(q)
	stored, err := agents.ByID(ctx, canonical)
	if err != nil {
		t.Fatalf("no row under the canonical host %q: %v", canonical, err)
	}
	if stored.ID != canonical {
		t.Fatalf("stored agent_id = %q; want %q — a row keyed on the caller's spelling is a "+
			"second key pin and a second billing ref", stored.ID, canonical)
	}
	if origin.hitCount() == 0 {
		t.Fatal("the directory was never fetched from the canonical host")
	}

	// Every spelling of the one host resolves to the one registration.
	for _, spelling := range []string{
		canonical,
		asSpelled,
		"AGENT.SPELLING.TEST",
		"http://agent.spelling.test",
		"https://agent.spelling.test.",
		"agent.spelling.test:443",
	} {
		got, err := reg.LookupPublicKey(ctx, spelling)
		if err != nil {
			t.Fatalf("LookupPublicKey(%q): %v — this spelling names the same agent", spelling, err)
		}
		if !pub.Equal(got) {
			t.Errorf("LookupPublicKey(%q) returned a different key; one host must be one registration", spelling)
		}
	}
}

// TestRegistry_UnregistrableIdentityRefused is the negative path for the above: a
// value that names no host must not become a storage key or a fetch target. It is
// reported as ErrNotAHost — its own sentinel precisely so it is not confused with
// ErrAgentIDMismatch, which means two hosts disagreed after a fetch that did
// happen. IsCallerFault classifies it as a permanent caller fault, so every call
// site maps it to the unauthenticated answer it already gives an unregistrable
// identity.
func TestRegistry_UnregistrableIdentityRefused(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reg, q, _ := setupRegistry(t, &fixedClock{now: now})
	ctx := t.Context()

	// The shared corpus (internal/agentid/agentidtest): one list, so this layer and
	// the rule that produces the refusal cannot drift apart.
	for _, tc := range agentidtest.NonHostValues() {
		bad := tc.Value
		t.Run(tc.Name, func(t *testing.T) {
			err := reg.RefreshDirectoryKey(ctx, bad)
			if !errors.Is(err, agentreg.ErrNotAHost) {
				t.Fatalf("RefreshDirectoryKey(%q) = %v; want ErrNotAHost", bad, err)
			}
			if !agentreg.IsCallerFault(err) {
				t.Errorf("RefreshDirectoryKey(%q) is not classified a caller fault, so the "+
					"call sites would answer it as a retryable outage", bad)
			}
			// The repository refuses the value outright, which is a stronger
			// statement than "no row matched": the only writer of this column
			// cannot be made to key a row on it.
			if _, err := repo.NewAgentRepo(q).ByID(ctx, bad); !errors.Is(err, repo.ErrAgentIDNotAHost) {
				t.Errorf("the agents repo accepted %q as a key (err=%v)", bad, err)
			}
		})
	}
}

func TestRegistry_UpsertIdempotentAndKeyReplace(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reg, q, rw := setupRegistry(t, &fixedClock{now: now})
	ctx := t.Context()

	pubV1 := mustEd25519(t)
	agentID := "agent.idem.test"
	origin := newFixtureOrigin(t, agentID, []fixtureKey{{
		kid: "v1", pub: pubV1,
		validFrom:  now.Add(-time.Hour),
		validUntil: now.Add(24 * time.Hour),
	}})
	rw.set(agentID, origin.server.URL)

	if err := reg.RegisterFromDirectory(ctx, agentID, agentID); err != nil {
		t.Fatalf("register1: %v", err)
	}
	row1, err := q.GetAgent(ctx, agentID)
	if err != nil {
		t.Fatalf("GetAgent1: %v", err)
	}

	if err := reg.RegisterFromDirectory(ctx, agentID, agentID); err != nil {
		t.Fatalf("register2: %v", err)
	}
	row2, err := q.GetAgent(ctx, agentID)
	if err != nil {
		t.Fatalf("GetAgent2: %v", err)
	}
	if !bytesEqual(row1.PublicKey, row2.PublicKey) {
		t.Fatalf("idempotent upsert mutated key bytes")
	}
	if !row1.RegisteredAt.Time.Equal(row2.RegisteredAt.Time) {
		t.Fatalf("registered_at changed across idempotent upserts: %v -> %v",
			row1.RegisteredAt.Time, row2.RegisteredAt.Time)
	}

	pubV2 := mustEd25519(t)
	origin.setKeys(agentID, []fixtureKey{{
		kid: "v2", pub: pubV2,
		validFrom:  now.Add(-time.Hour),
		validUntil: now.Add(24 * time.Hour),
	}})
	if err := reg.RegisterFromDirectory(ctx, agentID, agentID); err != nil {
		t.Fatalf("register3: %v", err)
	}
	row3, err := q.GetAgent(ctx, agentID)
	if err != nil {
		t.Fatalf("GetAgent3: %v", err)
	}
	if bytesEqual(row3.PublicKey, row1.PublicKey) {
		t.Fatalf("expected new kid to replace stored key bytes")
	}
	gotPub, err := reg.LookupPublicKey(ctx, agentID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !pubV2.Equal(gotPub) {
		t.Fatalf("lookup did not return replacement key")
	}
}

func TestRegistry_LookupUnknown(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reg, _, _ := setupRegistry(t, &fixedClock{now: now})

	_, err := reg.LookupPublicKey(t.Context(), "agent.nobody.test")
	if !errors.Is(err, agentreg.ErrUnknown) {
		t.Fatalf("want ErrUnknown, got %v", err)
	}
}

// TestRegistry_GuardedClientRefusesLoopback pins that the injected SDK-guarded
// client refuses a caller-steered internal target. agents/register is an
// unauthenticated, caller-controlled (agent_id + discovery_url) fetch path, so
// the composition root injects the SDK-owned guarded client
// (resolvers.NewGuardedClientFromEnv) — never a fail-open http.DefaultClient.
//
// The guard refuses to dial a non-public address at connect time, so pointing a
// registration at a loopback origin fails the read, transiently, and persists no
// agent row. With an unguarded client the same dial would instead
// reach the origin, so this assertion fails closed against a regression.
func TestRegistry_GuardedClientRefusesLoopback(t *testing.T) {
	// Keep the address guard ON (SKIP_SSRF unset) but permit http (ALLOW_INSECURE),
	// so the ONLY possible refuser is the dial-time ADDRESS guard — the loopback
	// dial is refused deterministically, isolated from the scheme dimension.
	t.Setenv("SKIP_SSRF", "")
	t.Setenv("ALLOW_INSECURE", "true")
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	_, q, _ := setupRegistry(t, &fixedClock{now: now})
	ctx := t.Context()

	// A real loopback listener reserves a 127.0.0.1:port the registry would
	// dial; its served body never matters because the guard blocks the connect.
	origin := newFixtureOrigin(t, "irrelevant.test", nil)
	u, err := url.Parse(origin.server.URL)
	if err != nil {
		t.Fatalf("parse origin url: %v", err)
	}
	loopbackID := u.Host // 127.0.0.1:PORT

	// The SSRF-guarded client is SDK-owned and injected by the composition root;
	// construct it here exactly as production does.
	guarded := resolvers.NewGuardedClientFromEnv()
	reg := agentreg.New(agentreg.Config{
		Repo:  repo.NewAgentRepo(q),
		HTTP:  guarded,
		Clock: &fixedClock{now: now},
	})
	err = reg.RegisterFromDirectory(ctx, loopbackID, loopbackID)
	if !errors.Is(err, resolvers.ErrDirectoryUnavailable) || agentreg.IsCallerFault(err) {
		t.Fatalf("the SDK-guarded client must refuse a loopback target (a transient read failure); got %v", err)
	}
	requireNoAgentRow(t, ctx, q, loopbackID, "the SSRF guard blocked the fetch")
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// agentWBAJSON renders a WBA directory (protojson, snake_case) carrying the
// supplied Ed25519 keys. The shared forawellknown producer helpers guarantee the
// JWK shape matches what the consumer library accepts.
func agentWBAJSON(keys []fixtureKey) []byte {
	jwks := make([]*forawellknown.Key, 0, len(keys))
	for _, k := range keys {
		jwks = append(jwks, forawellknown.NewKey(k.pub, k.validFrom, k.validUntil))
	}
	return testutil.MarshalWBA(testutil.WBAFile(jwks...))
}
