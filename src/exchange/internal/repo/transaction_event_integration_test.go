//go:build integration

package repo_test

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
)

// Round-trip honesty: every leg here goes through the journal's own two ports,
// the write port ExecuteTransaction uses and the read port the console totals
// use. Neither port exposes single events, so an event is observed through the
// counts. The service-level rules — which denials are attributed, which
// requests may write — are driven through ExecuteTransaction in the transport
// suite; this file covers what the table and its two queries guarantee.

// journalAt is a fixed decision time. The journal takes the time it is given,
// so these tests need no clock.
var journalAt = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// journalFixture is one seat with two registered agents, and the journal's two
// ports.
type journalFixture struct {
	pool   *pgxpool.Pool
	seat   string
	events repo.TransactionEventRepo
	counts repo.DenialCountRepo
}

const (
	agentA = "agent-a.example"
	agentB = "agent-b.example"
)

func newJournalFixture(t *testing.T, ctx context.Context) journalFixture {
	t.Helper()
	pool := newTestPool(t, ctx)
	q := sqlc.New(pool)
	agents := repo.NewAgentRepo(q)
	seedAgent(t, ctx, agents, agentA)
	seedAgent(t, ctx, agents, agentB)
	return journalFixture{
		pool:   pool,
		seat:   repotest.SeedSeat(t, ctx, pool, repotest.Seat{Domain: "exchange.example"}),
		events: repo.NewTransactionEventRepo(q),
		counts: repo.NewDenialCountRepo(q),
	}
}

// denial is a valid event for publisher on the fixture's seat, refused for
// reason at journalAt plus offset.
func (f journalFixture) denial(agentID, key, publisher string, reason repo.DenialReason, offset time.Duration) repo.DenialEvent {
	return repo.DenialEvent{
		EventID:         uuid.NewString(),
		CreatedAt:       journalAt.Add(offset),
		TenantID:        f.seat,
		IdempotencyKey:  key,
		OfferID:         "offer-" + key,
		AgentID:         agentID,
		ResourceID:      "resource-" + publisher,
		ResourceOwnerID: publisher,
		Reason:          reason,
	}
}

func (f journalFixture) append(t *testing.T, ctx context.Context, ev repo.DenialEvent) bool {
	t.Helper()
	inserted, err := f.events.AppendDenial(ctx, ev)
	if err != nil {
		t.Fatalf("AppendDenial(%s, %s): %v", ev.AgentID, ev.IdempotencyKey, err)
	}
	return inserted
}

// countsIn reads publisher's denial counts on seat over [from, to), keyed by
// reason. The port promises no order, so the result is compared as a set.
func (f journalFixture) countsIn(
	t *testing.T, ctx context.Context, seat, publisher string, from, to time.Time,
) map[repo.DenialReason]int64 {
	t.Helper()
	rows, err := f.counts.DenialCounts(ctx, repo.DenialCountQuery{
		TenantID: seat, PublisherID: publisher, From: from, To: to,
	})
	if err != nil {
		t.Fatalf("DenialCounts(%s): %v", publisher, err)
	}
	out := make(map[repo.DenialReason]int64, len(rows))
	for _, r := range rows {
		if _, dup := out[r.Reason]; dup {
			t.Fatalf("DenialCounts returned %s twice", r.Reason)
		}
		out[r.Reason] = r.Count
	}
	return out
}

func assertCounts(t *testing.T, got, want map[repo.DenialReason]int64) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Fatalf("denial counts = %v, want %v", got, want)
	}
}

