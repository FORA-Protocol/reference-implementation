// Command fora-sign-directory signs a Web Bot Auth key directory response ahead of
// time, for a host that serves its directory as a static file.
//
// The Web Bot Auth profile requires every key directory to be served with a
// response signature by each key it lists (tag
// "http-message-signatures-directory", covering "@authority";req and
// content-digest). A server that holds the keys signs each response itself. A
// host that serves the directory as a static file runs no code per request and
// often holds no key, so whoever holds the key signs the directory once, ahead of
// time, for the one host name it is served under, and the host sends the three
// headers this prints with the file. Its callers: the operator key generation
// for the compose-stack module's static directories
// (deploy/terraform/scripts/gen-keys.sh), and the e2e stack's static
// key-directory hosts (deploy/publisher-jwks, an nginx image, whose entrypoint
// signs at start).
//
// It signs through the protocol SDK's helpers.SignDirectoryResponse, at the
// version go.mod pins, so its callers carry no signing code of their own and no
// second SDK pin.
//
// Usage:
//
//	fora-sign-directory -key <keyfile> -authority <host> -body <directory.json> [-days N]
//
// The keyfile is the JSON the key-generation scripts write: a base64url Ed25519
// seed (or seed||public key) under "private_key", "private" or "seed". The
// signature's window opens an hour in the past and lasts -days days. It prints
// three header lines, Content-Digest, Signature-Input and Signature, and refuses
// to sign a body that does not list the key.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
)

// skewBackdate opens the signature's window this far before now, the margin the
// repository's key-generation scripts give a key's validity window.
const skewBackdate = time.Hour

func main() {
	if err := run(os.Args[1:], os.Stdout, clock.System{}.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "fora-sign-directory:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer, now time.Time) error {
	fs := flag.NewFlagSet("fora-sign-directory", flag.ContinueOnError)
	keyFile := fs.String("key", "", "the signing key file")
	authority := fs.String("authority", "", "the host[:port] the directory is served under")
	bodyFile := fs.String("body", "", "the exact directory response body")
	days := fs.Int("days", 3650, "how many days the signature is valid")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyFile == "" || *authority == "" || *bodyFile == "" || *days <= 0 {
		return errors.New("-key, -authority, -body and a positive -days are required")
	}
	priv, err := readKey(*keyFile)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(*bodyFile)
	if err != nil {
		return fmt.Errorf("read the body: %w", err)
	}
	signed, err := sign(priv, strings.ToLower(*authority), body, now, time.Duration(*days)*24*time.Hour)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Content-Digest: %s\nSignature-Input: %s\nSignature: %s\n",
		signed.ContentDigest, signed.SignatureInput, signed.Signature)
	return err
}

// sign checks that body lists priv's public key and signs it for authority.
func sign(
	priv ed25519.PrivateKey, authority string, body []byte, now time.Time, lifetime time.Duration,
) (helpers.DirectoryResponseSignature, error) {
	pub, _ := priv.Public().(ed25519.PublicKey)
	keyID, err := helpers.Thumbprint(pub)
	if err != nil {
		return helpers.DirectoryResponseSignature{}, err
	}
	if !lists(body, base64.RawURLEncoding.EncodeToString(pub)) {
		return helpers.DirectoryResponseSignature{}, fmt.Errorf("the body does not list the key %s", keyID)
	}
	signer, err := helpers.NewEd25519Signer(keyID, priv)
	if err != nil {
		return helpers.DirectoryResponseSignature{}, err
	}
	created := now.Add(-skewBackdate)
	return helpers.SignDirectoryResponse(context.Background(), authority, body,
		[]helpers.Signer{signer}, created.Unix(), created.Add(lifetime).Unix())
}

// lists reports whether the directory body publishes the key whose JWK x is x.
func lists(body []byte, x string) bool {
	var doc struct {
		Keys []struct {
			X string `json:"x"`
		} `json:"keys"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return false
	}
	for _, key := range doc.Keys {
		if key.X == x {
			return true
		}
	}
	return false
}

// readKey reads an Ed25519 key from the key-generation scripts' JSON format.
func readKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // an operator-chosen path
	if err != nil {
		return nil, fmt.Errorf("read the key: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("the key file is not JSON: %w", err)
	}
	for _, field := range []string{"private_key", "private", "seed"} {
		encoded, ok := doc[field].(string)
		if !ok || encoded == "" {
			continue
		}
		seed, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encoded, "="))
		if err != nil {
			return nil, fmt.Errorf("the key's %s is not base64url: %w", field, err)
		}
		switch len(seed) {
		case ed25519.SeedSize, ed25519.PrivateKeySize:
			return ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize]), nil
		default:
			return nil, fmt.Errorf("the key's %s is %d bytes, not a seed", field, len(seed))
		}
	}
	return nil, errors.New("the key file carries no private_key, private or seed")
}
