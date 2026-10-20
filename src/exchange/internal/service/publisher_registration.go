package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// RegistrationService is the publisher-registration write plane: the four
// operations the publisher console drives on behalf of a signed-in person.
//
// It is separate from ExchangeService for the reason AdminService is: the agent
// hot path and the registration plane share no dependency, and a person acting
// through the console is a different caller than an agent presenting an offer.
// What it does share with the hot path is the authority: grants, publishers,
// sites and listings all live in this database, and the console holds none of
// them. The console says WHO is acting; every decision about what that person
// may do is taken here, from the grant rows.
type RegistrationService struct {
	publishersRead repo.PublisherReadRepo
	publishers     repo.PublisherWriteRepo
	grants         repo.PublisherGrantWriteRepo
	sitesRead      repo.PublisherDomainReadRepo
	sites          repo.PublisherDomainWriteRepo
	gate           *PublisherGate
	listingsRead   repo.PublisherListingReadRepo
	listings       repo.PublisherListingWriteRepo
	audit          repo.AuditRepo
	tx             db.TxRunner
	manifests      ManifestChecker
	cfg            RegistrationConfig
}

// RegistrationDeps bundles the RegistrationService wiring.
type RegistrationDeps struct {
	PublishersRead repo.PublisherReadRepo
	Publishers     repo.PublisherWriteRepo
	Grants         repo.PublisherGrantWriteRepo
	SitesRead      repo.PublisherDomainReadRepo
	Sites          repo.PublisherDomainWriteRepo
	// Gate resolves this deployment's single seat and checks a person's grant.
	// The composition root builds one and hands the same instance to the
	// publisher read service, so both decide who may see a publisher from one
	// seat and one set of grants.
	Gate         *PublisherGate
	ListingsRead repo.PublisherListingReadRepo
	Listings     repo.PublisherListingWriteRepo
	Audit        repo.AuditRepo
	TxRunner     db.TxRunner
	Manifests    ManifestChecker
	Config       RegistrationConfig
}

// RegistrationConfig carries the two deployment values every registration
// decision reads: which seat this Exchange is, and which catalog contributor a
// site's manifest has to authorize.
type RegistrationConfig struct {
	// ExchangeDomain is the hostname a site's manifest must name in its
	// exchanges[] entry for this seat to be the one it authorizes.
	ExchangeDomain string
	// ContributorHost is the operator's catalog contributor: the host whose Web
	// Bot Auth directory signs catalog pushes for every publisher on this
	// deployment. A site's manifest must list it, or the operator cannot import
	// that site's catalog and the listing is not activated.
	ContributorHost string
	// TermsDefaults are the deployment's currency and metering unit. They price
	// what a publisher agrees; the publisher decides the arrangement, the
	// deployment decides the denomination.
	TermsDefaults termspreset.Defaults
}

// NewRegistrationService assembles the registration service.
func NewRegistrationService(d RegistrationDeps) *RegistrationService {
	return &RegistrationService{
		publishersRead: d.PublishersRead,
		publishers:     d.Publishers,
		grants:         d.Grants,
		sitesRead:      d.SitesRead,
		sites:          d.Sites,
		gate:           d.Gate,
		listingsRead:   d.ListingsRead,
		listings:       d.Listings,
		audit:          d.Audit,
		tx:             d.TxRunner,
		manifests:      d.Manifests,
		cfg:            d.Config,
	}
}

// Denomination is the currency and metering unit every arrangement this
// Exchange registers is priced in, with the deployment's fallbacks filled in.
// The console previews terms in it, so a publisher agrees to the terms this
// service stores rather than to terms priced under a configuration of the
// console's own.
func (s *RegistrationService) Denomination() termspreset.Defaults {
	return s.cfg.TermsDefaults.Resolved()
}

// Actor is the person the console asserts on every call: the OIDC issuer and
// subject that identify them, and the address their provider says it verified.
//
// Grants key on (issuer, subject) and never on the email, because an email
// address is reassignable and a subject is not. The address travels for the
// audit row and for the operator reading it later.
type Actor struct {
	OIDCIssuer  string
	OIDCSubject string
	Email       string
	// EmailVerified is the provider's answer to whether it checked Email.
	EmailVerified bool
}

// Validate refuses an actor the Exchange cannot authorize. An empty issuer or
// subject is not an anonymous caller to be treated leniently: it is a console
// that failed to assert anyone, and continuing would write a grant no later
// login could match.
//
// An unverified address is refused here too. The console refuses it at sign-in
// as well, and both refusals are deliberate: this service writes the grant, so
// it is the one that must refuse rather than trust its caller to have done so.
func (a Actor) Validate() error {
	if strings.TrimSpace(a.OIDCIssuer) == "" {
		return exchange.Newf(exchange.KindInvalidRequest, "actor oidc_issuer is required")
	}
	if strings.TrimSpace(a.OIDCSubject) == "" {
		return exchange.Newf(exchange.KindInvalidRequest, "actor oidc_subject is required")
	}
	if !a.EmailVerified {
		return exchange.Newf(exchange.KindInvalidRequest, "actor email is not verified")
	}
	return nil
}

