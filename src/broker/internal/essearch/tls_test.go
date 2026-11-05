package essearch_test

import (
	"context"
	"crypto/x509"
	"errors"
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch/essearchtest"
)

// A configured CA is the only trust root for the connection: it does not add to
// the system roots, and without it the system roots alone decide. The stand-in
// serves a certificate no system trusts, so each case shows which roots were
// used.
func TestTLS_OnlyTheConfiguredCAIsTrusted(t *testing.T) {
	t.Parallel()
	es, caPEM := essearchtest.NewTLSServer(t)
	es.Script(essearchtest.Reply{URLs: []string{urlA}})

	trusted := es.Config()
	trusted.AllowHTTP = false
	trusted.CACertPEM = caPEM
	got, err := newSearcher(t, trusted).Search(context.Background(), "q", none)
	if err != nil || !slices.Equal(got, []string{urlA}) {
		t.Fatalf("with the server's CA: %v, %v", got, err)
	}

	for name, ca := range map[string][]byte{"the system roots": nil, "another CA": selfSignedPEM(t)} {
		cfg := trusted
		cfg.CACertPEM = ca
		_, err := newSearcher(t, cfg).Search(context.Background(), "q", none)
		var unknown x509.UnknownAuthorityError
		if !errors.Is(err, essearch.ErrUnavailable) || !errors.As(err, &unknown) {
			t.Errorf("with %s: err = %v, want an unknown-authority failure", name, err)
		}
	}
}
