//go:build integration

package exchangetest

import (
	"context"
	"sync"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
)

// Manifests stands in for publishers' own web servers: it answers what each
// site serves at its well-known path. It satisfies service.ManifestChecker, the
// port the registration service reads documents through, so everything above
// that port is the production code.
//
// A host with nothing served answers forawellknown.ErrNoDocument, which is what
// the production fetcher reports for a 404.
type Manifests struct {
	mu        sync.Mutex
	manifests map[string]*forawellknown.Manifest
	errs      map[string]error
	calls     int
}

// NewManifests returns a source where no site serves anything.
func NewManifests() *Manifests {
	return &Manifests{
		manifests: map[string]*forawellknown.Manifest{},
		errs:      map[string]error{},
	}
}

// Get answers what host serves now.
func (s *Manifests) Get(_ context.Context, host string) (*forawellknown.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if err, ok := s.errs[host]; ok {
		return nil, err
	}
	if m, ok := s.manifests[host]; ok {
		return m, nil
	}
	return nil, forawellknown.ErrNoDocument
}

// Refresh is what the registration service calls, so it answers the same way
// the production cache does: by going back to the source rather than to a
// remembered copy.
func (s *Manifests) Refresh(ctx context.Context, host string) (*forawellknown.Manifest, error) {
	return s.Get(ctx, host)
}

// Serve makes host serve m.
func (s *Manifests) Serve(host string, m *forawellknown.Manifest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.errs, host)
	s.manifests[host] = m
}

// Withdraw takes the document away again, which is how a test reaches the
// states that follow registration: the site proved itself once, and what it
// serves afterwards is its own business.
func (s *Manifests) Withdraw(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.manifests, host)
	delete(s.errs, host)
}

// Fail makes a fetch of host fail with err, the way the production fetcher
// reports a document it could not read.
func (s *Manifests) Fail(host string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.manifests, host)
	s.errs[host] = err
}

// Calls is how many times the Exchange has read any site's document. A suite
// uses it to prove that an action asked a site nothing.
func (s *Manifests) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
