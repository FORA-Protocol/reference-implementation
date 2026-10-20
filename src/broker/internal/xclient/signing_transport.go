package xclient

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraauth"
)

// RelayKey holds the Broker's outbound-relay Ed25519 keypair. After the WBA
// split the relay's RFC 9421 keyid is the RFC 7638 thumbprint of its public key
// (proof of key possession); its identity as a relay is the broker's directory
// origin, carried in the covered Signature-Agent member — not a kid prefix.
type RelayKey struct {
	// KeyID is the RFC 7638 thumbprint of Private's public key — the RFC 9421
	// keyid the relay signs with.
	KeyID   string
	Private ed25519.PrivateKey
}

// relayKeyFile mirrors the JSON written by scripts/gen-broker-relay-key.sh.
// A legacy kid field is ignored: the keyid is derived from the public key.
type relayKeyFile struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

// LoadRelayKey reads the broker-relay private key from path. The file shape is
// the same as deploy/mcp/agent-key.json: a base64url-encoded Ed25519 seed (+
// derived pubkey). The relay's keyid is the RFC 7638 thumbprint of the derived
// public key; any kid in the file is ignored.
func LoadRelayKey(path string) (*RelayKey, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-controlled path
	if err != nil {
		return nil, fmt.Errorf("xclient: read broker relay key %s: %w", path, err)
	}
	var doc relayKeyFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("xclient: decode broker relay key %s: %w", path, err)
	}
	seed, err := base64.RawURLEncoding.DecodeString(doc.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("xclient: decode private_key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("xclient: private_key length %d != %d", len(seed), ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	keyid, err := helpers.Thumbprint(priv.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, fmt.Errorf("xclient: relay key thumbprint: %w", err)
	}
	return &RelayKey{KeyID: keyid, Private: priv}, nil
}

// Signer returns the relay key as an SDK signer, keyid its RFC 7638
// thumbprint. The Broker's key directory lists the relay key, so the
// directory's response is signed by it too.
func (k *RelayKey) Signer() (helpers.Signer, error) {
	return helpers.NewEd25519Signer(k.KeyID, k.Private)
}

// NewSigningTransport wraps inner so every outbound Broker→Exchange /fora.*
// request carries the Broker's Web Bot Auth request signature. It composes the
// SDK's signing RoundTripper (sdk/go/core.NewSigningTransport) so the signed
// wire bytes — covered components, content-digest, the Signature-Agent
// dictionary member, Signature-Input, and the signature — are identical to
// every other FORA client the Exchange's verifier mirrors.
//
// The Broker signs alone. It never forwards an agent's request unchanged:
// Resolve's fan-out queries are requests the Broker originates, and each
// purchase sub-request is a re-packaged request the Broker authors, so there is
// no earlier signature for the Broker's to cover. Each request therefore carries
// one signature, sig1, covering the request and the Broker's own member only.
// The agent's identity and consent on a relayed purchase travel in the body as
// its AgentAcceptance, which the Exchange verifies against the agent's own
// directory.
//
// The SDK transport puts a fresh nonce on every signature it generates, which
// keeps identical back-to-back requests from colliding in the Exchange's replay
// store; the window is the SDK's clock window over clk, so created is the
// clock's now. origin is the Broker's own https origin (BROKER_PUBLIC_ORIGIN), the
// Signature-Agent member every signature names and the origin whose directory
// the Exchange resolves the relay key in; a value that is not an https origin
// is refused here, before anything is signed. ttl caps the signature lifetime
// (recommended 30s, at most the SDK's five-minute ceiling); inner nil defaults
// to http.DefaultTransport; clk is the time source (pass clock.System{} in
// production wiring, nil defaults to it).
func NewSigningTransport(
	inner http.RoundTripper, key *RelayKey, origin string, ttl time.Duration, clk clock.Clock,
) (http.RoundTripper, error) {
	if err := helpers.CheckHTTPSOrigin(origin); err != nil {
		return nil, fmt.Errorf("xclient: broker signing origin: %w", err)
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if clk == nil {
		clk = clock.System{}
	}
	signer, err := helpers.NewEd25519Signer(key.KeyID, key.Private)
	if err != nil {
		return nil, fmt.Errorf("xclient: build relay signer: %w", err)
	}
	return core.NewSigningTransport(signer, inner,
		core.WithSignPredicate(foraauth.IsFORAProcedure),
		core.WithSignatureAgent(origin),
		core.WithWindow(core.ClockWindow(clk.Now, ttl)),
	), nil
}
