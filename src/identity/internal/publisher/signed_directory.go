package publisher

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/keystore"
)

// An agent's key directory is answered signed: once per listed key, with the
// agent's custodied keys, over the authority the request named. The signers are
// resolved from custody once per built docSet, and the signatures are cached per
// authority on it, so a directory fetched often is signed once per window.

// SignedDirectory is Directory together with the directory's response
// signature for authority, the host[:port] the request fetched it under: one
// signature per listed key, made with the agent's custodied keys, covering that
// authority and the body's digest (helpers.SignDirectoryResponse). A custody
// outage while resolving the signers is ErrUnavailable.
func (s *Service) SignedDirectory(
	ctx context.Context, subdomain, authority string,
) ([]byte, helpers.DirectoryResponseSignature, error) {
	docs, err := s.get(ctx, subdomain)
	if err != nil {
		return nil, helpers.DirectoryResponseSignature{}, err
	}
	switch {
	case docs.wba == nil && docs.wbaUnavail:
		return nil, helpers.DirectoryResponseSignature{}, ErrUnavailable
	case docs.wba == nil:
		return nil, helpers.DirectoryResponseSignature{}, ErrAbsent
	}
	signers, err := s.directorySigners(ctx, docs)
	if err != nil {
		return nil, helpers.DirectoryResponseSignature{}, err
	}
	sig, err := docs.signatures.Sign(ctx, authority, docs.wba, signers)
	if err != nil {
		return nil, helpers.DirectoryResponseSignature{}, fmt.Errorf("publisher: sign directory: %w", err)
	}
	return docs.wba, sig, nil
}

// directorySigners resolves, once per docSet, a signer for every key the
// directory lists, keyid the key's RFC 7638 thumbprint. Custody hands back a
// crypto.Signer; the SDK signer needs the Ed25519 bytes, so a backend that will
// not surrender them is a hard fault here as it is for request signing
// (agentsign.ErrNotSignable).
func (s *Service) directorySigners(ctx context.Context, docs *docSet) ([]helpers.Signer, error) {
	docs.signerMu.Lock()
	defer docs.signerMu.Unlock()
	if docs.signers != nil {
		return docs.signers, nil
	}
	out := make([]helpers.Signer, 0, len(docs.wbaKeys))
	for _, k := range docs.wbaKeys {
		custodied, err := s.signers.Signer(ctx, k.Ref)
		switch {
		case errors.Is(err, keystore.ErrUnavailable), errors.Is(err, keystore.ErrPermissionDenied):
			return nil, fmt.Errorf("%w: directory signer: %w", ErrUnavailable, err)
		case err != nil:
			return nil, fmt.Errorf("publisher: directory signer for %s: %w", k.Ref.Thumbprint, err)
		}
		priv, ok := custodied.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("publisher: custody holds a %T for %s, not an Ed25519 key", custodied, k.Ref.Thumbprint)
		}
		keyID, err := helpers.Thumbprint(k.Public)
		if err != nil {
			return nil, fmt.Errorf("publisher: listed key thumbprint: %w", err)
		}
		signer, err := helpers.NewEd25519Signer(keyID, priv)
		if err != nil {
			return nil, fmt.Errorf("publisher: directory signer for %s: %w", keyID, err)
		}
		out = append(out, signer)
	}
	docs.signers = out
	return out, nil
}
