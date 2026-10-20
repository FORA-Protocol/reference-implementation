// Package repo is the console's persistence port. There is one record here --
// who has signed in -- because everything a person owns lives in the Exchange
// and the console keeps no copy it could disagree with.
package repo

import (
	"context"
	"fmt"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/db/sqlc"
)

// Account is one person who has signed in to the console.
//
// Issuer and Subject are the durable identity the login provider assigns. They
// are what the Exchange's grants key on, so a grant written during one sign-in
// is found by the next one even if the person's address has changed since.
//
// EmailVerified is the provider's answer, not ours. The console refuses
// publisher pages while it is false, and the Exchange refuses the write
// independently: one gate keeps the person out of a page that would not work,
// the other keeps an unverified address out of a grant.
type Account struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
	CreatedAt     time.Time
	LastLoginAt   time.Time
}

// AccountRepo is the narrow port the sign-in flow holds.
type AccountRepo interface {
	// Upsert records a sign-in, overwriting the display values the provider is
	// authoritative about and leaving the first-seen timestamp alone.
	Upsert(ctx context.Context, a Account) (Account, error)
}

// NewAccountRepo composes the port over the generated querier.
func NewAccountRepo(q sqlc.Querier) AccountRepo { return &accountRepo{q: q} }

type accountRepo struct{ q sqlc.Querier }

func (r *accountRepo) Upsert(ctx context.Context, a Account) (Account, error) {
	row, err := r.q.UpsertAccount(ctx, sqlc.UpsertAccountParams{
		OidcIssuer:    a.Issuer,
		OidcSubject:   a.Subject,
		Email:         a.Email,
		EmailVerified: a.EmailVerified,
		DisplayName:   a.DisplayName,
	})
	if err != nil {
		return Account{}, fmt.Errorf("upsert console account: %w", err)
	}
	return accountFromRow(row), nil
}

func accountFromRow(row sqlc.ConsoleAccount) Account {
	return Account{
		Issuer:        row.OidcIssuer,
		Subject:       row.OidcSubject,
		Email:         row.Email,
		EmailVerified: row.EmailVerified,
		DisplayName:   row.DisplayName,
		CreatedAt:     row.CreatedAt.Time,
		LastLoginAt:   row.LastLoginAt.Time,
	}
}
