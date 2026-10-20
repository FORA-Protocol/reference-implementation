package testutil

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

// popVector is one entry of testdata/pop-sign-vectors.json, whose "vectors" are
// the protocol SDK's own proof-of-possession corpus copied whole;
// scripts/check-sdk-pin-consistency.sh fails a re-pin whose copy differs from
// the pinned module's. Each vector also carries the SDK verifier's
// expected_reason, which is not read: the double orders its checks
// independently, so only its verdict is compared.
type popVector struct {
	Name           string `json:"name"`
	Method         string `json:"method"`
	URL            string `json:"url"`
	AgentID        string `json:"agent_id"`
	PresentedKey   string `json:"presented_key_b64url"`
	SignerSeedHex  string `json:"signer_seed_hex"`
	AgentDirectory string `json:"agent_directory"`
	Nonce          string `json:"nonce"`
	SignatureAgent string `json:"signature_agent"`
	SignatureInput string `json:"signature_input"`
	Signature      string `json:"signature"`
	NowUnix        int64  `json:"now_unix"`
	ExpectedValid  bool   `json:"expected_valid"`
}

func loadPoPVectors(t *testing.T) []popVector {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/pop-sign-vectors.json")
	if err != nil {
		t.Fatalf("read pop vectors: %v", err)
	}
	var doc struct {
		Vectors []popVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode pop vectors: %v", err)
	}
	if len(doc.Vectors) == 0 {
		t.Fatal("pop vectors: the corpus is empty")
	}
	return doc.Vectors
}

func (v popVector) header() http.Header {
	h := http.Header{}
	h.Set(helpers.AgentKeyHeader, v.PresentedKey)
	h.Set(helpers.SignatureAgentHeader, v.SignatureAgent)
	h.Set("Signature-Input", v.SignatureInput)
	h.Set("Signature", v.Signature)
	return h
}

// mintedCovered opens the Signature-Input of every proof the SDK signer emits:
// the dictionary member, covering exactly @method, @target-uri and that member.
const mintedCovered = `sig1=("@method" "@target-uri" "signature-agent";key="sig1");`

// mintedBySDK reports whether the vector is in the form the SDK signer emits:
// the dictionary member, the minted covered list, and an alg.
func (v popVector) mintedBySDK() bool {
	return strings.HasPrefix(v.SignatureAgent, "sig1=") &&
		strings.HasPrefix(v.SignatureInput, mintedCovered) &&
		strings.Contains(v.SignatureInput, `;alg="`)
}

// TestEdgeDouble_AcceptsExactlyTheSDKCorpusVerdicts replays every vector of
// the SDK's proof corpus through the double's check. The double must accept
// exactly the vectors the corpus marks valid — the dictionary member with and
// without a nonce, the legacy string form on one signature, a proof that also
// covers @authority or the presented key in another order, and a proof with no
// alg — and refuse the rest: an expired proof, a key that does not match
// agent_id, the v1.0.8 form that covers no Signature-Agent, a missing or wrong
// tag, a bare Signature-Agent, an http member, a superset without @method, an
// empty or unsupported alg, and an unsigned request. A double that accepted a
// refused form would let a fetcher emitting it pass every Go test and fail at
// the edge; one that refused an accepted form would fail a fetcher the edge
// serves.
func TestEdgeDouble_AcceptsExactlyTheSDKCorpusVerdicts(t *testing.T) {
	for _, v := range loadPoPVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			reason := verifyProof(v.NowUnix, proof{
				method: v.Method, targetURI: v.URL, agentID: v.AgentID, header: v.header(),
			})
			if v.ExpectedValid && reason != "" {
				t.Fatalf("the corpus marks this proof valid; the double refused it as %q", reason)
			}
			if !v.ExpectedValid && reason == "" {
				t.Fatal("the corpus marks this proof invalid; the double accepted it")
			}
		})
	}
}

// TestPoPVectors_MatchThePinnedSDKSigner signs every valid vector in the form
// the SDK mints through the pinned SDK (helpers.SignAgentBinding) and compares
// the emitted headers with the copy, so a copy that drifted from the module fails
// here rather than at an edge. The other valid vectors are forms only a verifier
// meets, a superset of the covered components or a proof with no alg, which the
// SDK signer never emits.
func TestPoPVectors_MatchThePinnedSDKSigner(t *testing.T) {
	signed := 0
	for _, v := range loadPoPVectors(t) {
		if !v.ExpectedValid || !v.mintedBySDK() {
			continue
		}
		t.Run(v.Name, func(t *testing.T) {
			seed, err := hex.DecodeString(v.SignerSeedHex)
			if err != nil {
				t.Fatalf("decode seed: %v", err)
			}
			priv := ed25519.NewKeyFromSeed(seed)
			signer, err := helpers.NewEd25519Signer(v.AgentID, priv)
			if err != nil {
				t.Fatalf("signer: %v", err)
			}
			created, expires, err := PoPWindowOf(v.SignatureInput)
			if err != nil {
				t.Fatal(err)
			}
			got, err := helpers.SignAgentBinding(context.Background(), signer, priv.Public().(ed25519.PublicKey),
				helpers.PoPOptions{
					URL: v.URL, Method: v.Method, Created: created, Expires: expires,
					SignatureAgent: v.AgentDirectory, Nonce: v.Nonce,
				})
			if err != nil {
				t.Fatalf("SignAgentBinding: %v", err)
			}
			for name, pair := range map[string][2]string{
				"X-FORA-Agent-Key": {got.AgentKey, v.PresentedKey},
				"Signature-Agent":  {got.SignatureAgent, v.SignatureAgent},
				"Signature-Input":  {got.SignatureInput, v.SignatureInput},
				"Signature":        {got.Signature, v.Signature},
			} {
				if pair[0] != pair[1] {
					t.Errorf("%s = %q, the copied corpus says %q", name, pair[0], pair[1])
				}
			}
		})
		signed++
	}
	if signed == 0 {
		t.Fatal("no vector was signed: the corpus carries no valid dictionary-form proof")
	}
}
