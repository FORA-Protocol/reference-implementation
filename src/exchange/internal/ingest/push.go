// Package ingest signs a FORA catalog feed and pushes it to the Exchange.
//
// It reads the feed through the shared catalogfeed package — one resource per
// line, parsed into typed records and mapped to proto-exact
// fora.v1.ResourceEntry values — then signs and submits them through the
// protocol SDK's catalog client over the CatalogService.PushResources RPC, in
// several submissions when the feed is larger than the wire bound on one. The
// cmd/fora-ingest binary is a thin flag-parsing wrapper over Run and Check.
package ingest

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	sdkconnect "github.com/FORA-Protocol/protocol/sdk/go/connect"
	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/catalogfeed"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
)

// signatureTTL bounds the RFC 9421 signature's validity window. Every
// submission is one synchronous RPC signed just before it is sent, so a short
// window suffices.
const signatureTTL = 30 * time.Second

// ContributorKey is the on-disk shape of a catalog-contributor Ed25519
// keypair: a kid plus base64url-encoded raw (32-byte) seed and public key.
// Both key-gen paths emit it — scripts/gen-contributor-key.sh (the operator
// path the fora-ingest --key flag names) and scripts/gen-e2e-keys.sh (the
// e2e fixtures) — via the same shared materializer, so the shapes cannot
// diverge.
type ContributorKey struct {
	KID        string `json:"kid"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

// LoadContributorKey reads and decodes the contributor keypair at path. The
// kid becomes the PushResources caller_id (Gate 1 keyID and Gate 2 contributor
// identity must both resolve to it).
func LoadContributorKey(path string) (kid string, priv ed25519.PrivateKey, err error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied key path
	if err != nil {
		return "", nil, fmt.Errorf("read contributor key %q: %w", path, err)
	}
	var doc ContributorKey
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", nil, fmt.Errorf("parse contributor key %q: %w", path, err)
	}
	if doc.KID == "" {
		return "", nil, fmt.Errorf("contributor key %q: missing kid", path)
	}
	seed, err := base64.RawURLEncoding.DecodeString(doc.PrivateKey)
	if err != nil {
		return "", nil, fmt.Errorf("contributor key %q: decode private_key: %w", path, err)
	}
	if len(seed) != ed25519.SeedSize {
		return "", nil, fmt.Errorf(
			"contributor key %q: private_key is %d bytes, want %d (raw seed)",
			path, len(seed), ed25519.SeedSize,
		)
	}
	return doc.KID, ed25519.NewKeyFromSeed(seed), nil
}

// SignatureAgentFor is the https origin a contributor's catalog push names as
// its Signature-Agent member: https://<kid>, the contributor hostname its
// keypair names, which is where the contributor serves its key directory. The
// Exchange's catalog gate resolves the signing key by the push's caller_id,
// which is that same kid (LoadContributorKey), so the member names the directory
// the gate reads, and a Web Bot Auth verifier that follows the member finds the
// same key. A kid that does not make an https origin (helpers.CheckHTTPSOrigin)
// is refused before anything is signed or sent.
func SignatureAgentFor(kid string) (string, error) {
	origin := "https://" + kid
	if err := helpers.CheckHTTPSOrigin(origin); err != nil {
		return "", fmt.Errorf("catalog push signature agent: %w", err)
	}
	return origin, nil
}

// NewCatalogClient builds the SDK catalog client the push speaks through,
// against the Exchange at exchangeURL, signing as the contributor whose key
// directory is served at signatureAgent (an https origin, see
// SignatureAgentFor). Custody stays at the pipeline root: the private key
// becomes an SDK Signer here and never travels through PushEntries.
//
// The composition: an Ed25519 signer whose RFC 9421 keyid is the RFC 7638
// thumbprint of priv's public key; signatureAgent as the covered
// Signature-Agent member, which names the contributor's directory to any Web
// Bot Auth verifier; the
// SDK's ClockWindow over clk for the signature timestamps (the SDK transport
// adds a fresh nonce to every signature, and the nonce is what keeps two
// identical submissions in one second unique for the Exchange's replay store);
// and
// strict wire validation, so a request the Exchange would refuse at its wire
// tier is refused here, before it is signed or sent. No sign predicate is
// needed: this client dials only /fora.v1.CatalogService/* procedures, every
// one of which is signed.
//
// What the SDK client brings over a signing transport composed around the
// generated client: it refuses redirects (a client that followed one would
// re-sign the request for a target the peer chose), caps what it reads of a
// response at 1 MiB, refuses a request that names no recipient before
// anything is signed, and validates the response against the wire rules.
//
// It speaks the Connect protocol, the SDK's default — deliberately not gRPC.
// The CLI pushes over public HTTPS through the deployment's TLS proxy, whose
// upstream hop is HTTP/1.1. gRPC carries the RPC verdict in HTTP trailers,
// which do not survive that hop: the proxy aborts the stream and the push
// fails with a transport error before any verdict arrives. Connect has no
// trailer dependency. Same decision, same reason, as the Broker's Exchange
// relay client.
//
// extra options follow the signing ones, so a caller can add what its run
// needs. The CLI (Run) passes sdkconnect.WithRequestIDFunc over the one id it
// puts on the run's context, so every submission of a run carries the same
// X-Request-ID and the Exchange's log lines for the run share one request_id.
// The automatic import builds its client through importer.NewCatalogClient,
// which is given its run's id the same way -- fora-import also logs that id on
// its own lines -- and adds the label that marks its pushes as the import's.
// This constructor never adds that label, so a push through Run is always
// recorded as a manual one.
func NewCatalogClient(
	exchangeURL, signatureAgent string, priv ed25519.PrivateKey, clk clock.Clock, extra ...sdkconnect.ClientOption,
) (*sdkconnect.CatalogClient, error) {
	if err := helpers.CheckHTTPSOrigin(signatureAgent); err != nil {
		return nil, fmt.Errorf("catalog push signature agent: %w", err)
	}
	if clk == nil {
		clk = clock.System{}
	}
	keyID, err := helpers.Thumbprint(priv.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, fmt.Errorf("derive contributor keyid thumbprint: %w", err)
	}
	signer, err := helpers.NewEd25519Signer(keyID, priv)
	if err != nil {
		return nil, fmt.Errorf("build contributor signer: %w", err)
	}
	opts := append([]sdkconnect.ClientOption{
		sdkconnect.WithSigner(signer),
		sdkconnect.WithSignatureAgent(signatureAgent),
		sdkconnect.WithSignWindow(core.ClockWindow(clk.Now, signatureTTL)),
		sdkconnect.WithValidation(sdkconnect.ValidationStrict),
	}, extra...)
	return sdkconnect.NewCatalogClient(strings.TrimRight(exchangeURL, "/"), opts...), nil
}

// PushTarget names who one catalog push is addressed to and who is sending
// it. Where it is dialled is the client's: NewCatalogClient took the URL.
type PushTarget struct {
	// Exchange is the recipient's bare identity domain, e.g. "exchange.example"
	// or "exchange.example:8081". It is normally the host of the URL the
	// client dials, because an Exchange is reached at its own identity — Run
	// derives it that way. They come apart only behind a routing shim, such
	// as a test harness reaching the Exchange through a mapped 127.0.0.1
	// port, where the dialled host names no Exchange at all.
	Exchange string
	// TenantID is the publisher tenant WITHIN that Exchange the entries are
	// pushed under (required). It names a tenant, never the Exchange.
	TenantID string
	// CallerID is the contributor key's kid (Gate 1 + Gate 2 identity — the
	// contributor DOMAIN, which the client also publishes as its
	// Signature-Agent directory origin).
	CallerID string
}

// PushEntries pushes entries to the Exchange's CatalogService.PushResources in
// feed order, through the SDK catalog client, and returns the structured
// verdict. It is RPC-only — there is no SQL path.
//
// The wire bounds one submission (EntriesPerSubmission), so a larger feed goes
// as several submissions, each a separate PushResources call carrying a
// contiguous slice of the feed. The contract of a run that needs more than
// one:
//
//   - submissions go in feed order, and each is stored or refused whole — the
//     unit of atomicity is the submission, never the feed;
//   - the first refusal stops the run: nothing after the refused submission
//     is sent; the report names the entry range that was stored, the
//     submission that was refused and why, and the range that was not sent,
//     and the returned error says the same;
//   - re-running the same feed is safe, because a catalog push is an upsert
//     keyed on the resource URI: the entries already stored are stored again
//     unchanged, and the run proceeds to the ones that were not. It converges
//     for a refusal that a later run would not repeat — a transient failure, or
//     an entry the publisher has since corrected. It does NOT converge for a
//     refusal the same input reproduces, and one such refusal is reachable
//     here: the split honours the entry-count bound only, while the recipient
//     ALSO bounds the size of a request. A feed of unusually large entries can
//     therefore build a submission that is under the count bound and over the
//     size one, and re-running rebuilds the identical submission. Splitting the
//     feed is the remedy until the client relates the two bounds.
//
// The Exchange never answers a partial acceptance: a refusal is an error from
// the call (CodeInvalidArgument for a violation), so a nil error means every
// entry of the feed was stored. That is the contract, and it is CHECKED here
// rather than assumed: a 2xx reporting fewer accepted entries than the
// submission carried is treated as a refusal of that submission, because the
// alternative is a run that exits 0 over a catalog the recipient did not store.
// No wire rule correlates the counts, and this client dials whatever Exchange
// an operator names, so the invariant holds only where something asserts it.
//
// An empty feed is refused here rather than sent, for the reason the wire
// refuses it: it asks for nothing.
func PushEntries(
	ctx context.Context,
	client *sdkconnect.CatalogClient,
	target PushTarget,
	entries []*forav1.ResourceEntry,
) (PushReport, error) {
	if len(entries) == 0 {
		return PushReport{}, catalogfeed.ErrEmptyFeed
	}
	bound, err := EntriesPerSubmission()
	if err != nil {
		return PushReport{}, err
	}
	report := PushReport{Entries: len(entries), Planned: submissionsNeeded(len(entries), bound)}
	for first := 0; first < len(entries); first += bound {
		last := min(first+bound, len(entries)) - 1
		result := SubmissionResult{First: first, Last: last}
		// ver is stamped here from the single owner of the protocol version;
		// the SDK client would fill an empty one from the same constant.
		resp, err := client.PushResources(ctx, &forav1.PushResourcesRequest{
			Ver:      helpers.ProtocolVersion,
			Exchange: target.Exchange,
			TenantId: target.TenantID,
			CallerId: target.CallerID,
			Entries:  entries[first : last+1],
		})
		if err == nil {
			err = shortAcceptance(resp.GetAccepted(), last-first+1)
		}
		if err != nil {
			result.Err = err
			report.Submissions = append(report.Submissions, result)
			return report, report.refusal()
		}
		result.Accepted = resp.GetAccepted()
		result.Warnings = resp.GetWarnings()
		report.Accepted += result.Accepted
		report.Warnings = append(report.Warnings, result.Warnings...)
		report.Submissions = append(report.Submissions, result)
	}
	return report, nil
}

// shortAcceptance turns a 2xx that accepted fewer entries than the submission
// carried into the refusal it is. A conforming Exchange cannot produce one: it
// stores a submission whole or refuses it whole, and the refusal is an error
// from the call rather than a count. Nothing on the wire says so, though —
// PushResourcesResponse carries no rule relating accepted to the request — so a
// recipient that answers 2xx with a short count would otherwise leave the run
// exiting 0 over a catalog it did not store. accepted is reported back so the
// operator sees which of the two numbers disagreed.
func shortAcceptance(accepted int32, sent int) error {
	if int(accepted) == sent {
		return nil
	}
	return fmt.Errorf(
		"the Exchange answered success but accepted %d of the %d entries it was sent; "+
			"a submission is stored whole or refused whole, so this answer is neither", accepted, sent)
}

// Options configures a full ingest Run.
type Options struct {
	// ExchangeURL is the Exchange base URL this push is dialled at (required).
	ExchangeURL string
	// Exchange overrides the recipient the push is addressed to. Empty means
	// the host of ExchangeURL, which is the right answer everywhere the
	// Exchange is dialled at its own identity — see PushTarget.
	Exchange string
	// TenantID is the tenant the entries are pushed under (required).
	TenantID string
	// KeyPath is the catalog-contributor Ed25519 keypair JSON path (required).
	KeyPath string
	// Feed is the JSON-L source to parse.
	Feed io.Reader
	// Report sink for the structured verdict; nil discards it.
	Report io.Writer
	// Clk supplies the wall clock for RFC 9421 created/expires; nil → System.
	Clk clock.Clock
}

// Run executes the full ingest pipeline (parse → map → sign+push) and returns
// a non-nil error when configuration is incomplete, the feed is malformed or
// empty, or a submission was refused or could not be sent. The report is
// written once anything was sent, so a refusal mid-run still says what was
// stored. Every submission of the run carries one request id: the one ctx
// already carries, or one minted for the run. main is a thin wrapper over this
// so the parse/map/push/exit-code contract is exercised by tests.
func Run(ctx context.Context, opts Options) (PushReport, error) {
	if opts.ExchangeURL == "" {
		return PushReport{}, errors.New("exchange URL is required")
	}
	if opts.TenantID == "" {
		return PushReport{}, errors.New("tenant is required")
	}
	recipient, err := foraaudience.RecipientOf(opts.ExchangeURL, opts.Exchange)
	if err != nil {
		return PushReport{}, fmt.Errorf("catalog push: %w", err)
	}

	records, err := catalogfeed.ParseJSONL(opts.Feed)
	if err != nil {
		return PushReport{}, err
	}
	entries, err := catalogfeed.MapRecords(records)
	if err != nil {
		return PushReport{}, err
	}

	kid, priv, err := LoadContributorKey(opts.KeyPath)
	if err != nil {
		return PushReport{}, err
	}
	signatureAgent, err := SignatureAgentFor(kid)
	if err != nil {
		return PushReport{}, err
	}
	// One request id for the whole run, set before the client exists so every
	// submission reads the same one; a caller that already set one keeps it.
	ctx = reqctx.WithID(ctx, reqctx.IDOrNew(ctx))
	client, err := NewCatalogClient(opts.ExchangeURL, signatureAgent, priv, opts.Clk,
		sdkconnect.WithRequestIDFunc(func() string { return reqctx.IDOrNew(ctx) }))
	if err != nil {
		return PushReport{}, err
	}

	report, err := PushEntries(ctx, client, PushTarget{
		Exchange: recipient,
		TenantID: opts.TenantID,
		CallerID: kid,
	}, entries)
	if opts.Report != nil && len(report.Submissions) > 0 {
		WriteReport(opts.Report, report)
	}
	return report, err
}
