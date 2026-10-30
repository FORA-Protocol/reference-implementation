package forawellknown_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
)

// The Exchange's profile list is written outside Go in files that cannot
// import it: the reference discovery document under deploy/publisher-wellknown
// (the publisher-manifest Terraform module is pinned to fora.json.example by
// its own test), the staging seed, and the documents the e2e stack's demo
// publishers serve from their origin. Each copy must equal ExchangeProfiles, or
// a publisher's manifest and the Broker's exchange row describe a different
// Exchange from the one that runs.
func TestProfileCopiesMatchExchangeProfiles(t *testing.T) {
	want := forawellknown.ExchangeProfiles()

	example := documentProfiles(t, "../../deploy/publisher-wellknown/fora.json.example")
	requireProfiles(t, "fora.json.example", [][]string{example}, want)

	documents, err := filepath.Glob("../../tests/e2e/mock-publisher/documents/*/.well-known/fora.json")
	if err != nil {
		t.Fatalf("list the e2e origin documents: %v", err)
	}
	served := make([][]string, 0, len(documents))
	for _, path := range documents {
		served = append(served, documentProfiles(t, path))
	}
	requireProfiles(t, "the e2e demo publishers' origin documents", served, want)

	seed := readFile(t, "../../deploy/terraform/scripts/seed-staging.sh")
	requireLiterals(t, "seed-staging.sh", seed, regexp.MustCompile(`'(\[[^']*\])'::jsonb`), want)
}

// documentProfiles reads the top-level supported_profiles of a fora.json file.
func documentProfiles(t *testing.T, path string) []string {
	t.Helper()
	var document struct {
		SupportedProfiles []string `json:"supported_profiles"`
	}
	readJSON(t, path, &document)
	return document.SupportedProfiles
}

// requireLiterals decodes every JSON array the pattern captures in text.
func requireLiterals(t *testing.T, name, text string, pattern *regexp.Regexp, want []string) {
	t.Helper()
	matches := pattern.FindAllStringSubmatch(text, -1)
	lists := make([][]string, 0, len(matches))
	for _, m := range matches {
		var list []string
		if err := json.Unmarshal([]byte(m[1]), &list); err != nil {
			t.Fatalf("%s: decode %s: %v", name, m[1], err)
		}
		lists = append(lists, list)
	}
	requireProfiles(t, name, lists, want)
}

func requireProfiles(t *testing.T, name string, lists [][]string, want []string) {
	t.Helper()
	if len(lists) == 0 {
		t.Fatalf("%s: no supported_profiles list found; the check would pass without checking anything", name)
	}
	for _, got := range lists {
		if !slices.Equal(got, want) {
			t.Errorf("%s: supported_profiles = %v, want %v", name, got, want)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(readFile(t, path)), v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}
