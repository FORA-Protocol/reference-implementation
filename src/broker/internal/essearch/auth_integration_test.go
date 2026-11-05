//go:build integration

package essearch_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
)

// Authentication against a real node with security on: the credentials a
// deployment supplies, and how each refusal is classified at boot.

func TestAuth_BasicAndAPIKeyBothSearch(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es, doc(urlA, "headline", "text"))
	ctx := context.Background()
	key, err := es.MintReadKey(ctx, "broker", catalogIndex)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*essearch.Config){
		"basic": func(*essearch.Config) {},
		"api key": func(c *essearch.Config) {
			c.Auth, c.Username, c.Password, c.APIKey = essearch.AuthAPIKey, "", "", key
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := nodeConfig(t, es)
			mutate(&cfg)
			s := newSearcher(t, cfg)
			if err := s.Probe(ctx); err != nil {
				t.Fatalf("Probe: %v", err)
			}
			got, err := s.Search(ctx, "headline", none)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			assertURIs(t, got, []string{urlA})
		})
	}
}

// A key with nothing but read on the index can run the whole probe: rendering
// and searching are the only calls the Broker makes.
func TestAuth_ReadKeyCanRender(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es, doc(urlA, "headline", "text"))
	ctx := context.Background()
	key, err := es.MintReadKey(ctx, "reader", catalogIndex)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*essearch.Config){
		"a stored template": func(*essearch.Config) {},
		"a template file":   func(c *essearch.Config) { c.TemplateID, c.TemplateSource = "", exampleTemplate(t) },
	} {
		cfg := nodeConfig(t, es)
		cfg.Auth, cfg.Username, cfg.Password, cfg.APIKey = essearch.AuthAPIKey, "", "", key
		mutate(&cfg)
		if err := newSearcher(t, cfg).Probe(ctx); err != nil {
			t.Errorf("%s: Probe with a read-only key: %v", name, err)
		}
	}
}

// A secured node refuses a Broker that sends nothing, which proves the mode
// sends nothing.
func TestAuth_NoneIsRefusedByASecuredNode(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es, doc(urlA, "headline", "text"))
	cfg := nodeConfig(t, es)
	cfg.Auth, cfg.Username, cfg.Password = essearch.AuthNone, "", ""
	s := newSearcher(t, cfg)
	if err := s.Probe(context.Background()); !errors.Is(err, essearch.ErrCredentials) {
		t.Fatalf("Probe err = %v, want ErrCredentials", err)
	}
	if _, err := s.Search(context.Background(), "headline", none); !errors.Is(err, essearch.ErrUnavailable) {
		t.Fatalf("Search err = %v, want ErrUnavailable", err)
	}
}

func TestAuth_RefusedCredentials(t *testing.T) {
	es := acquireES(t)
	seedCatalog(t, es, doc(urlA, "headline", "text"))
	ctx := context.Background()
	otherIndexKey, err := es.MintReadKey(ctx, "other-index", "some-other-index")
	if err != nil {
		t.Fatal(err)
	}
	wrongPass := "wrong-" + es.Password
	wrongKey := base64.StdEncoding.EncodeToString([]byte("not-an-id:not-a-key"))
	cases := map[string]func(*essearch.Config){
		"wrong password": func(c *essearch.Config) { c.Password = wrongPass },
		"wrong key": func(c *essearch.Config) {
			c.Auth, c.Username, c.Password, c.APIKey = essearch.AuthAPIKey, "", "", wrongKey
		},
		"a key for some other index": func(c *essearch.Config) {
			c.Auth, c.Username, c.Password, c.APIKey = essearch.AuthAPIKey, "", "", otherIndexKey
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := nodeConfig(t, es)
			mutate(&cfg)
			s := newSearcher(t, cfg)

			if err := s.Probe(ctx); !errors.Is(err, essearch.ErrCredentials) {
				t.Fatalf("Probe err = %v, want ErrCredentials", err)
			}
			_, err := s.Search(ctx, "headline", none)
			if !errors.Is(err, essearch.ErrUnavailable) {
				t.Fatalf("Search err = %v, want ErrUnavailable", err)
			}
			for _, secret := range []string{wrongPass, wrongKey, otherIndexKey} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error carries a credential: %v", err)
				}
			}
		})
	}
}
