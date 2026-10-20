package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	connect "connectrpc.com/connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/httpsig/transportconnect"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/iplimit"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/keypolicy"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// ConsolePathPrefix is the route prefix the publisher console's calls, its
// registrations and its reads, arrive under. Every request beneath it is verified against ONE
// configured key directory before a handler sees it, so the prefix is also the
// trust boundary: a signature that verifies here was made by the console this
// deployment is configured with, and by nothing else.
const ConsolePathPrefix = consoleapi.PathPrefix

// The registration routes as the mux mounts them: the shared paths, each under
// the one method they answer. The paths and bodies are the contract the
// console imports too (see consoleapi); what they share with every other
// signed surface is the signature, the same RFC 9421 Ed25519 profile, resolved
// from the caller's own Web Bot Auth directory.
const (
	ConsoleRegisterPublisherPath = "POST " + consoleapi.PathRegisterPublisher
	ConsoleRegisterDomainPath    = "POST " + consoleapi.PathRegisterDomain
	ConsoleCheckDomainPath       = "POST " + consoleapi.PathCheckDomain
	ConsolePreflightDomainPath   = "POST " + consoleapi.PathPreflightDomain
	ConsoleSetDomainTermsPath    = "POST " + consoleapi.PathSetDomainTerms
	ConsoleListRegistrationsPath = "POST " + consoleapi.PathListRegistrations
	ConsoleDenominationPath      = "POST " + consoleapi.PathDenomination
)

// The publisher read routes as the mux mounts them.
const (
	ConsoleRevenueTotalsPath     = "POST " + consoleapi.PathRevenueTotals
	ConsoleListTransactionsPath  = "POST " + consoleapi.PathListTransactions
	ConsoleTransactionDetailPath = "POST " + consoleapi.PathTransactionDetail
	ConsoleListCatalogPath       = "POST " + consoleapi.PathListCatalog
	ConsoleCatalogDetailPath     = "POST " + consoleapi.PathCatalogDetail
	ConsolePublisherTermsPath    = "POST " + consoleapi.PathPublisherTerms
)

// maxConsoleBodyBytes bounds what one console request can make the
// Exchange read. The body is buffered before the signature is checked -- there
// is no way to verify a digest over bytes that have not been read -- so this
// bound is what an unauthenticated caller reaches, and it is far above any
// honest request: the largest of these bodies is a registration's domain,
// preset and handful of paths.
const maxConsoleBodyBytes = 32 * 1024

// There is deliberately NO per-source rate limit on this surface, unlike the
// agent self-signup endpoint one file over.
//
// That endpoint is open to anyone, so a per-address bucket is what separates
// one caller from the next. Here every legitimate request arrives from ONE
// address -- the console process -- on behalf of whichever person is signed in,
// so a per-address bucket would not separate callers at all: it would meter the
// only caller there is, and a publisher registering a second site would be
// refused because somebody else registered a first one a moment earlier. What
// bounds an unauthenticated flood instead is the body cap above and the
// signature check below, which is the same posture the Connect mounts run under.

