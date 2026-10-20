package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// queryEnv sets the three query discovery settings and isolates the AWS
// configuration chain from the developer's machine: no shared config or
// credentials file, and the region given (empty means none).
func queryEnv(t *testing.T, sw, file, model, region string) {
	t.Helper()
	t.Setenv(envQueryDiscovery, sw)
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
	for _, sw := range []string{"", "off"} {
		queryEnv(t, sw, "", "", "")
		s, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger())
		if err != nil || s != nil {
			t.Errorf("switch %q: searcher = %v, err = %v; want nil, nil", sw, s, err)
		}
	}
}

func TestBuildQuerySearcherEnabled(t *testing.T) {
	queryEnv(t, "on", writeFeed(t, validFeed), "amazon.nova-lite-v1:0", "us-east-1")
	s, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger())
	if err != nil || s == nil {
		t.Fatalf("searcher = %v, err = %v; want a searcher", s, err)
	}
}

// Every configuration problem refuses startup with an error naming it.
func TestBuildQuerySearcherRefusesIncompleteConfig(t *testing.T) {
	feed := writeFeed(t, validFeed)
	cases := []struct {
		name, sw, file, model, region string
		want                          []string
	}{
		{"unknown switch value", "yes", feed, "m", "us-east-1", []string{envQueryDiscovery, `"yes"`}},
		{"nothing configured", "on", "", "", "us-east-1", []string{envQueryCatalogFile, envQueryModel}},
		{"no model", "on", feed, "", "us-east-1", []string{envQueryModel}},
		{"missing file", "on", feed + ".absent", "m", "us-east-1", []string{envQueryCatalogFile, "absent"}},
		{"invalid feed", "on", writeFeed(t, `{"domain":`), "m", "us-east-1", []string{envQueryCatalogFile}},
		{
			"no music records", "on", writeFeed(t, `{"domain":"d","path":"/p","terms":[]}`), "m", "us-east-1",
			[]string{envQueryCatalogFile, "music.*"},
		},
		{"no region", "on", feed, "m", "", []string{envQueryDiscovery, "AWS_REGION"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			queryEnv(t, tc.sw, tc.file, tc.model, tc.region)
			s, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger())
			if err == nil || s != nil {
				t.Fatalf("searcher = %v, err = %v; want a startup error", s, err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
		})
	}
}

func TestBuildQuerySearcherRefusesUnknownURIScheme(t *testing.T) {
	queryEnv(t, "on", writeFeed(t, validFeed), "m", "us-east-1")
	t.Setenv(envQueryURIScheme, "ftp")
	s, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger())
	if err == nil || s != nil || !strings.Contains(err.Error(), envQueryURIScheme) {
		t.Fatalf("searcher = %v, err = %v; want an error naming %s", s, err, envQueryURIScheme)
	}
}
