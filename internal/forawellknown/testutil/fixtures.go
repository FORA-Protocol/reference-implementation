// Package testutil provides shared fixtures for forawellknown tests: seeded
// (deterministic) signing keys, overlay-manifest / WBA-directory / revocation
// builders, and an httptest origin that serves all three documents. Keys are
// derived from a seed label so fixtures are reproducible and never depend on a
// non-deterministic RNG.
package testutil

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
)

// NewSigningKey returns a deterministic Ed25519 private key plus its published
// JWK, valid over [notBefore, notAfter). The seed is derived from the seed label
// so the same label always yields the same key across test runs. Keys carry no
// kid — they are identified by their RFC 7638 thumbprint (forawellknown.Thumbprint).
func NewSigningKey(seed string, notBefore, notAfter time.Time) (ed25519.PrivateKey, *forawellknown.Key) {
	raw := make([]byte, ed25519.SeedSize)
	copy(raw, seed)
	priv := ed25519.NewKeyFromSeed(raw)
	pub, _ := priv.Public().(ed25519.PublicKey)
	return priv, forawellknown.NewKey(pub, notBefore, notAfter)
}

// Manifest assembles a keyless commercial-overlay manifest value with the given
// role/domain; callers mutate the returned message for role-specific fields
// before marshaling. Identity keys live in the WBA directory (see WBAFile).
func Manifest(role forawellknown.Role, domain string) *forawellknown.Manifest {
	return &forawellknown.Manifest{
		Ver:    forawellknown.Version,
		Role:   role,
		Domain: domain,
	}
}

// ExchangeManifest renders the document an Exchange serves about itself: role,
// its own domain, and the endpoint it advertises.
//
// Built through the protobuf message rather than written out as a JSON literal,
// which is the whole reason it exists. A literal is not checked against the
// schema, so a field renamed upstream leaves it serving a document the resolver
// reads and ignores — and the test that depended on the endpoint then fails
// somewhere else entirely, or worse, passes.
//
// The endpoint is a caller's argument because the rule it has to satisfy is the
// point of most tests using this: an endpoint may name only the host and port
// that served the manifest, or a subdomain of that host. Passing the serving
// origin is the production shape; passing anything else is how the refusal is
// driven.
//
// It DELEGATES rather than assembling a second message of its own. The two used
// to build the same document side by side, so a member every Exchange manifest
// should carry could be added to one and not the other while the sibling's
// comment went on claiming they matched.
func ExchangeManifest(domain, endpoint string) []byte {
	return ExchangeManifestWithRegistration(domain, endpoint, Registration{})
}

// ExchangeManifestWithHealth is ExchangeManifest naming health as the
// manifest's health_endpoint, the URL a Broker probes to decide whether the
// Exchange is answering. health is published exactly as given, so a test can
// name an absolute URL or a relative one.
func ExchangeManifestWithHealth(domain, endpoint, health string) []byte {
	m := exchangeManifest(domain, endpoint, Registration{})
	m.HealthEndpoint = &health
	return MarshalManifest(m)
}

// ExchangeManifestAtVersion is ExchangeManifest with the document version the
// caller names in place of the one this implementation stamps. It exists to
// drive the consumer-side version gate — a manifest at a major this reader does
// not implement, or with no version at all (pass "") — through the real
// resolvers, with every other member exactly as production would serve it, so
// the refusal can only be the version. That promise holds because the members
// come from exchangeManifest, the one assembler every Exchange fixture shares;
// only Ver is overwritten here.
func ExchangeManifestAtVersion(domain, endpoint, ver string) []byte {
	m := exchangeManifest(domain, endpoint, Registration{})
	m.Ver = ver
	return MarshalManifest(m)
}

// Registration is the optional registration half of an Exchange manifest: what
// the Exchange asks a registration to carry, and which terms revision
// submitting one accepts.
//
// DataSchemaJSON is the schema AS THE OPERATOR WROTE IT, and it travels through
// a protojson round trip on its way into the document, exactly as it does in
// production. That matters for any test about the size cap, which the protocol
// measures over the served bytes rather than over what was configured.
type Registration struct {
	DataSchemaJSON string
	TermsURI       string
	TermsDigest    string
}