// ConsoleSignatureMiddleware verifies the RFC 9421 signature on every request
// under ConsolePathPrefix and refuses the request when it does not verify.
//
// The resolver it verifies against is bound to the console's configured
// directory and to nothing else, so this middleware answers one question:
// did the configured console sign this? There is no caller class to resolve, no
// agents row to look up, and no lazy self-signup -- a caller that is not the
// console cannot become one by presenting a directory of its own. That is the
// property the actor assertion rests on: the console tells the Exchange which
// person is acting, and the Exchange believes it only because the signature
// proves which process is speaking.
//
// The body is read once, capped, and put back for the handler, the same shape
// CatalogSignatureMiddleware uses: a digest cannot be checked over a stream
// that a handler has already consumed.
func ConsoleSignatureMiddleware(
	resolver helpers.KeyResolver, replay ConsoleReplayStore, next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, ConsolePathPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConsoleBodyBytes))
		if err != nil {
			writeConsoleReadError(w, r, err)
			return
		}
		_ = r.Body.Close()
		if err := verifyConsoleSignature(r, body, resolver, replay); err != nil {
			reqctx.FromContext(r.Context()).WarnContext(r.Context(), "console.signature_rejected",
				"path", r.URL.Path, "client_ip", iplimit.ClientIP(r, false), "err", err)
			// A missing covered component or a refused signature form is
			// answered with what a signature here must cover, as the SDK's
			// connectserver verify seam answers it on every FORA RPC.
			if accept, ok := helpers.AcceptSignatureFor(err); ok {
				w.Header().Set(helpers.AcceptSignatureHeader, accept)
			}
			writeConsoleRefusal(w, http.StatusUnauthorized,
				exchange.KindUnauthenticated, "request signature did not verify")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

// writeConsoleReadError answers a body that could not be read. Only a body past
// the cap is a size refusal; a connection that broke mid-body is a malformed
// request, and answering it 413 would tell the console to send less when
// nothing it sent was too large.
func writeConsoleReadError(w http.ResponseWriter, r *http.Request, err error) {
	if transportconnect.IsBodyTooLarge(err) {
		writeConsoleRefusal(w, http.StatusRequestEntityTooLarge,
			exchange.KindInvalidRequest, "request body too large")
		return
	}
	reqctx.FromContext(r.Context()).InfoContext(r.Context(), "console.body_unreadable",
		"path", r.URL.Path, "err", err)
	writeConsoleRefusal(w, http.StatusBadRequest,
		exchange.KindInvalidRequest, "request body could not be read")
}

// ConsoleReplayStore is the narrow replay port the console surface needs. It is
// the same (keyID, signature) pair every other signed surface records, held as
// its own interface here so this file depends on a method set rather than on a
// store implementation.
type ConsoleReplayStore interface {
	Seen(ctx context.Context, keyID, signature string) (bool, error)
	SeenOrAdd(ctx context.Context, keyID, signature string, ttl time.Duration) (bool, error)
}

// consoleReplayTTL is how long one console signature is remembered. It matches
// the window every other FORA surface uses, so a captured registration request
// is no more replayable than a captured transaction.
//
// WHAT THIS REQUIRES OF THE CONSOLE. An Ed25519 signature is deterministic:
// the same body, signed by the same key, over the same (created, expires) pair
// and parameters, produces the same bytes. Registration is deliberately
// retry-safe, so the console DOES re-send an identical body -- a double submit,
// a re-entered form -- and two such requests inside one wall-clock second would
// carry one signature, the second refused here as a replay rather than answered
// idempotently. The console's signing transport (internal/forahttpsig)
// therefore puts a fresh nonce on every signature, which keeps each signature
// distinct without widening what a captured one can do.
const consoleReplayTTL = 5 * time.Minute

// verifyConsoleSignature runs the SDK's RFC 9421 verification over the buffered
// body, then records the signature so the same bytes cannot be sent twice.
//
// Replay is checked after verification rather than before: recording the
// signature of a request that turns out not to verify would let an attacker
// burn the replay key of a signature it captured but could not forge.
func verifyConsoleSignature(
	r *http.Request, body []byte, resolver helpers.KeyResolver, replay ConsoleReplayStore,
) error {
	if resolver == nil {
		return errors.New("console signature: no key resolver configured")
	}
	// The lifetime clamp is what makes the replay record sufficient: a
	// signature can live no longer than the record of it, so it cannot be
	// replayed after the record has expired, whatever window the signer chose.
	v, err := helpers.VerifyRequestResolved(r.Context(), r, body, resolver,
		helpers.VerifyOptions{MaxSignatureAge: consoleReplayTTL})
	if err != nil {
		return err
	}
	if replay == nil {
		return nil
	}
	seen, err := replay.SeenOrAdd(r.Context(), v.KeyID, v.Signature, consoleReplayTTL)
	if err != nil {
		return err
	}
	if seen {
		return errors.New("console signature: replayed")
	}
	return nil
}

// ConsoleDirectoryOrigin is the https origin of the console's key directory:
// the scheme and host of the configured directory URL. A URL that is not https,
// or names no origin, is refused: a key directory is only fetched over https,
// and the console's signatures name their directory as an https origin, so
// such a configuration could match no console signature at all.
func ConsoleDirectoryOrigin(directoryURL string) (string, error) {
	u, err := url.Parse(directoryURL)
	if err != nil {
		return "", fmt.Errorf("console directory %q is not a URL: %w", directoryURL, err)
	}
	origin := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
	if err := helpers.CheckHTTPSOrigin(origin); err != nil {
		return "", fmt.Errorf("console directory %q does not name an https origin: %w", directoryURL, err)
	}
	return origin, nil
}

// ConsoleKeyResolver builds the resolver the console surface verifies against.
// The signature's key is resolved in the directory its own Signature-Agent
// member names, like every other signature's, and the member must be the ONE
// console origin this deployment configured (ConsoleDirectoryOrigin); a
// signature naming any other directory resolves no key here.
//
// Refusing every other member is the point. A caller signing with a key from a
// directory of its own choosing would otherwise verify, and the Exchange would
// then accept whatever actor that caller asserted.
func ConsoleKeyResolver(origin string, inner helpers.KeyResolver) helpers.KeyResolver {
	return keypolicy.ResolverFunc(func(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
		if member := helpers.SignatureAgentFromContext(ctx); member != origin {
			return nil, fmt.Errorf("%w: signature names directory %q, not the configured console %q",
				helpers.ErrUnknownKey, member, origin)
		}
		return inner.Resolve(helpers.WithSignatureAgent(ctx, origin), keyID)
	})
}

// decodeConsoleRequest reads one JSON body into dst.
//
// Unknown fields are refused rather than ignored. The console and the Exchange
// ship together, so a field the Exchange does not know is a version skew, and
// silently dropping it would let the console believe it registered a path scope
// or a preset the Exchange never stored.
func decodeConsoleRequest(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return exchange.Wrap(exchange.KindInvalidRequest, err, "malformed request body")
	}
	return nil
}

