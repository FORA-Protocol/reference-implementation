// Package consoleapi is the contract between the publisher console and the
// Exchange's console routes, the registration routes and the publisher reads:
// the paths, the request and response bodies, and the reason codes a check
// answers with.
//
// Both services import it, so it is written once. The routes are plain JSON
// over HTTP rather than Connect RPCs because the contract belongs to this
// implementation: the FORA protocol says nothing about how an Exchange
// registers publishers or what it shows them. What they share with every other signed surface is the
// signature, which the Exchange checks before a handler sees a body.
//
// The package holds shapes and constants only. Which requests are valid, and
// what an Exchange does with them, is the Exchange's service layer; how a
// person is shown an answer is the console's.
package consoleapi

// PathPrefix is the prefix every console route sits under. The Exchange
// verifies every request beneath it against the one console directory it is
// configured with, and the console signs every request it sends there, so the
// prefix is also the trust boundary.
const PathPrefix = "/exchange/v1/console/"

// The registration routes, without a method. Every one of them is a POST: even
// the reads carry a body, and the body is what is signed.
const (
	PathRegisterPublisher = PathPrefix + "publishers/register"
	PathRegisterDomain    = PathPrefix + "domains/register"
	PathCheckDomain       = PathPrefix + "domains/check"
	PathPreflightDomain   = PathPrefix + "domains/preflight"
	PathSetDomainTerms    = PathPrefix + "domains/terms"
	PathListRegistrations = PathPrefix + "registrations/list"
	PathDenomination      = PathPrefix + "denomination"
)

// DenominationRequest asks what every price this Exchange stores is
// denominated in. It names no actor: the answer is the deployment's, the same
// for every person.
type DenominationRequest struct{}

// Denomination is the currency and metering unit this Exchange prices every
// registered arrangement in. The publisher decides whether to charge and how
// much; the deployment decides in what. The console previews terms in exactly
// this denomination, so the terms a publisher agrees to are the terms the
// Exchange stores.
type Denomination struct {
	Currency string `json:"currency"`
	Unit     string `json:"unit"`
}