// TestAppendDenialRecordsOnePerAgentAndKey pins the journal's dedup. The same
// agent and item key is recorded once, whatever the second attempt carries, so
// a denial raised again on a retry is not counted twice; the first decision
// stays. A second agent with the same key is a different caller and is
// recorded, because the protocol scopes idempotency per authenticated caller.
func TestAppendDenialRecordsOnePerAgentAndKey(t *testing.T) {
	ctx := context.Background()
	f := newJournalFixture(t, ctx)

	first := f.denial(agentA, "req-1:offer-1", "pub_a", "INSUFFICIENT_BALANCE", 0)
	if !f.append(t, ctx, first) {
		t.Fatal("first denial for (agent-a, req-1:offer-1) was not recorded")
	}
	retry := f.denial(agentA, "req-1:offer-1", "pub_a", "REPORTING_OVERDUE", time.Second)
	if f.append(t, ctx, retry) {
		t.Fatal("a second denial for the same agent and key was recorded; the retry is counted twice")
	}
	other := f.denial(agentB, "req-1:offer-1", "pub_a", "INSUFFICIENT_BALANCE", 0)
	if !f.append(t, ctx, other) {
		t.Fatal("another agent's denial under the same key was dropped")
	}

	got := f.countsIn(t, ctx, f.seat, "pub_a", journalAt, journalAt.Add(time.Hour))
	assertCounts(t, got, map[repo.DenialReason]int64{"INSUFFICIENT_BALANCE": 2})
}

// TestDenialCountsScopeToSeatPublisherAndPeriod pins every predicate of the
// read: the seat, the publisher, the reason grouping, and the half-open period —
// an event at the start is inside it, an event at the end is not.
func TestDenialCountsScopeToSeatPublisherAndPeriod(t *testing.T) {
	ctx := context.Background()
	f := newJournalFixture(t, ctx)
	otherSeat := repotest.SeedSeat(t, ctx, f.pool, repotest.Seat{Domain: "other-seat.example"})

	f.append(t, ctx, f.denial(agentA, "k1:o", "pub_a", "OFFER_EXPIRED", 0))
	f.append(t, ctx, f.denial(agentA, "k2:o", "pub_a", "OFFER_EXPIRED", 30*time.Minute))
	f.append(t, ctx, f.denial(agentA, "k3:o", "pub_a", "ACCOUNT_INACTIVE", 59*time.Minute))
	f.append(t, ctx, f.denial(agentA, "k4:o", "pub_a", "OFFER_EXPIRED", time.Hour))    // at the end: outside
	f.append(t, ctx, f.denial(agentA, "k5:o", "pub_a", "OFFER_EXPIRED", -time.Second)) // before the start
	f.append(t, ctx, f.denial(agentA, "k6:o", "pub_b", "OFFER_EXPIRED", 0))            // another publisher

	start, end := journalAt, journalAt.Add(time.Hour)
	assertCounts(t, f.countsIn(t, ctx, f.seat, "pub_a", start, end),
		map[repo.DenialReason]int64{"OFFER_EXPIRED": 2, "ACCOUNT_INACTIVE": 1})
	assertCounts(t, f.countsIn(t, ctx, f.seat, "pub_b", start, end),
		map[repo.DenialReason]int64{"OFFER_EXPIRED": 1})
	assertCounts(t, f.countsIn(t, ctx, otherSeat, "pub_a", start, end), map[repo.DenialReason]int64{})
	assertCounts(t, f.countsIn(t, ctx, f.seat, "pub_c", start, end), map[repo.DenialReason]int64{})
}

// TestAppendDenialRefusesAnUnknownReason pins that a label outside the
// fora.denial_reason enum is refused before anything is written, so a service
// that failed to translate a reason gets an error rather than a journal row the
// counts could not name.
func TestAppendDenialRefusesAnUnknownReason(t *testing.T) {
	ctx := context.Background()
	f := newJournalFixture(t, ctx)

	for _, reason := range []repo.DenialReason{"", "UNSPECIFIED", "DENIAL_REASON_OFFER_EXPIRED", "offer_expired"} {
		inserted, err := f.events.AppendDenial(ctx, f.denial(agentA, "k:"+string(reason), "pub_a", reason, 0))
		if !errors.Is(err, repo.ErrInvalidDenialReason) {
			t.Fatalf("AppendDenial with reason %q: err = %v, want ErrInvalidDenialReason", reason, err)
		}
		if inserted {
			t.Fatalf("AppendDenial with reason %q reported a write", reason)
		}
	}
	assertCounts(t, f.countsIn(t, ctx, f.seat, "pub_a", journalAt, journalAt.Add(time.Hour)),
		map[repo.DenialReason]int64{})
}
