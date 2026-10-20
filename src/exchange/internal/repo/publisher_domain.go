package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// PublisherDomain is the domain view of a fora.publisher_domains row: one site,
// belonging to exactly one publisher, and the two facts about it that the
// URL-signing path needs.
//
// Domain is the PUBLISHER's own hostname -- the site that serves
// /.well-known/fora.json. It is not repo.Tenant.Domain, which names the
// Exchange seat; the two are separate namespaces and a value from one never
// keys the other.
//
// CDN names the integration serving the site: cloudflare, fastly or cloudfront.
// Every supported integration verifies the delivery URL inside an edge function
// and so signs with Ed25519, which is why the scheme cannot stand in for it. It
// is empty on a site that predates self-service registration, where nobody
// recorded which integration the operator deployed.
//
// CloudFrontKeyPairID is empty for an Ed25519 site, and also for an AWS-fronted
// site whose publisher has not yet uploaded the public key to their own AWS
// account and entered what CloudFront gave back. Registration cannot supply it,
// so the empty state is normal and the signer is what refuses to mint a URL
// without it.
type PublisherDomain struct {
	Domain              string
	PublisherID         string
	SigningScheme       string
	CDN                 string
	CloudFrontKeyPairID string
	CreatedAt           time.Time
}

// NewSite is the argument set Create takes. It is a struct rather than four
// positional strings because three of them are strings of the same type and a
// transposed pair would compile: a site registered with the publisher id in the
// domain column is a row no later lookup finds, and no test that seeds through
// the same transposition would catch it.
type NewSite struct {
	Domain        string
	PublisherID   string
	SigningScheme string
	CDN           string
}

// PublisherDomainReadRepo is the read-only site contract. The catalog push path
// holds it to resolve a pushed entry's domain to its payee, and the URL-minting
// path holds it to read the scheme and key-pair id that site's edge verifies
// with, and the registration service holds it to read a payee's sites back to
// the console. None of them may write, so none depends on a port that could.
//
// Deployment-wide by design, like PublisherReadRepo: a site sits behind one CDN
// and carries one key-pair id whichever seat sells it. What that site offers and
// on what terms is the relationship's, and lives on the listing.
type PublisherDomainReadRepo interface {
	ByDomain(ctx context.Context, domain string) (PublisherDomain, error)
	// ByDomains resolves several sites in one read, ordered by domain. A domain
	// with no site is absent from the result rather than an error: the caller
	// holds the list it asked for and is the one that knows whether a gap is a
	// fault.
	ByDomains(ctx context.Context, domains []string) ([]PublisherDomain, error)
}

// PublisherDomainWriteRepo is the registration write contract for sites, held
// by the RegisterDomain and SetDomainKeyPairID handlers' service.
//
// Both ports take an explicit pgx.Tx: RegisterDomain commits the site row, its
// listing and its audit row together, and SetDomainKeyPairID commits the id with
// its own audit row.
type PublisherDomainWriteRepo interface {
	// Create registers a site under a publisher. The signing scheme follows from
	// the CDN, never from the publisher. Returns ErrPublisherDomainExists when
	// the site is already registered -- possibly under a different payee, which
	// is why this is never an upsert -- and ErrPublisherNotFound when the
	// publisher it names does not exist.
	Create(ctx context.Context, tx pgx.Tx, s NewSite) (PublisherDomain, error)
	// SetCloudFrontKeyPairID records the key-pair id CloudFront assigned in the
	// publisher's AWS account.
	//
	// publisherID scopes the write to the payee the caller is authorized for.
	// Sites are deployment-wide and the domain is caller input, so a write
	// keyed on the domain alone would let an actor holding a grant for one
	// payee overwrite another payee's site -- and this column becomes the
	// Key-Pair-Id on that site's delivery URLs, so the edge would then refuse
	// content the Exchange had already charged for.
	//
	// Returns rows-affected, which is zero for three reasons the caller cannot
	// tell apart: the site is not registered, it belongs to a different
	// publisher, or it does not sign with CloudFront RSA. In each case the id
	// was not recorded, so the caller must see the no-op rather than report
	// success.
	SetCloudFrontKeyPairID(
		ctx context.Context, tx pgx.Tx, domain, publisherID, keyPairID string,
	) (int64, error)
}

