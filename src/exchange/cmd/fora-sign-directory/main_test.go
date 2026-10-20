package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

func writeFixtures(t *testing.T, listed bool) (keyFile, bodyFile string, pub ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyFile = filepath.Join(dir, "key.json")
	seed := base64.RawURLEncoding.EncodeToString(priv.Seed())
	if err := os.WriteFile(keyFile, []byte(`{"private_key":"`+seed+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	x := base64.RawURLEncoding.EncodeToString(pub)
	if !listed {
		other, _, _ := ed25519.GenerateKey(rand.Reader)
		x = base64.RawURLEncoding.EncodeToString(other)
	}
	bodyFile = filepath.Join(dir, "directory.json")
	body := fmt.Sprintf(`{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","use":"sig","x":%q}]}`, x)
	if err := os.WriteFile(bodyFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return keyFile, bodyFile, pub
}

// The printed headers are a response signature the SDK's own verifier accepts for
// the authority signed, and for no other.
func TestTheSignatureVerifiesForTheAuthorityItWasMadeFor(t *testing.T) {
	keyFile, bodyFile, pub := writeFixtures(t, true)
	var out bytes.Buffer
	now := time.Now()
	if err := run([]string{"-key", keyFile, "-authority", "Agent-E2E", "-body", bodyFile}, &out, now); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		name, value, _ := strings.Cut(line, ": ")
		h.Set(name, value)
	}
	body, _ := os.ReadFile(bodyFile)
	thumb, _ := helpers.Thumbprint(pub)
	verified, err := helpers.VerifyDirectoryResponse("agent-e2e", h, body, []ed25519.PublicKey{pub}, now)
	if err != nil || !verified[thumb] {
		t.Fatalf("the signature did not verify for agent-e2e: %v %v", verified, err)
	}
	verified, err = helpers.VerifyDirectoryResponse("other-host", h, body, []ed25519.PublicKey{pub}, now)
	if err != nil || verified[thumb] {
		t.Fatalf("the signature verified for a host it was not made for: %v %v", verified, err)
	}
}

func TestADirectoryThatDoesNotListTheKeyIsNotSigned(t *testing.T) {
	keyFile, bodyFile, _ := writeFixtures(t, false)
	var out bytes.Buffer
	err := run([]string{"-key", keyFile, "-authority", "agent-e2e", "-body", bodyFile}, &out, time.Now())
	if err == nil || out.Len() != 0 {
		t.Fatalf("signed a directory that does not list the key: %q %v", out.String(), err)
	}
}
