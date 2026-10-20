package testutil

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

// The edge double's check of one delivery proof, written out independently of
// the SDK's verifiers (see EdgeDouble). It accepts what they accept: a signature
// covering at least @method, @target-uri and the agent's Signature-Agent
// reference, in any order and with anything else beside them; the parameters in
// any order; tag "web-bot-auth"; and an alg that is absent or ed25519 in any
// letter case, an empty one refused. The refusal tokens are the production
// edge's, so a test can assert the exact string an agent would see.

// proof is one bound delivery request as the edge sees it: the method, the
// verbatim target URI, the agent_id the URL carries, and the headers.
type proof struct {
	method, targetURI, agentID string
	header                     http.Header
}

// proofLabel is the signature label every FORA signer writes on a delivery
// proof. A proof carries one signature, so the double reads that member only.
const proofLabel = "sig1"

// param is one RFC 8941 parameter: a key and its value, a quoted string
// unquoted or an integer as written.
type param struct {
	key, value string
}

// component is one entry of a proof's covered list, as written.
type component struct {
	name   string
	params []param
}

// render writes the component the way it opens its signature-base line.
func (c component) render() string {
	var b strings.Builder
	b.WriteString(`"` + c.name + `"`)
	for _, p := range c.params {
		b.WriteString(";" + p.key + `="` + p.value + `"`)
	}
	return b.String()
}

// proofInput is the proof's Signature-Input member: what it covers, its
// parameters, and the member value verbatim, which closes the signature base.
type proofInput struct {
	covered  []component
	params   map[string]string
	rawInner string
}

// verifyProof is the edge's check of one proof at the verifier time now. It
// returns the refusal token, or "" when the proof holds.
func verifyProof(now int64, p proof) string {
	pub, reason := presentedKey(p.header)
	if reason != "" {
		return reason
	}
	in, reason := parseProofInput(p.header.Get("Signature-Input"))
	if reason != "" {
		return reason
	}
	if in.params["tag"] != helpers.WBATag {
		return "bad_tag"
	}
	if alg, present := in.params["alg"]; present && !strings.EqualFold(alg, "ed25519") {
		return "unsupported_alg"
	}
	ref, ok := in.agentReference()
	if !ok {
		return "bad_covered_components"
	}
	origin, reason := proofAgent(p.header, ref)
	if reason != "" {
		return reason
	}
	if reason = freshness(now, in.params["created"], in.params["expires"]); reason != "" {
		return reason
	}
	if reason = identityAgrees(p.agentID, in.params["keyid"], pub); reason != "" {
		return reason
	}
	sig, reason := presentedSignature(p.header)
	if reason != "" {
		return reason
	}
	base, ok := signatureBase(p, in, origin)
	if !ok {
		return "bad_covered_components"
	}
	if !ed25519.Verify(pub, []byte(base), sig) {
		return "pop_sig_invalid"
	}
	return ""
}

// parseProofInput reads the proofLabel member of a Signature-Input header: an
// inner list of quoted component names, each with optional parameters, then the
// signature parameters.
func parseProofInput(header string) (proofInput, string) {
	if strings.TrimSpace(header) == "" {
		return proofInput{}, "missing_sig"
	}
	inner, ok := strings.CutPrefix(strings.TrimSpace(header), proofLabel+"=")
	if !ok {
		return proofInput{}, "malformed_sig_input"
	}
	list, ok := strings.CutPrefix(inner, "(")
	if !ok {
		return proofInput{}, "malformed_sig_input"
	}
	list, tail, ok := strings.Cut(list, ")")
	if !ok {
		return proofInput{}, "malformed_sig_input"
	}
	in := proofInput{params: map[string]string{}, rawInner: inner}
	for _, item := range strings.Fields(list) {
		c, ok := parseComponent(item)
		if !ok {
			return proofInput{}, "malformed_sig_input"
		}
		in.covered = append(in.covered, c)
	}
	params, ok := parseParams(tail)
	if !ok {
		return proofInput{}, "malformed_sig_input"
	}
	for _, p := range params {
		in.params[p.key] = p.value
	}
	return in, ""
}

// parseComponent reads one covered component: a quoted name, then parameters.
func parseComponent(item string) (component, bool) {
	rest, ok := strings.CutPrefix(item, `"`)
	if !ok {
		return component{}, false
	}
	name, rest, ok := strings.Cut(rest, `"`)
	if !ok || name == "" {
		return component{}, false
	}
	params, ok := parseParams(rest)
	if !ok {
		return component{}, false
	}
	return component{name: name, params: params}, true
}

// parseParams reads a run of `;key=value` parameters whose values are quoted
// strings or integers, the only kinds a delivery proof carries.
func parseParams(s string) ([]param, bool) {
	var out []param
	for s != "" {
		rest, ok := strings.CutPrefix(s, ";")
		if !ok {
			return nil, false
		}
		key, value, ok := strings.Cut(rest, "=")
		if !ok || key == "" {
			return nil, false
		}
		var v string
		if v, s, ok = cutValue(value); !ok {
			return nil, false
		}
		out = append(out, param{key: key, value: v})
	}
	return out, true
}

// cutValue splits a parameter value off the front of s: a quoted string,
// returned unquoted, or a run of digits.
func cutValue(s string) (value, rest string, ok bool) {
	if quoted, isQuoted := strings.CutPrefix(s, `"`); isQuoted {
		value, rest, ok = strings.Cut(quoted, `"`)
		return value, rest, ok
	}
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return s[:n], s[n:], n > 0
}

