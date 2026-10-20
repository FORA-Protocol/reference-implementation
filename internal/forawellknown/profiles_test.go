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
// import it: the publisher-manifest Terraform module (pinned to
// template-env.json by its own test), the staging seed, and the e2e stack.
// Each copy must equal ExchangeProfiles, or a publisher's manifest and the
// Broker's exchange row describe a different Exchange from the one that runs.
func TestProfileCopiesMatchExchangeProfiles(t *testing.T) {
	want := forawellknown.ExchangeProfiles()

	var env struct {
		ExchangesJSON string `json:"EXCHANGES_JSON"`
	}
	readJSON(t, "../../deploy/publisher-wellknown/template-env.json", &env)
	requireProfiles(t, "template-env.json EXCHANGES_JSON", exchangeEntryProfiles(t, env.ExchangesJSON), want)

	seed := readFile(t, "../../deploy/terraform/scripts/seed-staging.sh")
	requireLiterals(t, "seed-staging.sh", seed, regexp.MustCompile(`'(\[[^']*\])'::jsonb`), want)

	compose := readFile(t, "../../docker-compose.e2e.yml")
	requireLiterals(t, "docker-compose.e2e.yml", compose, regexp.MustCompile(`"supported_profiles":(\[[^\]]*\])`), want)
}

func exchangeEntryProfiles(t *testing.T, exchangesJSON string) [][]string {
	t.Helper()
	var entries []struct {
		SupportedProfiles []string `json:"supported_profiles"`
	}
	if err := json.Unmarshal([]byte(exchangesJSON), &entries); err != nil {
		t.Fatalf("decode EXCHANGES_JSON: %v", err)
	}
	out := make([][]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.SupportedProfiles)
	}
	return out
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
