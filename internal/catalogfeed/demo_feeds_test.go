package catalogfeed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

// The demo catalog feeds and the totals the manifest beside them states.
//
// The Docker e2e suite owns separate feeds. The Exchange music integration
// test reads one playable music track; this check covers every demo record
// so malformed entries or unsupported tokens fail before a deployment push.
//
// The manifest in that directory states that all 49 records and 69 terms map and
// validate with zero warnings and zero hard rejects. That is a claim about this
// data, so it is checked here rather than left as prose.
//
// This is a parser-and-mapper test on committed input: no Exchange, no database,
// no RPC. It asserts what the ingest binary would compute before it signs
// anything, which is exactly the stage that decides whether a deployment push
// succeeds.
const (
	demoFeedRecords = 49
	demoFeedTerms   = 69
)

func demoFeedNames() []string {
	return []string{"philosophy.jsonl", "music.jsonl", "sfx.jsonl"}
}

func demoFeedPath(name string) string {
	return filepath.Join("..", "..", "deploy", "fixtures", "demo", name)
}

// demoContentDir is where the publisher origin image takes each record's file
// from: nginx routes the four path prefixes to one publisher tree each
// (deploy/publisher/nginx.conf), and the record's path is appended verbatim.
// A feed path with no committed file under that tree is an entry the Exchange
// would accept and the origin would answer 404 for, which nothing before a
// deployment would otherwise notice.
func demoContentDir(recordPath string) (string, bool) {
	prefixes := map[string]string{
		"/articles/": "stoa-press",
		"/lyrics/":   "harmonia-records",
		"/tracks/":   "harmonia-records",
		"/sfx/":      "foleyworks",
	}
	for prefix, dir := range prefixes {
		if strings.HasPrefix(recordPath, prefix) {
			return filepath.Join("..", "..", "deploy", "content", "demo", dir), true
		}
	}
	return "", false
}

// requireContentFile fails when a record names a path the publisher origin
// would not serve.
func requireContentFile(t *testing.T, feed string, record Record) {
	t.Helper()
	dir, ok := demoContentDir(record.Path)
	if !ok {
		t.Errorf("%s: %s: path is under no prefix the origin serves", feed, record.Path)
		return
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(record.Path))); err != nil {
		t.Errorf("%s: %s: no committed content file: %v", feed, record.Path, err)
	}
}

// parseDemoFeed parses one demo feed, failing the test with the feed's name so a
// breakage points at the file that changed rather than at a total.
func parseDemoFeed(t *testing.T, name string) []Record {
	t.Helper()
	f, err := os.Open(demoFeedPath(name))
	if err != nil {
		t.Fatalf("open demo feed %s: %v", name, err)
	}
	defer func() { _ = f.Close() }()
	records, err := ParseJSONL(f)
	if err != nil {
		t.Fatalf("parse demo feed %s: %v", name, err)
	}
	if len(records) == 0 {
		t.Fatalf("demo feed %s parsed to zero records", name)
	}
	return records
}

// TestDemoFeedsMapAndValidateCleanly drives every demo feed through ParseJSONL,
// then MapRecords (mapRecord per record, which canonicalizes each term through
// the SDK helper), then helpers.ValidateLicenseTerm per term — the ingest-tier
// check the Exchange runs on a pushed entry. A hard reject fails; so does a
// warning, because the manifest claims there are none and a warning is how an
// unregistered vocab token announces itself.
func TestDemoFeedsMapAndValidateCleanly(t *testing.T) {
	t.Parallel()

	var totalRecords, totalTerms int
	for _, name := range demoFeedNames() {
		records := parseDemoFeed(t, name)
		totalRecords += len(records)
		for _, record := range records {
			requireContentFile(t, name, record)
		}

		entries, err := MapRecords(records)
		if err != nil {
			t.Fatalf("map demo feed %s: %v", name, err)
		}
		for _, entry := range entries {
			for _, term := range entry.GetTerms() {
				totalTerms++
				helpers.NormalizeLicenseTerm(term) // idempotent: mapRecord already ran it
				warnings, err := helpers.ValidateLicenseTerm(term)
				if err != nil {
					t.Errorf("%s: %s: term rejected: %v", name, entry.GetPath(), err)
				}
				for _, w := range warnings {
					t.Errorf("%s: %s: term warning: %s", name, entry.GetPath(), w.Message)
				}
			}
		}
	}

	if totalRecords != demoFeedRecords {
		t.Errorf("demo feeds hold %d records, manifest states %d", totalRecords, demoFeedRecords)
	}
	if totalTerms != demoFeedTerms {
		t.Errorf("demo feeds hold %d terms, manifest states %d", totalTerms, demoFeedTerms)
	}
}
