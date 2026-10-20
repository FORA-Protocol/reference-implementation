//go:build integration

package exchangetest

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/agentreg"
)

// Registry stands in for callers' own key directories: it answers the key a
// caller id resolves to. In production each id is a host serving a Web Bot
// Auth directory on the public internet, which a suite does not own, so the
// fake sits at the registry port (agentreg.Registry) and everything above it
// is the production code. Self-signup and key refresh fetch a directory, so
// the fake refuses both.
type Registry struct {
	mu   sync.Mutex
	keys map[string]ed25519.PublicKey
}

// NewRegistry returns a registry that knows no caller.
func NewRegistry() *Registry {
	return &Registry{keys: map[string]ed25519.PublicKey{}}
}

// Put makes id resolve to pub.
func (r *Registry) Put(id string, pub ed25519.PublicKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[id] = pub
}

// LookupPublicKey answers the key id resolves to, or agentreg.ErrUnknown.
func (r *Registry) LookupPublicKey(_ context.Context, id string) (ed25519.PublicKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k, ok := r.keys[id]; ok {
		return k, nil
	}
	return nil, agentreg.ErrUnknown
}

// RegisterFromDirectory is refused: it fetches a directory.
func (r *Registry) RegisterFromDirectory(_ context.Context, _, _ string) error {
	return errors.New("exchangetest.Registry: self-signup fetches a directory, which the fake does not")
}

// RefreshDirectoryKey is refused: it fetches a directory.
func (r *Registry) RefreshDirectoryKey(_ context.Context, _ string) error {
	return errors.New("exchangetest.Registry: key refresh fetches a directory, which the fake does not")
}
