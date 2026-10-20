package tagsearch_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

// The fora-music-v1 schema and the Broker must agree on what a label is. If
// the schema accepts a label the Broker refuses, a schema-valid feed stops the
// Broker's index from loading; if it refuses one the Broker accepts, the
// schema rejects feeds that work.
func TestLabelRuleMatchesTheMusicSchema(t *testing.T) {
	sch := testutil.CompileSchema(t, filepath.Clean("../../../../schemas/music/v1/music-v1.schema.json"))
	labels := []string{
		"jazz", "french horn", "électronique", "ß", "ⓐ", "Ⓐ", "ja zz", "ja\nzz",
		"", "Jazz", "Électronique", "ǅ", "ΣΑ", " jazz", "jazz ", "　jazz", "jazz\u0085",
		"\tjazz", "jazz\r", " jazz", "jazz ",
	}
	for _, label := range labels {
		raw, err := json.Marshal(map[string]any{"music.genre": label})
		if err != nil {
			t.Fatal(err)
		}
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		schemaOK := sch.Validate(inst) == nil
		line := `{"domain":"music.example","path":"/a","terms":[],"ext":` + string(raw) + `}`
		_, loadErr := tagsearch.Load(strings.NewReader(line), "https")
		_, filterErr := tagsearch.ParseFilters(map[string]any{"music.genre": label})
		if schemaOK != (loadErr == nil) || schemaOK != (filterErr == nil) {
			t.Errorf("label %q: schema accepts=%t, index load error=%v, filter error=%v",
				label, schemaOK, loadErr, filterErr)
		}
	}
}

// The schema and the Broker must also agree on the ISRC format, which both the
// feed and the music.isrc filter use.
func TestISRCRuleMatchesTheMusicSchema(t *testing.T) {
	sch := testutil.CompileSchema(t, filepath.Clean("../../../../schemas/music/v1/music-v1.schema.json"))
	for _, isrc := range []string{
		"USAAA2600001", "GB1AB2600002", "usaaa2600001", "US-AAA-26-00001", "USAAA260001", "USAAA26000011",
		"", " USAAA2600001", "USAAA260000A", "U1AAA2600001",
	} {
		raw, err := json.Marshal(map[string]any{"music.genre": "jazz", "music.isrc": isrc})
		if err != nil {
			t.Fatal(err)
		}
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		schemaOK := sch.Validate(inst) == nil
		line := `{"domain":"music.example","path":"/a","terms":[],"ext":` + string(raw) + `}`
		_, loadErr := tagsearch.Load(strings.NewReader(line), "https")
		_, filterErr := tagsearch.ParseFilters(map[string]any{"music.isrc": isrc})
		if schemaOK != (loadErr == nil) || schemaOK != (filterErr == nil) {
			t.Errorf("isrc %q: schema accepts=%t, index load error=%v, filter error=%v", isrc, schemaOK, loadErr, filterErr)
		}
	}
}

// The schema's "integer" accepts a whole number written with a fraction part,
// such as 90.0, and so does the SDK's music.Music, which the Broker decodes
// with. Each value is written raw: encoding it from a Go number would print
// 90.0 as 90.
func TestBPMRuleMatchesTheMusicSchema(t *testing.T) {
	sch := testutil.CompileSchema(t, filepath.Clean("../../../../schemas/music/v1/music-v1.schema.json"))
	for _, bpm := range []string{"90", "90.0", "1e2", "90.5", "0", "-5", `"90"`, "null"} {
		raw := `{"music.genre":"jazz","music.bpm":` + bpm + `}`
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		schemaOK := sch.Validate(inst) == nil
		line := `{"domain":"music.example","path":"/a","terms":[],"ext":` + raw + `}`
		_, loadErr := tagsearch.Load(strings.NewReader(line), "https")
		if bpm == "null" {
			// The schema refuses null; the Broker reads it as absent, as the spec allows.
			if schemaOK || loadErr != nil {
				t.Errorf("bpm null: schema accepts=%t, index load error=%v", schemaOK, loadErr)
			}
			continue
		}
		if schemaOK != (loadErr == nil) {
			t.Errorf("bpm %s: schema accepts=%t, index load error=%v", bpm, schemaOK, loadErr)
		}
	}
}