// ExchangeManifestWithRegistration renders the document an Exchange serves about
// itself, carrying the registration members as well. An empty field is left
// absent, so one fixture covers "publishes a schema", "publishes terms", and
// every combination — including the pass-through case, which is what a manifest
// with none of them means, and which is what ExchangeManifest asks for.
func ExchangeManifestWithRegistration(domain, endpoint string, reg Registration) []byte {
	return MarshalManifest(exchangeManifest(domain, endpoint, reg))
}

// exchangeManifest is the ONE assembler of an Exchange's manifest message. Every
// exported Exchange fixture marshals what it returns, and none builds a message
// of its own: a member every Exchange manifest should carry is added here, once,
// and cannot be added to one fixture while a sibling's comment goes on claiming
// they match.
func exchangeManifest(domain, endpoint string, reg Registration) *forawellknown.Manifest {
	m := Manifest(forawellknown.RoleExchange, domain)
	m.Endpoint = &endpoint
	if reg.DataSchemaJSON != "" {
		schema := &structpb.Struct{}
		if err := protojson.Unmarshal([]byte(reg.DataSchemaJSON), schema); err != nil {
			panic("forawellknown/testutil: registration data_schema is not a JSON object: " + err.Error())
		}
		m.AccountRegistration = &forav1.AccountRegistration{DataSchema: schema}
	}
	if reg.TermsURI != "" {
		m.TermsUri = &reg.TermsURI
	}
	if reg.TermsDigest != "" {
		m.TermsDigest = &reg.TermsDigest
	}
	return m
}

// WBAFile assembles a WBA directory carrying the given keys; callers set
// RevocationUrl before marshaling when exercising the revocation channel.
func WBAFile(keys ...*forawellknown.Key) *forawellknown.WBAFile {
	return &forawellknown.WBAFile{Keys: keys}
}

// MarshalManifest renders m as canonical protojson (snake_case, full enums).
func MarshalManifest(m *forawellknown.Manifest) []byte {
	raw, _ := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(m)
	return raw
}

// MarshalWBA renders a WBA directory as canonical protojson.
func MarshalWBA(f *forawellknown.WBAFile) []byte {
	raw, _ := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(f)
	return raw
}

// MarshalRevocation renders a KeyRevocationList (as_of + revoked thumbprints)
// as canonical protojson.
func MarshalRevocation(asOf time.Time, revoked ...string) []byte {
	list := &forav1.KeyRevocationList{
		AsOf:    timestamppb.New(asOf.UTC()),
		Revoked: revoked,
	}
	raw, _ := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(list)
	return raw
}

// PublisherExchange builds an AuthorizedExchange entry for a publisher manifest.
func PublisherExchange(domain, endpoint string, rel forav1.ProviderRelationship) *forav1.AuthorizedExchange {
	return &forav1.AuthorizedExchange{Domain: domain, Endpoint: endpoint, Relationship: rel}
}

// Client returns the HTTP client the forawellknown tests use in place of
// http.DefaultClient. httptest.Server.Close() calls
// http.DefaultTransport.CloseIdleConnections() as a convenience, which drops
// every idle keep-alive connection pooled on the shared global transport. With
// the package's parallel tests each running an Origin and deferring Close(), one
// test's Close() can close an idle connection a concurrent test is reusing,
// surfacing as "connection broken: http: CloseIdleConnections called". A private
// transport with keep-alives disabled keeps no idle pool to race and never
// touches the global transport, so no peer's Close() can disturb an in-flight
// request.
func Client() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
}

// endpoint is one swappable well-known document endpoint: a body, an optional
// forced status code, an optional Cache-Control header, a request gate (to force
// concurrent callers to overlap inside a single in-flight fetch), and a hit
// counter. The manifest and WBA endpoints share this machinery.
type endpoint struct {
	contentType  string
	doc          atomic.Pointer[[]byte]
	status       atomic.Int32
	cacheControl atomic.Pointer[string]
	gate         atomic.Pointer[chan struct{}]
	hits         atomic.Int64
	// signers, when set, sign the response the way a key directory's is signed:
	// once per signer, covering the authority the request named and the body's
	// digest (helpers.SignDirectoryResponse).
	signers atomic.Pointer[[]helpers.Signer]
	// mediaType, when set, replaces contentType.
	mediaType atomic.Pointer[string]
}

