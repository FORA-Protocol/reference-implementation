package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
)

// queryEnv sets the provider and its two llm settings, and isolates the AWS
// configuration chain from the developer's machine: no shared config or
// credentials file, and the region given (empty means none).
func queryEnv(t *testing.T, provider, file, model, region string) {
	t.Helper()
	t.Setenv(envQueryProvider, provider)
	t.Setenv(envQueryCatalogFile, file)
	t.Setenv(envQueryModel, model)
	t.Setenv(envQueryURIScheme, "")
	missing := filepath.Join(t.TempDir(), "absent")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", region)
	t.Setenv("AWS_DEFAULT_REGION", "")
}

func writeFeed(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "feed.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validFeed = `{"domain":"music.example","path":"/a.mp3","terms":[],"ext":{"music.genre":"soundtrack"}}`

func TestBuildQuerySearcherDisabled(t *testing.T) {
	for _, provider := range []string{"", "none"} {
		queryEnv(t, provider, "", "", "")
		s, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger())
		if err != nil || s != nil {
			t.Errorf("provider %q: searcher = %v, err = %v; want nil, nil", provider, s, err)
		}
	}
}

func TestBuildQuerySearcherEnabled(t *testing.T) {
	queryEnv(t, "llm", writeFeed(t, validFeed), "amazon.nova-lite-v1:0", "us-east-1")
	s, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger())
	if err != nil || s == nil {
		t.Fatalf("searcher = %v, err = %v; want a searcher", s, err)
	}
}

// Every configuration problem refuses startup with an error naming it.
func TestBuildQuerySearcherRefusesIncompleteConfig(t *testing.T) {
	feed := writeFeed(t, validFeed)
	cases := []struct {
		name, provider, file, model, region string
		want                                []string
	}{
		{"unknown provider", "yes", feed, "m", "us-east-1", []string{envQueryProvider, `"yes"`, "elasticsearch"}},
		{"the retired switch value", "on", feed, "m", "us-east-1", []string{envQueryProvider, `"on"`}},
		{"nothing configured", "llm", "", "", "us-east-1", []string{envQueryCatalogFile, envQueryModel}},
		{"no model", "llm", feed, "", "us-east-1", []string{envQueryModel}},
		{"missing file", "llm", feed + ".absent", "m", "us-east-1", []string{envQueryCatalogFile, "absent"}},
		{"invalid feed", "llm", writeFeed(t, `{"domain":`), "m", "us-east-1", []string{envQueryCatalogFile}},
		{
			"no music records", "llm", writeFeed(t, `{"domain":"d","path":"/p","terms":[]}`), "m", "us-east-1",
			[]string{envQueryCatalogFile, "music.*"},
		},
		{"no region", "llm", feed, "m", "", []string{envQueryProvider, "AWS_REGION"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			queryEnv(t, tc.provider, tc.file, tc.model, tc.region)
			s, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger())
			assertBootRefusal(t, s, err, tc.want...)
		})
	}
}

func TestBuildQuerySearcherRefusesUnknownURIScheme(t *testing.T) {
	queryEnv(t, "llm", writeFeed(t, validFeed), "m", "us-east-1")
	t.Setenv(envQueryURIScheme, "ftp")
	s, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger())
	assertBootRefusal(t, s, err, envQueryURIScheme)
}

// assertBootRefusal fails unless the build refused to start, with an error that
// names each of wants.
func assertBootRefusal(t *testing.T, s catalogsearch.Searcher, err error, wants ...string) {
	t.Helper()
	if err == nil || s != nil {
		t.Fatalf("searcher = %v, err = %v; want a startup error", s, err)
	}
	for _, w := range wants {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not name %q", err, w)
		}
	}
}