// agentReference returns the covered Signature-Agent reference when the proof
// also covers @method and @target-uri, each without parameters.
func (in proofInput) agentReference() (component, bool) {
	var method, target, found bool
	var ref component
	for _, c := range in.covered {
		switch name := strings.ToLower(c.name); {
		case name == "@method" && len(c.params) == 0:
			method = true
		case name == "@target-uri" && len(c.params) == 0:
			target = true
		case name == strings.ToLower(helpers.SignatureAgentHeader) && !found:
			ref, found = c, true
		}
	}
	return ref, method && target && found
}

// proofAgent reads the agent's directory origin out of the proof's
// Signature-Agent header, in the form the covered reference names: the
// dictionary member its key parameter names, or, with no parameter, the legacy
// quoted string a verifier still accepts on a proof carrying one signature.
// Anything that is not an https origin is refused.
//
// Known difference: the double reads a member with no parameters only, as every
// signer it meets writes; the SDK verifiers also accept a type=directory member
// parameter.
func proofAgent(h http.Header, ref component) (string, string) {
	raw := strings.TrimSpace(h.Get(helpers.SignatureAgentHeader))
	var origin string
	switch {
	case len(ref.params) == 1 && ref.params[0].key == "key":
		if member, value, ok := strings.Cut(raw, "="); ok && member == ref.params[0].value {
			origin = unquote(value)
		}
	case len(ref.params) == 0:
		origin = unquote(raw)
	}
	if origin == "" || helpers.CheckHTTPSOrigin(origin) != nil {
		return "", "bad_signature_agent"
	}
	return origin, ""
}

// unquote returns the content of an RFC 8941 string with no escapes, or "".
func unquote(s string) string {
	inner, ok := strings.CutPrefix(s, `"`)
	if !ok {
		return ""
	}
	inner, ok = strings.CutSuffix(inner, `"`)
	if !ok || strings.ContainsAny(inner, `"\`) {
		return ""
	}
	return inner
}

// signatureBase rebuilds the RFC 9421 signature base from the request as
// received, one line per covered component in the order the proof lists them,
// then the parameters line, joined with newlines and with no trailing newline.
// It reports false for a component it cannot supply.
func signatureBase(p proof, in proofInput, origin string) (string, bool) {
	lines := make([]string, 0, len(in.covered)+1)
	for _, c := range in.covered {
		value, ok := componentValue(p, c, origin)
		if !ok {
			return "", false
		}
		lines = append(lines, c.render()+": "+value)
	}
	lines = append(lines, `"@signature-params": `+in.rawInner)
	return strings.Join(lines, "\n"), true
}

// componentValue is one covered component's value: @method upper-cased,
// @target-uri verbatim (the bytes the request line carried, never re-parsed),
// @authority the lower-cased host, the Signature-Agent reference the agent's
// origin as a quoted string, and any other header as received.
func componentValue(p proof, c component, origin string) (string, bool) {
	switch name := strings.ToLower(c.name); {
	case name == "@method":
		return strings.ToUpper(p.method), true
	case name == "@target-uri":
		return p.targetURI, true
	case name == "@authority":
		u, err := url.Parse(p.targetURI)
		if err != nil || u.Host == "" {
			return "", false
		}
		return strings.ToLower(u.Host), true
	case name == strings.ToLower(helpers.SignatureAgentHeader):
		return `"` + origin + `"`, true
	case strings.HasPrefix(name, "@") || len(c.params) > 0:
		return "", false
	default:
		values := p.header.Values(name)
		if len(values) == 0 {
			return "", false
		}
		return strings.TrimSpace(strings.Join(values, ", ")), true
	}
}

// presentedKey decodes the key the fetcher offered.
func presentedKey(h http.Header) (ed25519.PublicKey, string) {
	raw := h.Get(helpers.AgentKeyHeader)
	if raw == "" {
		return nil, "missing_agent_key"
	}
	pub, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, "bad_agent_key"
	}
	return pub, ""
}

// presentedSignature decodes the RFC 8941 byte string, which is STANDARD base64
// even though the presented key beside it is base64url.
func presentedSignature(h http.Header) ([]byte, string) {
	raw, ok := strings.CutPrefix(h.Get("Signature"), proofLabel+"=:")
	if !ok || !strings.HasSuffix(raw, ":") {
		return nil, "missing_sig"
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(raw, ":"))
	if err != nil {
		return nil, "missing_sig"
	}
	return sig, ""
}

// identityAgrees enforces the three-way identity: the agent_id the Exchange
// signed into the URL, the keyid on the signature, and the thumbprint of the key
// actually presented must all be one value. Verifying the signature without this
// would let any actor present their own key plus a valid self-signature.
func identityAgrees(agentID, keyid string, pub ed25519.PublicKey) string {
	if keyid != agentID {
		return "keyid_mismatch"
	}
	thumbprint, err := helpers.Thumbprint(pub)
	if err != nil || thumbprint != agentID {
		return "thumbprint_mismatch"
	}
	return ""
}

// freshness applies the window the production edge applies: created may lead the
// verifier by at most 300s, and expires may not already have passed.
//
// The expiry comparison is INCLUSIVE — a proof whose expires lands exactly on the
// verifier's current second is stale, matching the SDK verifier's `now >= expires`
// (sdk/ts/src/pop.ts). Refusing only strictly-past proofs would make this double
// lenient where production refuses, which is the direction that lets a bad signing
// window pass every Go test and 403 at the real edge.
func freshness(now int64, createdRaw, expiresRaw string) string {
	created, err := strconv.ParseInt(createdRaw, 10, 64)
	if err != nil {
		return "pop_missing_created"
	}
	expires, err := strconv.ParseInt(expiresRaw, 10, 64)
	if err != nil {
		return "pop_missing_exp"
	}
	if created > now+300 {
		return "pop_future_created"
	}
	if expires <= now {
		return "pop_expired"
	}
	return ""
}
