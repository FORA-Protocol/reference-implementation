//go:build integration

package essearch_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/searchfilters"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
)

// sharedES is the one real node of this package's integration tests. Those tests
// run serially, because each resets it.
var sharedES *testutil.SharedElasticsearch

func TestMain(m *testing.M) {
	os.Exit(testutil.RunElasticsearchMain(m, func(es *testutil.SharedElasticsearch) { sharedES = es }))
}

const (
	catalogIndex = "catalog"
	templateID   = "fora-catalog"
	// catalogMapping is what the example template reads: the URL is a keyword
	// field, so it sorts, the searched fields are text, and the filters address a
	// keyword and a date. slug feeds the computed-URL template.
	catalogMapping = `{"url":{"type":"keyword"},"title":{"type":"text"},"body":{"type":"text"},
		"language":{"type":"keyword"},"date_published":{"type":"date"},"slug":{"type":"keyword"}}`
)

// repoFile reads a file the repository ships, so the tests run the documents the
// operator documentation hands out.
func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func exampleTemplate(t *testing.T) string {
	t.Helper()
	return string(repoFile(t, "deploy/broker/search-template.example.mustache"))
}

func exampleSchema(t *testing.T) *searchfilters.Schema {
	t.Helper()
	s, err := searchfilters.Load(repoFile(t, "deploy/broker/search-filters.example.json"))
	if err != nil || s == nil {
		t.Fatalf("load the example schema: %v, %v", s, err)
	}
	return s
}

// acquireES resets the node and returns it. The reset runs at the start of the
// test, so a test that panicked leaves nothing behind for the next one.
func acquireES(t *testing.T) *testutil.SharedElasticsearch {
	t.Helper()
	if err := sharedES.Reset(context.Background()); err != nil {
		t.Fatalf("reset elasticsearch: %v", err)
	}
	return sharedES
}

// seedCatalog stores the example template, creates the catalog index under the
// example's mapping and fills it.
func seedCatalog(t *testing.T, es *testutil.SharedElasticsearch, docs ...map[string]any) {
	t.Helper()
	ctx := context.Background()
	if err := es.PutTemplate(ctx, templateID, exampleTemplate(t)); err != nil {
		t.Fatal(err)
	}
	if err := es.CreateIndex(ctx, catalogIndex, catalogMapping); err != nil {
		t.Fatal(err)
	}
	if err := es.BulkIndex(ctx, catalogIndex, docs); err != nil {
		t.Fatal(err)
	}
}

// nodeConfig returns settings that reach the real node as its superuser over
// plain http, which is what the module gives a node of this version, with the
// stored example template and the example filter schema. The timeout is
// generous: this is the node's first query on a loaded CI runner, not a latency
// test.
func nodeConfig(t *testing.T, es *testutil.SharedElasticsearch) essearch.Config {
	t.Helper()
	return essearch.Config{
		Endpoint:   es.Address,
		Index:      catalogIndex,
		TemplateID: templateID,
		HitURLPath: "_source.url",
		Limit:      10,
		Timeout:    15 * time.Second,
		Auth:       essearch.AuthBasic,
		Username:   es.Username,
		Password:   es.Password,
		AllowHTTP:  true,
		Filters:    exampleSchema(t),
	}
}
