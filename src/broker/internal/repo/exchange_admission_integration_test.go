//go:build integration

package repo_test

// The registry's routability rule is derived in Go, by repo.Exchange.Admission,
// and enforced again in SQL, by the trust_level predicate ListUnblockedExchanges
// carries. Two languages, one rule, and nothing but care keeping them together:
// a trust level added to the Postgres enum and handled in Admission but left out
// of the query — or the reverse — compiles, migrates, and passes every other
// test in the tree while the relay and the resolve router disagree about which
// exchanges exist.
//
// This is the test that makes them agree. It seeds one row per trust level and
// asserts the list the query returns is exactly the set Admission calls
// routable-or-recoverable, so neither side can gain or lose a value alone.

import (
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// everyTrustLevel is the whole broker.trust_level vocabulary as the domain
// spells it. A value added to the Postgres enum and not to this slice leaves
// the new value untested, which is why TestTrustLevelsMatchTheGeneratedEnum
// pins the slice against the enum sqlc generated from the migration.
var everyTrustLevel = []string{
	repo.TrustLevelDiscovered,
	repo.TrustLevelVerified,
	repo.TrustLevelPreferred,
	repo.TrustLevelBlocked,
}

// TestTrustLevelsMatchTheGeneratedEnum pins the domain vocabulary against the
// database's. The services compare repo.TrustLevel* rather than the generated
// sqlc constants, so that handlers and services stay off the generated package.
// That decoupling is worth having and it is also how the two spellings could
// drift: a renamed enum value would leave every comparison in the tree
// compiling against a string the column can no longer hold.
func TestTrustLevelsMatchTheGeneratedEnum(t *testing.T) {
	t.Parallel() // pure comparison of two constant sets — touches no database.

	generated := []string{
		string(sqlc.BrokerTrustLevelDISCOVERED),
		string(sqlc.BrokerTrustLevelVERIFIED),
		string(sqlc.BrokerTrustLevelPREFERRED),
		string(sqlc.BrokerTrustLevelBLOCKED),
	}
	domain := slices.Clone(everyTrustLevel)
	slices.Sort(generated)
	slices.Sort(domain)
	if len(domain) != len(generated) {
		t.Fatalf("domain trust levels %v, generated %v — the two vocabularies differ in size",
			domain, generated)
	}
	for i := range domain {
		if domain[i] != generated[i] {
			t.Errorf("domain trust level %q has no match in the generated enum %v",
				domain[i], generated)
		}
	}
}

// TestListUnblockedReturnsExactlyTheRowsAdmissionDoesNotBlock is the agreement
// test between the SQL predicate and the Go rule.
//
// It asserts both directions. A value the query excludes but Admission calls
// live would be an exchange the resolve router is willing to route to and the
// registry never lists. A value the query includes but Admission blocks would
// be one the relay hands to callers as a candidate and then refuses.
func TestListUnblockedReturnsExactlyTheRowsAdmissionDoesNotBlock(t *testing.T) {
	ctx := t.Context()
	r := repo.NewExchangeRepo(newTestPool(t, ctx))

	// One row per trust level at the column's default probe state, so the only
	// thing that can decide whether a row is listed is its trust level. A DOWN
	// or REFUSED row's listing is covered by the health-recovery suite, which
	// drives the state through a real probe pass.
	want := make([]string, 0, len(everyTrustLevel))
	for _, level := range everyTrustLevel {
		domain := "mp-" + level + ".example"
		if _, err := r.UpsertFromBootstrap(ctx, repo.Exchange{
			ID:                "mp-" + level,
			Domain:            domain,
			Endpoint:          "https://" + domain,
			TrustLevel:        level,
			SupportedProfiles: []string{"fora-news-v1"},
			Priority:          10,
		}); err != nil {
			t.Fatalf("seed %s exchange: %v", level, err)
		}
		if (repo.Exchange{TrustLevel: level, ProbeState: repo.ProbeLive}).Admission() != repo.AdmissionBlocked {
			want = append(want, domain)
		}
	}

	listed, err := r.ListUnblocked(ctx)
	if err != nil {
		t.Fatalf("list unblocked: %v", err)
	}
	got := make([]string, 0, len(listed))
	for _, ex := range listed {
		got = append(got, ex.Domain)
		if ex.Admission() == repo.AdmissionBlocked {
			t.Errorf("the query listed %q, which Admission blocks — the relay would "+
				"hand it to a caller as a candidate and then refuse it", ex.Domain)
		}
	}
	slices.Sort(want)
	slices.Sort(got)
	if len(want) != len(got) {
		t.Fatalf("listed %v, want %v — the SQL predicate and Admission disagree about "+
			"which trust levels are routable", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("listed %v, want %v — %q is missing from the registry's only list "+
				"read, so nothing routes to it", got, want, want[i])
		}
	}
}

// everyProbeState is the whole broker.exchange_probe_state vocabulary as the
// domain spells it, for the same reason everyTrustLevel exists.
var everyProbeState = []string{
	string(repo.ProbeLive),
	string(repo.ProbeDown),
	string(repo.ProbeRefused),
}

// TestProbeStatesMatchTheGeneratedEnum pins the domain vocabulary against the
// database's, exactly as TestTrustLevelsMatchTheGeneratedEnum does for trust.
// The stakes are the same and one step worse: probe_state decides whether a
// refusal reaches an agent as final or as retryable, so a value the column can
// no longer hold does not fail loudly — it falls through Admission's default
// arm and every refused exchange starts answering "try again".
func TestProbeStatesMatchTheGeneratedEnum(t *testing.T) {
	t.Parallel() // pure comparison of two constant sets — touches no database.

	generated := []string{
		string(sqlc.BrokerExchangeProbeStateLIVE),
		string(sqlc.BrokerExchangeProbeStateDOWN),
		string(sqlc.BrokerExchangeProbeStateREFUSED),
	}
	domain := slices.Clone(everyProbeState)
	slices.Sort(generated)
	slices.Sort(domain)
	if len(domain) != len(generated) {
		t.Fatalf("domain probe states %v, generated %v — the two vocabularies differ in size",
			domain, generated)
	}
	for i := range domain {
		if domain[i] != generated[i] {
			t.Errorf("domain probe state %q has no match in the generated enum %v",
				domain[i], generated)
		}
	}
}

// TestAdmissionClassifiesEveryRow covers the rule the relay's SSRF gate and the
// resolve router both read, over every combination that decides an answer.
//
// The two rows that matter most are the last two. A REFUSED row must NOT come
// out AdmissionDown: down is the retryable answer, and a refused manifest reads
// the same on every retry, so an agent told to try again about one retries
// forever. And a row carrying no probe state at all — the zero value, or a read
// path that forgot the column — must not come out routable, which is what an
// Admission that reached AdmissionLive by default would do.
func TestAdmissionClassifiesEveryRow(t *testing.T) {
	t.Parallel() // pure classification — touches no database.

	for name, tc := range map[string]struct {
		ex   repo.Exchange
		want repo.Admission
	}{
		"trusted and answering": {
			repo.Exchange{TrustLevel: repo.TrustLevelVerified, ProbeState: repo.ProbeLive},
			repo.AdmissionLive,
		},
		"trusted and unreachable": {
			repo.Exchange{TrustLevel: repo.TrustLevelVerified, ProbeState: repo.ProbeDown},
			repo.AdmissionDown,
		},
		"trusted and its manifest refused": {
			repo.Exchange{TrustLevel: repo.TrustLevelVerified, ProbeState: repo.ProbeRefused},
			repo.AdmissionRefused,
		},
		"trust withdrawn outranks a live probe": {
			repo.Exchange{TrustLevel: repo.TrustLevelBlocked, ProbeState: repo.ProbeLive},
			repo.AdmissionBlocked,
		},
		"trust withdrawn outranks a refused manifest": {
			repo.Exchange{TrustLevel: repo.TrustLevelBlocked, ProbeState: repo.ProbeRefused},
			repo.AdmissionBlocked,
		},
		"no probe state at all": {
			repo.Exchange{TrustLevel: repo.TrustLevelVerified},
			repo.AdmissionDown,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.ex.Admission(); got != tc.want {
				t.Errorf("Admission() = %v, want %v", got, tc.want)
			}
		})
	}
}
