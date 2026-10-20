// Package ingesttest holds the contributor-key fixture every suite that
// signs a catalog push needs: a fresh keypair written in the shape
// ingest.LoadContributorKey reads, so each suite drives the production load
// path. No private-key material is tracked in git; every run mints its own.
package ingesttest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest"
)

// WriteContributorKeyFile mints a contributor keypair under kid and writes it
// to a temp file, returning the path.
func WriteContributorKeyFile(tb testing.TB, kid string) string {
	tb.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatalf("generate contributor key: %v", err)
	}
	raw, err := json.Marshal(ingest.ContributorKey{
		KID:        kid,
		PrivateKey: base64.RawURLEncoding.EncodeToString(priv.Seed()),
		PublicKey:  base64.RawURLEncoding.EncodeToString(pub),
	})
	if err != nil {
		tb.Fatalf("marshal contributor key: %v", err)
	}
	path := filepath.Join(tb.TempDir(), "contributor-key.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		tb.Fatalf("write contributor key: %v", err)
	}
	return path
}
