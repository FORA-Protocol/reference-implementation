package main

import (
	"crypto/ed25519"
	"fmt"
	"net/http"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
)

// documents holds the discovery documents the impostor serves: one Exchange
// manifest per identity, and the one key directory every identity shares,
// with the signer its response is signed by.
type documents struct {
	manifests  map[string][]byte
	directory  []byte
	signer     helpers.Signer
	signatures *server.SignatureCache
}

// newDocuments builds every document once, at start. Each passes the same
// protocol and schema checks a Broker applies when it reads one, so a document
// the Broker would refuse stops the impostor at boot instead of reading as a
// Broker fault later. The key directory is honest: it follows the Web Bot Auth
// directory rules exactly as the reference Exchange's does, because the
// impostor's faults are all in how it answers a purchase.
func newDocuments(identities map[string]bool, priv ed25519.PrivateKey, clk clock.Clock) (*documents, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("offer key: not an Ed25519 public key")
	}
	keys := helpers.DirectoryDocument([]ed25519.PublicKey{pub}, 0, clk.Now()).GetKeys()
	directory, err := server.BuildWBA(server.WBAConfig{Keys: server.StaticKeys(keys...)})
	if err != nil {
		return nil, fmt.Errorf("key directory: %w", err)
	}
	keyID, err := helpers.Thumbprint(pub)
	if err != nil {
		return nil, fmt.Errorf("offer key thumbprint: %w", err)
	}
	signer, err := helpers.NewEd25519Signer(keyID, priv)
	if err != nil {
		return nil, fmt.Errorf("directory signer: %w", err)
	}
	manifests := make(map[string][]byte, len(identities))
	for identity := range identities {
		manifest, err := server.Build(server.Config{
			Role:              forawellknown.RoleExchange,
			Domain:            identity,
			Name:              "Misbehaving Exchange",
			Endpoint:          "http://" + identity,
			BaseCurrency:      offerCurrency,
			SupportedProfiles: []string{"fora-news-v1"},
		})
		if err != nil {
			return nil, fmt.Errorf("manifest of %s: %w", identity, err)
		}
		manifests[identity] = manifest
	}
	return &documents{
		manifests: manifests, directory: directory, signer: signer,
		signatures: server.NewSignatureCache(clk.Now, 0),
	}, nil
}

// serveManifest answers the manifest of the identity the request is addressed
// to, by its Host header. A host the impostor does not answer as gets 404.
func (d *documents) serveManifest(w http.ResponseWriter, r *http.Request) {
	manifest, ok := d.manifests[r.Host]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(manifest)
}

// serveDirectory answers the key directory, for every identity alike, as every
// key directory answers: 200 with the directory media type and a response
// signature by the listed key covering the authority the request named.
func (d *documents) serveDirectory(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.manifests[r.Host]; !ok {
		http.NotFound(w, r)
		return
	}
	_ = server.WriteDirectory(w, r, d.directory, []helpers.Signer{d.signer}, d.signatures)
}