// Actor is the person a call is made for. The console asserts it and the
// Exchange believes it only because the signature proves which process is
// speaking; the Exchange then decides what that person may do.
type Actor struct {
	OIDCIssuer    string `json:"oidc_issuer"`
	OIDCSubject   string `json:"oidc_subject"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

// RegisterPublisherRequest mints, or returns, the payee this person acts for.
type RegisterPublisherRequest struct {
	Actor       Actor  `json:"actor"`
	DisplayName string `json:"display_name"`
}

// NewSite saves one site under the caller's payee and lists it on this seat,
// not verified. Saving reads nothing from the site; the check route is what
// verifies it.
type NewSite struct {
	Actor            Actor    `json:"actor"`
	PublisherID      string   `json:"publisher_id"`
	Domain           string   `json:"domain"`
	CDN              string   `json:"cdn"`
	ContentPathScope []string `json:"content_path_scope"`
	// Terms is the licensing the publisher chose, or null when they have not
	// chosen it yet. A site without terms is saved and listed, and is not
	// verified until it has them.
	Terms      *Terms `json:"terms"`
	SitemapURL string `json:"sitemap_url"`
	// ImportReplacesManualTerms is the publisher's answer to whether the
	// automatic catalog import may take over the site's pages that are already
	// in the catalog, pushed by another source: replace their terms, keep their
	// titles and descriptions. Left out, it is false. It is set with the terms:
	// here, or by the terms route while the site is not verified.
	ImportReplacesManualTerms bool `json:"import_replaces_manual_terms"`
}

// DomainTerms sets the terms of one of the caller's sites that is saved and
// not verified yet: the licensing, the sitemap the licensing step found, and
// the answer to whether the automatic import may take over pages already in
// the catalog. A verified site's terms are not changed here.
type DomainTerms struct {
	Actor                     Actor  `json:"actor"`
	PublisherID               string `json:"publisher_id"`
	Domain                    string `json:"domain"`
	Terms                     Terms  `json:"terms"`
	SitemapURL                string `json:"sitemap_url"`
	ImportReplacesManualTerms bool   `json:"import_replaces_manual_terms"`
}

// DomainRequest names one of the caller's domains. It is the body of both the
// check and the preflight routes: the two differ in what they do with it, not
// in what they take.
type DomainRequest struct {
	Actor       Actor  `json:"actor"`
	PublisherID string `json:"publisher_id"`
	Domain      string `json:"domain"`
}

// ListRegistrationsRequest reads back what this person has registered.
type ListRegistrationsRequest struct {
	Actor Actor `json:"actor"`
}

// Terms is the licensing a publisher agreed for one site: whether and at what
// price access is charged, which uses are permitted, which are prohibited
// outright, and whether attribution is required. The pricing models are
// termspreset.ModelFree and termspreset.ModelPerUnit.
type Terms struct {
	PricingModel        string   `json:"pricing_model"`
	Rate                string   `json:"rate,omitempty"`
	PermittedFunctions  []string `json:"permitted_functions"`
	ProhibitedFunctions []string `json:"prohibited_functions,omitempty"`
	AttributionRequired bool     `json:"attribution_required"`
}

// Publisher is the payee a person acts for.
type Publisher struct {
	PublisherID string `json:"publisher_id"`
	DisplayName string `json:"display_name"`
}

// MaxUnverifiedDomains is how many domains one publisher may hold saved and not
// verified at once. Saving holds a hostname before anything proves it is the
// publisher's, so one account could otherwise hold any number of names nobody
// else can register until an operator releases them. Verified domains do not
// count. The Exchange refuses a save beyond it; the console reads the same
// number to say so before the publisher has done the work.
const MaxUnverifiedDomains = 10

// ListingStateActive is the state of a listing its document has proved: a
// verified site, the only kind anything is sold from.
const ListingStateActive = "ACTIVE"

// Registration is one site as the Exchange reports it. State says whether it
// is verified: ACTIVE is verified and sold from, and any other state is a site
// saved but not verified yet, from which nothing is sold.
type Registration struct {
	Domain           string   `json:"domain"`
	CDN              string   `json:"cdn"`
	SigningScheme    string   `json:"signing_scheme"`
	State            string   `json:"state"`
	ContentPathScope []string `json:"content_path_scope"`
	// Terms is null for a site saved before its terms were chosen.
	Terms      *Terms `json:"terms"`
	SitemapURL string `json:"sitemap_url"`
	// ImportReplacesManualTerms is the answer NewSite registered the site with.
	ImportReplacesManualTerms bool    `json:"import_replaces_manual_terms"`
	VerifiedAt                *string `json:"verified_at"`
	LastCheckedAt             *string `json:"last_checked_at"`
}

// Active reports whether this site's listing has been proved by its document.
func (r Registration) Active() bool { return r.State == ListingStateActive }

// Registrations is what the console opens on: the payee, and every site.
// Registered is false for a person who has signed in and registered nothing,
// which is a normal state rather than an error.
type Registrations struct {
	Registered  bool           `json:"registered"`
	PublisherID string         `json:"publisher_id"`
	DisplayName string         `json:"display_name"`
	Sites       []Registration `json:"sites"`
}

// CheckResult is the answer of the check route: whether the site's document
// proved control, every reason it did not, and the listing as it now stands. A
// failed check is a 200 with reasons, because nothing went wrong with the
// request; the repair is an edit to a file on another host.
type CheckResult struct {
	Passed       bool         `json:"passed"`
	Reasons      []string     `json:"reasons"`
	Registration Registration `json:"registration"`
}

// PreflightResult answers a question about a domain that has no registration
// yet, so it carries no listing: only whether one would be accepted now and
// what stands in the way.
type PreflightResult struct {
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons"`
}

