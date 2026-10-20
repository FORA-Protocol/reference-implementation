// Package forawellknown is the single consumer/producer library for the FORA
// discovery surface, which after the WBA split spans two files every FORA
// participant (agent, broker, exchange, publisher) serves:
//
//   - the pure Web Bot Auth directory at /.well-known/http-message-signatures-directory
//     (a fora.v1.WBAFile — a JOSE JWK Set plus a directory-level revocation_url,
//     zero FORA-specific fields), and
//   - the FORA commercial overlay at /.well-known/fora.json (a
//     fora.v1.WellKnownManifest — role, authorized exchanges/contributors,
//     exchange capability fields; no identity keys).
//
// Keys live ONLY in the WBA file and are referenced elsewhere by RFC 7638
// thumbprint (the RFC 9421 keyid), never republished in the overlay. Both wire
// shapes are protojson with UseProtoNames=true — snake_case field names
// (not_before, revocation_url) and full enum value names (ROLE_PUBLISHER,
// PROVIDER_RELATIONSHIP_DIRECT). See the embedded schemas under schema/.
//
// The library provides:
//   - Fetch / FetchDocument: GET + version gate + schema-validate +
//     protojson.Unmarshal a remote overlay manifest.
//   - Cache: TTL-bounded, single-flighted overlay-manifest cache keyed by domain.
//   - NewDirectoryFetcher: how this repository reads a WBA directory it does not
//     serve, the SDK's directory fetcher over the caller's client, clock and
//     time limit.
//   - ParseWBA / ParseManifest / Validate*: the schema checks for documents a
//     producer builds or a test round-trips.
//   - NewKey / Thumbprint / PublicKey: key encoding and decoding over a
//     WBAFile's key set.
//   - server.Handler / server.WBAHandler (subpackage): builds + serves the
//     overlay manifest and WBA directory for a role.
package forawellknown

import (
	"fmt"
	"net/url"
	"strings"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
)

// Version is the manifest document version this library stamps on every
// /.well-known/fora.json it produces. It is the SDK's WellKnownManifestVersion —
// the one owner of the value — and NOT helpers.ProtocolVersion, which versions
// the RPC envelope. The two are separate namespaces that happen to read the same
// today: a change to the manifest layout bumps both, a protocol change that
// leaves the manifest untouched bumps only the envelope's, and no code may stamp
// one from the other.
//
// On the consumer side this library applies the SDK's receive-side rule before
// it reads any other member of a document: a manifest whose ver carries the
// major of this constant is accepted whatever its minor, and one carrying an
// unrecognised major, a value that is not MAJOR.MINOR, or no ver at all is
// refused with ErrVersionRefused. See ValidateManifest.
const Version = helpers.WellKnownManifestVersion

// WBAMediaType is the media type a key directory is served under. A directory
// served as anything else, the JWK Set type included, is refused.
const WBAMediaType = resolvers.WBADirectoryMediaType

// Path is the fixed request path every FORA participant serves the commercial
// overlay manifest at.
const Path = "/.well-known/fora.json"

// WBAPath is the fixed request path every FORA participant serves its pure Web
// Bot Auth directory (WBAFile) at. It is the standard WBA directory location,
// readable by any off-the-shelf WBA verifier.
const WBAPath = "/.well-known/http-message-signatures-directory"

// RevocationPath is the conventional request path a participant serves its
// KeyRevocationList at, advertised to peers via WBAFile.revocation_url.
// Producers that serve a revocation channel mount it here; routing both the WBA
// directory and the revocation route off these constants keeps a path typo a
// compile error rather than an E2E-only failure.
const RevocationPath = "/.well-known/fora-key-revocations.json"

// Manifest is the FORA commercial overlay document. Alias (not a fresh type) so
// the generated proto accessors (GetRole, GetExchanges, …) are available.
type Manifest = forav1.WellKnownManifest

// WBAFile is the Web Bot Auth directory: a JWK Set (keys) plus an optional
// directory-level revocation_url. Identity keys live here, never in Manifest.
type WBAFile = forav1.WBAFile

// Key is a single Ed25519 JWK within a WBAFile's key set.
type Key = forav1.JsonWebKey

// RevocationList is the revocation snapshot served at WBAFile.revocation_url.
// Its revoked entries are RFC 7638 thumbprints (base64url-no-pad).
type RevocationList = forav1.KeyRevocationList

// Role identifies which kind of participant a Manifest describes.
type Role = forav1.Role

// Role constants re-exported so callers need not import the proto package.
const (
	RoleUnspecified = forav1.Role_ROLE_UNSPECIFIED
	RoleAgent       = forav1.Role_ROLE_AGENT
	RoleExchange    = forav1.Role_ROLE_EXCHANGE
	RoleBroker      = forav1.Role_ROLE_BROKER
	RolePublisher   = forav1.Role_ROLE_PUBLISHER
)

// ManifestURL builds the absolute /.well-known/fora.json (commercial overlay)
// URL for a host. See wellKnownURL for the host-parsing contract.
func ManifestURL(host, scheme, port string) (string, error) {
	return wellKnownURL(host, scheme, port, Path)
}

// RevocationURL builds the absolute /.well-known/fora-key-revocations.json
// (KeyRevocationList) URL for a host — the value a producer advertises in
// WBAFile.revocation_url. A verifier (the SDK's WBAKeyResolver) host-anchors the
// advertised URL to the directory's own host and skips a cross-host one, so a producer serving a
// wildcard zone of per-agent hosts MUST derive this per host, not share one value.
// See wellKnownURL for the host-parsing contract.
func RevocationURL(host, scheme, port string) (string, error) {
	return wellKnownURL(host, scheme, port, RevocationPath)
}

// wellKnownURL builds the absolute URL for a fixed well-known path on a host.
// host may be a bare domain ("publisher.example"), a host:port, or a full origin
// ("https://publisher.example"). scheme defaults to "https" when host carries
// no scheme; port, when non-empty, is appended to a bare host (local/compose
// stacks serve on a non-default port).
func wellKnownURL(host, scheme, port, path string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("%w: empty host", ErrInvalidHost)
	}
	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrInvalidHost, err)
		}
		if u.Host == "" {
			return "", fmt.Errorf("%w: no host in %q", ErrInvalidHost, host)
		}
		u.Path = path
		u.RawQuery = ""
		return u.String(), nil
	}
	if scheme == "" {
		scheme = "https"
	}
	authority := host
	if port != "" && !strings.Contains(host, ":") {
		authority = host + ":" + port
	}
	return scheme + "://" + authority + path, nil
}