// auditActor renders the actor for the audit log's free-form actor column.
// One spelling, defined here, so an operator grepping the column finds every
// row a person wrote regardless of which registration path wrote it.
func (a Actor) auditActor() *string {
	s := a.OIDCIssuer + "|" + a.OIDCSubject
	return &s
}

// RegisterPublisher mints the payee this person acts for on this seat, or
// returns the one they already hold.
//
// It is idempotent by actor rather than by a caller-supplied key. One account
// registers one publisher on one seat, and the unique index
// publisher_grants_one_actor_idx on (seat, issuer, subject) is what holds that.
// A repeat that arrives after the first registration committed finds the grant
// on the read below. A repeat that raced it, such as a double submit, passes
// that read too, and its grant insert then fails on the index: its transaction
// rolls back, taking its own publisher row with it, and it answers with the
// publisher the winner committed. A caller-supplied key would add a second
// record that can disagree with the grants, and a publisher minted under a key
// nobody holds a grant for is unreachable forever.
//
// The publisher row and its grant commit together. A publisher without its
// grant is a payee nobody can act for: the console never asserts ownership, so
// no later call could adopt it.
func (s *RegistrationService) RegisterPublisher(
	ctx context.Context, caller ConsoleCaller, displayName string,
) (repo.Publisher, error) {
	if err := caller.Actor.Validate(); err != nil {
		return repo.Publisher{}, err
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		return repo.Publisher{}, exchange.Newf(exchange.KindInvalidRequest, "display_name is required")
	}
	seat, err := s.gate.SeatID(ctx)
	if err != nil {
		return repo.Publisher{}, err
	}
	existing, err := s.gate.ActingPublisher(ctx, seat, caller.Actor)
	if err != nil {
		return repo.Publisher{}, err
	}
	if existing != "" {
		return s.readPublisher(ctx, existing)
	}
	out, err := s.mintPublisher(ctx, caller, seat, name)
	if errors.Is(err, repo.ErrGrantExists) {
		// A concurrent registration for this person committed first. Its grant
		// is visible now that this transaction has rolled back.
		return s.existingPublisher(ctx, seat, caller.Actor)
	}
	if err != nil {
		return repo.Publisher{}, err
	}
	return out, nil
}

// mintPublisher writes the publisher row, its grant, and the audit row in one
// transaction. It returns repo.ErrGrantExists unwrapped when the grant insert
// loses to a concurrent registration for the same person, so the caller can
// tell that case from a failure.
func (s *RegistrationService) mintPublisher(
	ctx context.Context, caller ConsoleCaller, seat, name string,
) (repo.Publisher, error) {
	publisherID := "pub_" + uuid.NewString()
	var out repo.Publisher
	err := s.tx.WithTx(ctx, func(tx pgx.Tx) error {
		p, werr := s.publishers.Create(ctx, tx, publisherID, name)
		if werr != nil {
			return exchange.Wrap(exchange.KindInternal, werr, "create publisher")
		}
		if gerr := s.grants.Grant(ctx, tx, repo.PublisherGrant{
			TenantID:    seat,
			PublisherID: publisherID,
			OIDCIssuer:  caller.Actor.OIDCIssuer,
			OIDCSubject: caller.Actor.OIDCSubject,
		}); gerr != nil {
			if errors.Is(gerr, repo.ErrGrantExists) {
				return gerr
			}
			return exchange.Wrap(exchange.KindInternal, gerr, "grant publisher")
		}
		detail, derr := marshalAuditDetail(registerPublisherAudit{
			PublisherID: publisherID,
			DisplayName: name,
			ActorEmail:  caller.Actor.Email,
		})
		if derr != nil {
			return derr
		}
		out = p
		return appendAudit(ctx, tx, s.audit, repo.AuditEntry{
			Actor:      caller.Actor.auditActor(),
			SourceAddr: caller.SourceAddr,
			Action:     "RegisterPublisher",
			Detail:     detail,
			TenantID:   seat,
			RequestID:  requestIDPtr(caller.RequestID),
		})
	})
	if err != nil {
		return repo.Publisher{}, err
	}
	return out, nil
}

// existingPublisher reads the publisher a concurrent registration committed for
// this person. The grant is guaranteed by the unique violation that sent the
// caller here, so finding none is an internal fault, never a not-found answer.
func (s *RegistrationService) existingPublisher(
	ctx context.Context, seat string, a Actor,
) (repo.Publisher, error) {
	id, err := s.gate.ActingPublisher(ctx, seat, a)
	if err != nil {
		return repo.Publisher{}, err
	}
	if id == "" {
		return repo.Publisher{}, exchange.Newf(exchange.KindInternal,
			"grant insert conflicted but no grant is visible for this actor")
	}
	return s.readPublisher(ctx, id)
}