// ErrorBody is how every route answers a refusal: a status, a kind, and one
// sentence. The sentence is written for the publisher when the kind is a caller
// fault; for any other kind it is a fixed placeholder, because the Exchange's
// own diagnosis stays in the Exchange's log.
type ErrorBody struct {
	Error string `json:"error"`
	// Kind is the Exchange's classification of the refusal. The console
	// decides whose fault a refusal is from this, never from the HTTP status.
	Kind string `json:"kind,omitempty"`
}

// The kinds a refusal carries. They are the Exchange's own kind tokens, so a
// kind not listed here can still arrive, and is treated as a fault on the
// deployment's side.
const (
	// ErrorKindInvalidRequest: something in the request is wrong, and the
	// sentence names it.
	ErrorKindInvalidRequest = "invalid_request"
	// ErrorKindNotFound: the publisher, site or transaction the request names
	// is not registered here, or is not the acting person's to see.
	ErrorKindNotFound = "not_found"
	// ErrorKindPermissionDenied: the person acting holds no grant for what
	// they asked about.
	ErrorKindPermissionDenied = "permission_denied"
	// ErrorKindFailedPrecondition: the request is well formed, but the state
	// it acts on does not allow it.
	ErrorKindFailedPrecondition = "failed_precondition"
	// ErrorKindUnauthenticated: the request's signature did not verify.
	ErrorKindUnauthenticated = "unauthenticated"
	// ErrorKindUnavailable: the Exchange cannot serve console requests right
	// now, for a reason its operator has to repair.
	ErrorKindUnavailable = "unavailable"
	// ErrorKindInternal: the Exchange failed.
	ErrorKindInternal = "internal"
)

// CallerFaultKinds are the kinds whose sentence is written for the publisher:
// a refusal of something about the request itself. Every other kind is the
// deployment's to fix.
func CallerFaultKinds() []string {
	return []string{
		ErrorKindInvalidRequest,
		ErrorKindNotFound,
		ErrorKindPermissionDenied,
		ErrorKindFailedPrecondition,
	}
}

// The reasons a check can fail, as codes. The codes are stable and travel on
// the wire; the console turns each into the sentence a publisher reads. Each
// names one repair, because "verification failed" tells a publisher nothing
// about which line of which document to change.
const (
	// ReasonManifestMissing: the site answered 404 at the well-known path. The
	// repair is to publish the document, not to debug the network.
	ReasonManifestMissing = "manifest_missing"
	// ReasonManifestUnreadable: the fetch failed, or what came back was not a
	// document the Exchange can parse.
	ReasonManifestUnreadable = "manifest_unreadable"
	// ReasonManifestVersionRefused: the document parsed but declares a
	// version the Exchange does not accept.
	ReasonManifestVersionRefused = "manifest_version_refused"
	// ReasonExchangeNotListed: the document's exchanges list carries no entry
	// for this Exchange, so the site authorizes some other Exchange or none.
	ReasonExchangeNotListed = "exchange_not_listed"
	// ReasonResourceOwnerMismatch: the entry for this Exchange attests a
	// different payee than the one the caller registers under.
	ReasonResourceOwnerMismatch = "resource_owner_id_mismatch"
	// ReasonContributorNotListed: the document does not authorize the
	// operator's catalog contributor, so the operator could not import the
	// site's catalog even once the listing were active.
	ReasonContributorNotListed = "contributor_not_listed"
	// ReasonDomainTaken: the hostname is already registered to another
	// publisher. It names no publisher, because the caller holds no grant for
	// that one. It is the one reason a publisher cannot repair by editing a
	// file.
	ReasonDomainTaken = "domain_taken"
)

// AllReasons lists every reason code, so a consumer can prove it has an answer
// for each one.
func AllReasons() []string {
	return []string{
		ReasonManifestMissing,
		ReasonManifestUnreadable,
		ReasonManifestVersionRefused,
		ReasonExchangeNotListed,
		ReasonResourceOwnerMismatch,
		ReasonContributorNotListed,
		ReasonDomainTaken,
	}
}