// directorySignedFrom and directorySignedUntil bound the fixture's directory
// response signatures: 2001 to 2096, so a reader on a deterministic clock
// anywhere in between accepts them.
const (
	directorySignedFrom  = 1_000_000_000
	directorySignedUntil = 4_000_000_000
)

func (e *endpoint) serve(w http.ResponseWriter, r *http.Request) {
	if g := e.gate.Load(); g != nil {
		<-*g
	}
	e.hits.Add(1)
	if code := e.status.Load(); code != 0 {
		w.WriteHeader(int(code))
		return
	}
	if cc := e.cacheControl.Load(); cc != nil {
		w.Header().Set("Cache-Control", *cc)
	}
	var body []byte
	if p := e.doc.Load(); p != nil {
		body = *p
	}
	if s := e.signers.Load(); s != nil {
		// A window wide enough for any clock a test injects, real or
		// deterministic: the reader judges the signature against its own clock.
		sig, err := helpers.SignDirectoryResponse(r.Context(), strings.ToLower(r.Host), body, *s,
			directorySignedFrom, directorySignedUntil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sig.Apply(w.Header())
	}
	ct := e.contentType
	if mt := e.mediaType.Load(); mt != nil {
		ct = *mt
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(body)
}

func (e *endpoint) block() (release func()) {
	ch := make(chan struct{})
	e.gate.Store(&ch)
	var once sync.Once
	return func() {
		once.Do(func() {
			e.gate.Store(nil)
			close(ch)
		})
	}
}

// Origin is an httptest server that serves an overlay manifest at
// /.well-known/fora.json, a WBA directory at
// /.well-known/http-message-signatures-directory, and a revocation list at
// RevocationPath. Every document is swappable mid-test; the manifest and WBA
// endpoints can force a status code (Set*Status), the manifest endpoint can emit
// a Cache-Control header (SetCacheControl), count requests (Hits), and be gated
// (Block) to force concurrent callers to overlap inside one in-flight fetch. The
// WBA endpoint can be gated too (BlockWBA), so a reader's time limit can be
// driven against a directory that never answers.
type Origin struct {
	*httptest.Server
	manifest   *endpoint
	wba        *endpoint
	revocation atomic.Pointer[[]byte]
}

// RevocationPath is where Origin serves its KeyRevocationList. Re-exported from
// the production constant so fixtures and producers share one source.
const RevocationPath = forawellknown.RevocationPath

// NewOrigin starts an Origin serving the given initial manifest bytes over plain
// http. The WBA and revocation documents start empty; set them with SetWBA /
// SetRevocation. A key directory is only ever fetched over https, so a test
// that reads the directory uses NewTLSOrigin.
func NewOrigin(manifest []byte) *Origin {
	o := newOrigin(manifest)
	o.Server = httptest.NewServer(o.mux())
	return o
}

// NewTLSOrigin is NewOrigin over https, the only scheme a key directory is
// fetched over. Its URL is https://127.0.0.1:<port>; HTTPClient returns a client
// that trusts the origin's certificate.
func NewTLSOrigin(manifest []byte) *Origin {
	o := newOrigin(manifest)
	o.Server = httptest.NewTLSServer(o.mux())
	return o
}

func newOrigin(manifest []byte) *Origin {
	o := &Origin{
		manifest: &endpoint{contentType: "application/json"},
		wba:      &endpoint{contentType: forawellknown.WBAMediaType},
	}
	o.SetManifest(manifest)
	return o
}

func (o *Origin) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc(forawellknown.Path, o.manifest.serve)
	mux.HandleFunc(forawellknown.WBAPath, o.wba.serve)
	mux.HandleFunc(RevocationPath, o.serveRevocation)
	return mux
}

// HTTPClient returns a client for this origin: for a TLS origin one that trusts
// its certificate, with keep-alives disabled for the reason Client gives.
func (o *Origin) HTTPClient() *http.Client {
	if o.TLS == nil {
		return Client()
	}
	tr := o.Server.Client().Transport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	return &http.Client{Transport: tr}
}

// DirectoryOrigin is the origin as a Signature-Agent member names it:
// https://127.0.0.1:<port> for a TLS origin.
func (o *Origin) DirectoryOrigin() string { return o.URL }

// SetWBASigners makes the WBA directory's response carry one response
// signature per key, as every key directory's must. Each key signs under its
// RFC 7638 thumbprint. With none set, the directory is served unsigned, which a
// reader refuses.
func (o *Origin) SetWBASigners(privs ...ed25519.PrivateKey) {
	if len(privs) == 0 {
		o.wba.signers.Store(nil)
		return
	}
	signers := make([]helpers.Signer, 0, len(privs))
	for _, priv := range privs {
		signers = append(signers, DirectorySigner(priv))
	}
	o.wba.signers.Store(&signers)
}

// SetWBAContentType swaps the media type the WBA directory is served under, so a
// test can drive the refusal of a directory served as anything but the
// directory media type.
func (o *Origin) SetWBAContentType(mediaType string) { o.wba.mediaType.Store(&mediaType) }

// DirectorySigner is priv as the signer of a key directory response: keyid the
// RFC 7638 thumbprint of its public half.
func DirectorySigner(priv ed25519.PrivateKey) helpers.Signer {
	signer, err := helpers.NewEd25519Signer(MustThumbprintPriv(priv), priv)
	if err != nil {
		panic("forawellknown/testutil: directory signer: " + err.Error())
	}
	return signer
}

// SetManifest swaps the served overlay manifest document.
func (o *Origin) SetManifest(b []byte) { o.manifest.doc.Store(&b) }

// SetWBA swaps the served WBA directory document.
func (o *Origin) SetWBA(b []byte) { o.wba.doc.Store(&b) }

// SetRevocation swaps the served revocation document.
func (o *Origin) SetRevocation(b []byte) { o.revocation.Store(&b) }

// SetManifestStatus forces the manifest endpoint to return code (0 resets to 200).
func (o *Origin) SetManifestStatus(code int) {
	o.manifest.status.Store(int32(code)) //nolint:gosec // small status code
}

// SetWBAStatus forces the WBA endpoint to return code (0 resets to 200).
func (o *Origin) SetWBAStatus(code int) { o.wba.status.Store(int32(code)) } //nolint:gosec // small status code

// SetCacheControl sets the Cache-Control header the manifest endpoint emits on a
// 2xx response (empty clears it). Exercises the Cache's max-age TTL clamping.
func (o *Origin) SetCacheControl(value string) {
	if value == "" {
		o.manifest.cacheControl.Store(nil)
		return
	}
	o.manifest.cacheControl.Store(&value)
}

// Hits reports how many times the manifest endpoint has produced a response.
func (o *Origin) Hits() int64 { return o.manifest.hits.Load() }

// WBAHits reports how many times the WBA-directory endpoint has produced a
// response — used to assert a burst of unknown-keyid lookups coalesced into a
// bounded number of directory fetches.
func (o *Origin) WBAHits() int64 { return o.wba.hits.Load() }

// Block arms the manifest gate: every manifest request blocks until the returned
// release is called, so concurrent callers provably overlap inside a single
// in-flight fetch (single-flight). release is idempotent.
func (o *Origin) Block() (release func()) { return o.manifest.block() }

// BlockWBA arms the WBA directory gate: every directory request blocks until the
// returned release is called, so a reader's time limit fires against a directory
// that never answers. The gate ignores the request's context, and Close waits
// for in-flight handlers, so a test registers t.Cleanup(release) after
// t.Cleanup(o.Close): cleanups run last-in first-out, and release must run
// first. release is idempotent.
func (o *Origin) BlockWBA() (release func()) { return o.wba.block() }

// RevocationURL is the absolute URL clients poll for the revocation list.
func (o *Origin) RevocationURL() string { return o.URL + RevocationPath }

// Host is the origin's "host:port", which is the shape a domain-valued FORA
// field carries — URL, the embedded server's own field, carries a scheme and is
// not that shape. A test driving a caller that takes a domain passes this; one
// driving a caller that takes a URL passes URL.
func (o *Origin) Host() string { return o.Listener.Addr().String() }

func (o *Origin) serveRevocation(w http.ResponseWriter, _ *http.Request) {
	p := o.revocation.Load()
	if p == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(*p)
}

// Ptr returns a pointer to v; a small helper for optional proto fields in tests.
func Ptr[T any](v T) *T { return &v }