// consoleCallerOf builds the caller from the actor a request asserted. Every
// request carries one, and it is passed through whole: whether the actor may
// act at all, an unverified address included, is the service's decision.
func consoleCallerOf(r *http.Request, a consoleapi.Actor) service.ConsoleCaller {
	return service.ConsoleCaller{
		Actor: service.Actor{
			OIDCIssuer:    a.OIDCIssuer,
			OIDCSubject:   a.OIDCSubject,
			Email:         a.Email,
			EmailVerified: a.EmailVerified,
		},
		SourceAddr: iplimit.ClientIP(r, false),
		RequestID:  reqctx.RequestID(r.Context()),
	}
}

// decodeConsoleCall is the two steps every route takes before the service:
// read the body, then build the caller from the actor inside it.
func decodeConsoleCall(r *http.Request, dst any, actor func() consoleapi.Actor) (service.ConsoleCaller, error) {
	if err := decodeConsoleRequest(r, dst); err != nil {
		return service.ConsoleCaller{}, err
	}
	return consoleCallerOf(r, actor()), nil
}

// writeConsoleJSON writes one successful response.
func writeConsoleJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// writeConsoleError maps a domain error to its HTTP status and body.
//
// The status is derived from Kind.ConnectCode, the one place the Exchange
// decides what each kind means on the wire, so this surface and the Connect
// handlers cannot answer one kind two ways. The body carries the kind as a
// token, which is what the console decides whose fault a refusal is from.
//
// A server-side failure is logged at error level and answered with a fixed
// sentence: its message is the operator's diagnosis, not something to show a
// publisher. An error that is not a domain error is a server fault and says so,
// without echoing its text to the caller.
func writeConsoleError(w http.ResponseWriter, r *http.Request, err error) {
	de, ok := errors.AsType[*exchange.Error](err)
	if !ok {
		reqctx.FromContext(r.Context()).ErrorContext(r.Context(), "console.request_failed",
			"path", r.URL.Path, "err", err)
		writeConsoleRefusal(w, http.StatusInternalServerError, exchange.KindInternal, "internal error")
		return
	}
	status := consoleStatusFor(de.Kind)
	if status >= http.StatusInternalServerError {
		reqctx.FromContext(r.Context()).ErrorContext(r.Context(), "console.request_failed",
			"path", r.URL.Path, "kind", de.Kind.String(), "err", err)
		writeConsoleRefusal(w, status, de.Kind, "internal error")
		return
	}
	reqctx.FromContext(r.Context()).InfoContext(r.Context(), "console.request_refused",
		"path", r.URL.Path, "kind", de.Kind.String())
	writeConsoleRefusal(w, status, de.Kind, de.Message)
}

// writeConsoleRefusal writes one refusal in the shape the console reads
// (consoleapi.ErrorBody): a status, the kind, and one sentence.
func writeConsoleRefusal(w http.ResponseWriter, status int, kind exchange.Kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(consoleapi.ErrorBody{Error: msg, Kind: kind.String()})
}

// consoleStatusFor is the HTTP status a kind is answered with on this surface:
// the status the Connect protocol assigns to the kind's Connect code. The
// surface is plain JSON rather than Connect, so it cannot use Connect's own
// writer, but it uses Connect's table so the two agree.
func consoleStatusFor(kind exchange.Kind) int {
	if status, ok := connectHTTPStatus[kind.ConnectCode()]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// connectHTTPStatus is the Connect protocol's code-to-status table, as the
// Connect specification states it. A code missing here answers 500, which is
// what the specification gives Unknown.
var connectHTTPStatus = map[connect.Code]int{
	connect.CodeCanceled:           499,
	connect.CodeInvalidArgument:    http.StatusBadRequest,
	connect.CodeDeadlineExceeded:   http.StatusGatewayTimeout,
	connect.CodeNotFound:           http.StatusNotFound,
	connect.CodeAlreadyExists:      http.StatusConflict,
	connect.CodePermissionDenied:   http.StatusForbidden,
	connect.CodeResourceExhausted:  http.StatusTooManyRequests,
	connect.CodeFailedPrecondition: http.StatusBadRequest,
	connect.CodeAborted:            http.StatusConflict,
	connect.CodeOutOfRange:         http.StatusBadRequest,
	connect.CodeUnimplemented:      http.StatusNotImplemented,
	connect.CodeInternal:           http.StatusInternalServerError,
	connect.CodeUnavailable:        http.StatusServiceUnavailable,
	connect.CodeDataLoss:           http.StatusInternalServerError,
	connect.CodeUnauthenticated:    http.StatusUnauthorized,
}