// ConsoleCaller carries what the console proves about one request: the person
// acting, the address the request arrived from, and the correlation id. It is
// the registration plane's analogue of AdminCaller, and unlike that one its
// Actor is never absent — the transport refuses a console request without one.
type ConsoleCaller struct {
	Actor      Actor
	SourceAddr string
	RequestID  string
}

type registerPublisherAudit struct {
	PublisherID string `json:"publisher_id"`
	DisplayName string `json:"display_name"`
	ActorEmail  string `json:"actor_email,omitempty"`
}

func (s *RegistrationService) readPublisher(ctx context.Context, publisherID string) (repo.Publisher, error) {
	p, err := s.publishersRead.ByID(ctx, publisherID)
	if err != nil {
		if errors.Is(err, repo.ErrPublisherNotFound) {
			return repo.Publisher{}, exchange.Newf(exchange.KindNotFound, "publisher %q not found", publisherID)
		}
		return repo.Publisher{}, exchange.Wrap(exchange.KindInternal, err, "get publisher")
	}
	return p, nil
}

// Registration is one site as the console reads it back: the site row, its
// listing on this seat, and nothing the caller did not register.
type Registration struct {
	Domain           string
	SigningScheme    string
	CDN              string
	State            string
	ContentPathScope []string
	// Terms is nil for a site saved before its terms were chosen.
	Terms                     *Terms
	SitemapURL                string
	ImportReplacesManualTerms bool
	VerifiedAt                *string
	LastCheckedAt             *string
}

// ListRegistrations returns the publisher this person acts for and every site
// they have registered on this seat, so the console can resume a setup that was
// left half-finished. It is the read the console opens on, which is why it takes
// no publisher id: the actor's grants are what select the payee.
//
// An actor with no grant is not an error. It is someone who has signed in and
// registered nothing, and the console sends them to the registration form.
func (s *RegistrationService) ListRegistrations(
	ctx context.Context, caller ConsoleCaller,
) (repo.Publisher, []Registration, error) {
	if err := caller.Actor.Validate(); err != nil {
		return repo.Publisher{}, nil, err
	}
	seat, err := s.gate.SeatID(ctx)
	if err != nil {
		return repo.Publisher{}, nil, err
	}
	publisherID, err := s.gate.ActingPublisher(ctx, seat, caller.Actor)
	if err != nil {
		return repo.Publisher{}, nil, err
	}
	if publisherID == "" {
		return repo.Publisher{}, nil, nil
	}
	publisher, err := s.readPublisher(ctx, publisherID)
	if err != nil {
		return repo.Publisher{}, nil, err
	}
	listings, err := s.listingsRead.ByPublisher(ctx, seat, publisherID)
	if err != nil {
		return repo.Publisher{}, nil, exchange.Wrap(exchange.KindInternal, err, "list publisher domain listings")
	}
	out, err := s.joinSites(ctx, listings)
	if err != nil {
		return repo.Publisher{}, nil, err
	}
	return publisher, out, nil
}

// joinSites pairs each listing with its site, reading every site in one query.
//
// A listing whose site is missing is an internal fault, not a gap to skip: the
// listing's foreign key makes the state unreachable short of a hand-edited
// database, and a list that silently dropped the site would show the publisher
// fewer sites than they registered.
func (s *RegistrationService) joinSites(
	ctx context.Context, listings []repo.PublisherDomainListing,
) ([]Registration, error) {
	domains := make([]string, 0, len(listings))
	for _, l := range listings {
		domains = append(domains, l.Domain)
	}
	sites, err := s.sitesRead.ByDomains(ctx, domains)
	if err != nil {
		return nil, exchange.Wrap(exchange.KindInternal, err, "list publisher domains")
	}
	byDomain := make(map[string]repo.PublisherDomain, len(sites))
	for _, site := range sites {
		byDomain[site.Domain] = site
	}
	out := make([]Registration, 0, len(listings))
	for _, l := range listings {
		site, ok := byDomain[l.Domain]
		if !ok {
			return nil, exchange.Newf(exchange.KindInternal,
				"listing for %q has no registered site", l.Domain)
		}
		out = append(out, registrationFrom(site, l))
	}
	return out, nil
}

// registrationFrom joins the two rows the console reads one site through.
func registrationFrom(site repo.PublisherDomain, listing repo.PublisherDomainListing) Registration {
	return Registration{
		Domain:           listing.Domain,
		SigningScheme:    site.SigningScheme,
		CDN:              site.CDN,
		State:            listing.State,
		ContentPathScope: listing.ContentPathScope,
		Terms:            optionalTerms(listing.Terms, listing.Domain),
		SitemapURL:       listing.SitemapURL,
		// The listing's answer, false for one registered before it was asked.
		ImportReplacesManualTerms: listing.ImportReplacesManualTerms,
		VerifiedAt:                formatTime(listing.VerifiedAt),
		LastCheckedAt:             formatTime(listing.LastCheckedAt),
	}
}

func formatTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
