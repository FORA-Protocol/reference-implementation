package catalogfeed_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/catalogfeed"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// The ext of every record in the feeds this repo ships that carries
// fora-music-v1 keys (music.<field>) must satisfy the fora-music-v1 schema.
// Records without such keys are valid catalog records that music indexing
// skips, so they are not checked here.
const musicSchemaPath = "../../schemas/music/v1/music-v1.schema.json"

var musicFeeds = []string{
	"../../deploy/fixtures/demo/music.jsonl",
	"../../tests/e2e/harness/fixtures/catalog/music.jsonl",
	"../../deploy/terraform/modules/query-index-feed/tests/fixture.jsonl",
}

func TestMusicFeedsMatchTheMusicSchema(t *testing.T) {
	t.Parallel()
	sch := testutil.CompileSchema(t, filepath.Clean(musicSchemaPath))
	for _, path := range musicFeeds {
		t.Run(filepath.Base(filepath.Dir(path))+"/"+filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			exts := musicExts(t, path)
			if len(exts) == 0 {
				t.Fatalf("%s has no music.* key; the check would pass without checking anything", path)
			}
			for uri, ext := range exts {
				if err := sch.Validate(musicInstance(t, ext)); err != nil {
					t.Errorf("%s: ext does not match the schema: %v", uri, err)
				}
			}
		})
	}
}

// The schema must refuse the shapes the profile forbids, or the feed check
// above proves nothing.
func TestMusicSchemaRefusesInvalidObjects(t *testing.T) {
	t.Parallel()
	sch := testutil.CompileSchema(t, filepath.Clean(musicSchemaPath))
	invalid := map[string]string{
		"upper-case label":      `{"genre":"Jazz"}`,
		"non-ASCII upper case":  `{"genre":"Électronique"}`,
		"non-breaking space":    `{"mood":["calm\u00a0"]}`,
		"untrimmed label":       `{"mood":[" calm"]}`,
		"empty label":           `{"instruments":[""]}`,
		"label not in an array": `{"suitable_for":"vlog"}`,
		"fractional bpm":        `{"bpm":120.5}`,
		"zero bpm":              `{"bpm":0}`,
		"zero duration":         `{"duration_seconds":0}`,
		"unknown energy":        `{"energy":"extreme"}`,
		"vocals as a string":    `{"vocals":"no"}`,
		"null field":            `{"vocals":null}`,
		"hyphenated isrc":       `{"isrc":"US-UAN-23-00011"}`,
		"empty artist":          `{"artist":""}`,
	}
	for name, obj := range invalid {
		if err := sch.Validate(musicInstance(t, flatMusic(t, obj))); err == nil {
			t.Errorf("%s: %s was accepted", name, obj)
		}
	}
	valid := map[string]string{
		"empty object":         `{}`,
		"empty arrays":         `{"mood":[],"instruments":[]}`,
		"unknown field":        `{"iswc":"T-034524680-1"}`,
		"multi-word label":     `{"instruments":["french horn"]}`,
		"compact isrc":         `{"isrc":"USUAN2300011"}`,
		"fractional duration":  `{"duration_seconds":60.03}`,
		"category label":       `{"instruments":["brass"]}`,
		"vocals false present": `{"vocals":false}`,
	}
	for name, ext := range map[string]string{
		"other profile keys":                         `{"news.section":"culture","music.genre":"jazz"}`,
		"nested music object is only an unknown key": `{"music":{"genre":"Jazz"}}`,
	} {
		if err := sch.Validate(musicInstance(t, json.RawMessage(ext))); err != nil {
			t.Errorf("%s: %s was refused: %v", name, ext, err)
		}
	}
	for name, obj := range valid {
		if err := sch.Validate(musicInstance(t, flatMusic(t, obj))); err != nil {
			t.Errorf("%s: %s was refused: %v", name, obj, err)
		}
	}
}

// musicExts returns the raw ext of every record in the feed that carries at
// least one music.<field> key, keyed by domain and path.
func musicExts(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	records, err := catalogfeed.ParseJSONL(f)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]json.RawMessage{}
	for _, rec := range records {
		var ext map[string]json.RawMessage
		if len(rec.Ext) > 0 {
			if err := json.Unmarshal(rec.Ext, &ext); err != nil {
				t.Fatalf("%s%s: ext: %v", rec.Domain, rec.Path, err)
			}
		}
		for k := range ext {
			if strings.HasPrefix(k, "music.") {
				out[rec.Domain+rec.Path] = rec.Ext
				break
			}
		}
	}
	return out
}

// flatMusic turns a JSON object of profile field names into the ext object
// that carries them, as music.<field> keys.
func flatMusic(t *testing.T, fields string) json.RawMessage {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(fields), &m); err != nil {
		t.Fatalf("decode %s: %v", fields, err)
	}
	ext := map[string]any{}
	for k, v := range m {
		ext["music."+k] = v
	}
	raw, err := json.Marshal(ext)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func musicInstance(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return inst
}