// ErrPublisherDomainNotFound is returned when a site lookup has no match.
var ErrPublisherDomainNotFound = errors.New("repo: publisher domain not found")

// ErrPublisherDomainExists is returned when registering a site that is already
// registered. A site belongs to exactly one publisher, so the second
// registration is refused rather than moved.
var ErrPublisherDomainExists = errors.New("repo: publisher domain already registered")

// NewPublisherDomainReadRepo composes the read-only site port over the fat
// sqlc.Querier.
func NewPublisherDomainReadRepo(q sqlc.Querier) PublisherDomainReadRepo {
	return &publisherDomainRepo{q: q}
}

// NewPublisherDomainWriteRepo composes the registration write ports for sites
// over the fat sqlc.Querier.
func NewPublisherDomainWriteRepo(q sqlc.Querier) PublisherDomainWriteRepo {
	return &publisherDomainRepo{q: q}
}

type publisherDomainRepo struct{ q sqlc.Querier }

func (r *publisherDomainRepo) ByDomain(ctx context.Context, domain string) (PublisherDomain, error) {
	row, err := r.q.GetPublisherDomain(ctx, domain)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublisherDomain{}, ErrPublisherDomainNotFound
		}
		return PublisherDomain{}, fmt.Errorf("get publisher domain: %w", err)
	}
	return publisherDomainFromRow(row), nil
}

func (r *publisherDomainRepo) ByDomains(ctx context.Context, domains []string) ([]PublisherDomain, error) {
	if len(domains) == 0 {
		return []PublisherDomain{}, nil
	}
	rows, err := r.q.ListPublisherDomainsByDomain(ctx, domains)
	if err != nil {
		return nil, fmt.Errorf("list publisher domains: %w", err)
	}
	out := make([]PublisherDomain, 0, len(rows))
	for _, row := range rows {
		out = append(out, publisherDomainFromRow(row))
	}
	return out, nil
}

func (r *publisherDomainRepo) Create(
	ctx context.Context, tx pgx.Tx, s NewSite,
) (PublisherDomain, error) {
	row, err := sqlc.New(tx).CreatePublisherDomain(ctx, sqlc.CreatePublisherDomainParams{
		Domain:        s.Domain,
		PublisherID:   s.PublisherID,
		SigningScheme: sqlc.ForaSigningScheme(s.SigningScheme),
		Cdn:           s.CDN,
	})
	if err != nil {
		return PublisherDomain{}, mapInsertErr(
			err, "create publisher domain", ErrPublisherDomainExists,
			fkSentinel{"publisher_domains_publisher_fk", ErrPublisherNotFound})
	}
	return publisherDomainFromRow(row), nil
}

func (r *publisherDomainRepo) SetCloudFrontKeyPairID(
	ctx context.Context, tx pgx.Tx, domain, publisherID, keyPairID string,
) (int64, error) {
	// pgText maps "" to SQL NULL, which is what clearing the id has to write:
	// the column's CHECK refuses the empty string, so an empty argument that
	// reached the column as '' would fail the write instead of clearing it.
	n, err := sqlc.New(tx).SetPublisherDomainKeyPairID(ctx, sqlc.SetPublisherDomainKeyPairIDParams{
		Domain:              domain,
		PublisherID:         publisherID,
		CloudfrontKeyPairID: pgText(keyPairID),
	})
	if err != nil {
		return 0, fmt.Errorf("set publisher domain cloudfront key-pair id: %w", err)
	}
	return n, nil
}

func publisherDomainFromRow(row sqlc.ForaPublisherDomain) PublisherDomain {
	return PublisherDomain{
		Domain:              row.Domain,
		PublisherID:         row.PublisherID,
		SigningScheme:       string(row.SigningScheme),
		CDN:                 row.Cdn,
		CloudFrontKeyPairID: textOrEmpty(row.CloudfrontKeyPairID),
		CreatedAt:           row.CreatedAt.Time,
	}
}
