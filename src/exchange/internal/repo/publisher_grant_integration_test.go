//go:build integration

package repo_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// TestGrantAllowsOnePublisherPerPersonPerSeat pins the mapping of the unique
// index publisher_grants_one_actor_idx to repo.ErrGrantExists. A person who
// already acts for one payee on a seat cannot be granted a second one there. A
// repeat of the same grant is still a no-op, and the same person may hold a
// grant on another seat.
//
// Persistence round-trip only, for the reason the sibling registration suite
// gives: the writes and the read go through the repository ports and stop there.
func TestGrantAllowsOnePublisherPerPersonPerSeat(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	q := sqlc.New(pool)
	const (
		seatA   = "t-one-actor-a"
		seatB   = "t-one-actor-b"
		issuer  = "https://login.example"
		subject = "sub-1"
	)
	seedTenantRow(t, ctx, q, seatA, "exchange.example")
	seedTenantRow(t, ctx, q, seatB, "other-exchange.example")
	for _, id := range []string{"pub-first", "pub-second"} {
		if _, err := createPublisher(t, ctx, pool, id, id); err != nil {
			t.Fatalf("Create publisher %q: %v", id, err)
		}
	}
	first := repo.PublisherGrant{TenantID: seatA, PublisherID: "pub-first", OIDCIssuer: issuer, OIDCSubject: subject}
	if err := grant(t, ctx, pool, first); err != nil {
		t.Fatalf("first Grant: %v", err)
	}
	if err := grant(t, ctx, pool, first); err != nil {
		t.Fatalf("repeating the same Grant = %v, want a no-op", err)
	}

	second := first
	second.PublisherID = "pub-second"
	if err := grant(t, ctx, pool, second); !errors.Is(err, repo.ErrGrantExists) {
		t.Fatalf("granting a second payee on the same seat = %v, want repo.ErrGrantExists", err)
	}
	ids, err := repo.NewPublisherGrantReadRepo(q).PublisherIDsForActor(ctx, seatA, issuer, subject)
	if err != nil {
		t.Fatalf("PublisherIDsForActor: %v", err)
	}
	if !slices.Equal(ids, []string{"pub-first"}) {
		t.Fatalf("PublisherIDsForActor after the refused grant = %v, want [pub-first]", ids)
	}

	second.TenantID = seatB
	if err := grant(t, ctx, pool, second); err != nil {
		t.Fatalf("granting the same person a payee on another seat: %v", err)
	}
}
