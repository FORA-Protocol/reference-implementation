package searchfilters_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/searchfilters"
)

// Load and Check are a parser and a decision table with many branches, so they
// are unit-tested here; the services that read the schema are tested through
// their own surfaces.

// examplePath is the example the operator documentation ships.
const examplePath = "../../deploy/broker/search-filters.example.json"

// newsSchema is a minimal valid schema; withTop and withKeys vary it one rule
// at a time.
func newsSchema(top, props string) string {
	return `{"type":"object","additionalProperties":false` + top +
		`,"properties":{"news.language":{"type":"string","pattern":"^[a-z]{2}$"}` + props + `}}`
}

func mustLoad(t *testing.T, doc string) *searchfilters.Schema {
	t.Helper()
	s, err := searchfilters.Load([]byte(doc))
	if err != nil || s == nil {
		t.Fatalf("Load = %v, %v; want a schema", s, err)
	}
	return s
}

func TestLoad_AcceptsTheShippedExample(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(examplePath))
	if err != nil {
		t.Fatal(err)
	}
	s := mustLoad(t, string(raw))
	if got, want := s.Keys(), []string{"news.date_published_after", "news.language"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Keys() = %v, want %v", got, want)
	}
	if string(s.Raw()) != string(raw) {
		t.Error("Raw() is not the document as loaded")
	}
}

func TestLoad_BlankIsNoSchema(t *testing.T) {
	for _, doc := range []string{"", " \n\t\r"} {
		if s, err := searchfilters.Load([]byte(doc)); s != nil || err != nil {
			t.Errorf("Load(%q) = %v, %v; want no schema and no error", doc, s, err)
		}
	}
}

// Each row breaks one rule, and the refusal names that rule.
func TestLoad_RefusesEachRule(t *testing.T) {
	lang := `"news.language":{"type":"string"}`
	cases := map[string]struct{ doc, want string }{
		"not JSON":                        {`{"type":`, "malformed"},
		"an array":                        {`[{"type":"object"}]`, "malformed"},
		"type string":                     {`{"type":"string","additionalProperties":false,"properties":{` + lang + `}}`, `type must be "object"`},
		"no properties":                   {`{"type":"object","additionalProperties":false}`, "properties must declare"},
		"empty properties":                {`{"type":"object","additionalProperties":false,"properties":{}}`, "properties must declare"},
		"additionalProperties absent":     {`{"type":"object","properties":{` + lang + `}}`, "additionalProperties must be false"},
		"additionalProperties true":       {`{"type":"object","additionalProperties":true,"properties":{` + lang + `}}`, "additionalProperties must be false"},
		"additionalProperties a schema":   {`{"type":"object","additionalProperties":{},"properties":{` + lang + `}}`, "additionalProperties must be false"},
		"patternProperties":               {newsSchema(`,"patternProperties":{"^x":{}}`, ""), "patternProperties at the top level"},
		"required at the top":             {newsSchema(`,"required":["news.language"]`, ""), "required at the top level"},
		"oneOf at the top":                {newsSchema(`,"oneOf":[{},{}]`, ""), "oneOf at the top level"},
		"dialect with a trailing #":       {newsSchema(`,"$schema":"https://json-schema.org/draft/2020-12/schema#"`, ""), "$schema must be absent or exactly"},
		"another dialect":                 {newsSchema(`,"$schema":"http://json-schema.org/draft-07/schema#"`, ""), "wrong_dialect"},
		"a remote $ref":                   {newsSchema("", `,"news.topic":{"$ref":"https://schemas.example/topic.json"}`), "remote_ref"},
		"a local $ref":                    {newsSchema("", `,"news.topic":{"$ref":"#/properties/news.language"}`), "uses $ref"},
		"$defs":                           {newsSchema(`,"$defs":{"lang":{"type":"string"}}`, ""), "uses $defs"},
		"an absolute $id":                 {newsSchema(`,"$id":"https://broker.example/filters.json"`, ""), "uses $id"},
		"a relative $id":                  {newsSchema(`,"$id":"filters.json"`, ""), "uses $id"},
		"$anchor in a key":                {newsSchema("", `,"news.topic":{"$anchor":"topic","type":"string"}`), "uses $anchor"},
		"$dynamicAnchor":                  {newsSchema(`,"$dynamicAnchor":"root"`, ""), "uses $dynamicAnchor"},
		"a default on a key":              {newsSchema("", `,"news.topic":{"type":"string","default":"world"}`), "uses default"},
		"a default under items":           {newsSchema("", `,"news.tags":{"type":"array","items":{"type":"string","default":"x"}}`), "uses default"},
		"a default breaking its own rule": {newsSchema("", `,"news.zone":{"type":"string","pattern":"^[a-z]{2}$","default":"none"}`), "uses default"},
		"$dynamicRef under items":         {newsSchema("", `,"news.tags":{"type":"array","items":{"$dynamicRef":"#/properties/news.language"}}`), "uses $dynamicRef"},
		"an unsafe pattern":               {newsSchema("", `,"news.topic":{"type":"string","pattern":"(?=x)"}`), "unsafe_pattern"},
		"one byte over the size cap":      {oversizedSchema(), "too_large"},
		"a key with an empty part":        {newsSchema("", `,"news..topic":{"type":"string"}`), `key "news..topic"`},
		"a key with a space":              {newsSchema("", `,"news.top ic":{"type":"string"}`), `key "news.top ic"`},
		"a key that is another's prefix":  {newsSchema("", `,"news":{"type":"string"}`), `"news" is also a key`},
		"a key with a slash":              {newsSchema("", `,"news/topic":{"type":"string"}`), `key "news/topic"`},
		"minProperties at the top":        {newsSchema(`,"minProperties":1`, ""), "minProperties at the top level"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, err := searchfilters.Load([]byte(c.doc))
			if err == nil || s != nil {
				t.Fatalf("Load = %v, %v; want a refusal", s, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("refusal %q does not name %q", err, c.want)
			}
		})
	}
}

// oversizedSchema is a valid filter schema one byte past the SDK's 16 KiB cap.
func oversizedSchema() string {
	base := newsSchema(`,"description":""`, "")
	pad := 16384 + 1 - len(base)
	return newsSchema(`,"description":"`+strings.Repeat("x", pad)+`"`, "")
}

// A reference keyword or a default inside a data keyword is a value a filter
// may equal, not a keyword, so it is not refused.
func TestLoad_ReferenceKeywordAsDataIsAccepted(t *testing.T) {
	mustLoad(t, newsSchema("", `,"news.topic":{"const":{"$ref":"#/x"},"examples":[{"$id":"y","default":"z"}]}`))
}

func TestCheck_AcceptsEveryDeclaredShape(t *testing.T) {
	s := mustLoad(t, newsSchema("",
		`,"news.count":{"type":"integer","minimum":1},"news.tags":{"type":"array","items":{"enum":["a","b"]}}`))
	for _, filters := range []map[string]any{
		{"news.language": "en"},
		{"news.count": 3.0},
		{"news.tags": []any{"a", "b"}},
		{"news.language": "el", "news.count": 1.0, "news.tags": []any{}},
	} {
		if v := s.Check(filters); v != nil {
			t.Errorf("Check(%v) = %+v, want accepted", filters, v)
		}
	}
}

func TestCheck_EmptyAndNilAccept(t *testing.T) {
	s := mustLoad(t, newsSchema("", ""))
	var none *searchfilters.Schema
	for _, schema := range []*searchfilters.Schema{s, none} {
		for _, filters := range []map[string]any{nil, {}} {
			if v := schema.Check(filters); v != nil {
				t.Errorf("Check(%v) = %+v, want accepted", filters, v)
			}
		}
	}
}

func TestCheck_NilSchemaRefusesEveryKey(t *testing.T) {
	var none *searchfilters.Schema
	v := none.Check(map[string]any{"news.language": "en", "music.genre": "jazz"})
	if v == nil || !v.Unsupported || v.Key != "music.genre" {
		t.Fatalf("Check = %+v, want the first sorted key unsupported", v)
	}
	if none.Keys() != nil || none.Raw() != nil {
		t.Error("a nil schema declares keys or holds a document")
	}
}

func TestCheck_UnsupportedKeyIsFirstInSortedOrder(t *testing.T) {
	s := mustLoad(t, newsSchema("", ""))
	v := s.Check(map[string]any{"news.language": "en", "news.zone": "x", "news.author": "y"})
	if v == nil || !v.Unsupported || v.Key != "news.author" {
		t.Fatalf("Check = %+v, want news.author unsupported", v)
	}
}

// A value that breaks its key's constraint names the key and the constraint,
// and never the value: the refusal travels back to whoever sent it.
func TestCheck_ValueViolationNamesTheKeyAndTheConstraintNotTheValue(t *testing.T) {
	s := mustLoad(t, newsSchema("", `,"news.tone":{"enum":["calm","urgent"]}`))
	cases := map[string]struct {
		key   string
		value any
		echo  string
	}{
		"pattern": {"news.language", "english-please", "english-please"},
		"type":    {"news.language", 4242.0, "4242"},
		"enum":    {"news.tone", "furious-tone", "furious-tone"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			v := s.Check(map[string]any{c.key: c.value})
			if v == nil || v.Unsupported || v.Key != c.key || v.Constraint == "" {
				t.Fatalf("Check = %+v, want a value violation on %s", v, c.key)
			}
			if strings.Contains(v.Constraint, c.echo) {
				t.Errorf("constraint %q quotes the submitted value", v.Constraint)
			}
		})
	}
}

// With two malformed values, the first in path order is the one reported, so
// the answer does not depend on map order.
func TestCheck_FirstValueViolationInPathOrder(t *testing.T) {
	s := mustLoad(t, newsSchema("", `,"news.tone":{"enum":["calm","urgent"]}`))
	v := s.Check(map[string]any{"news.tone": "furious", "news.language": "english"})
	if v == nil || v.Key != "news.language" {
		t.Fatalf("Check = %+v, want the violation on news.language", v)
	}
}

// A failure deep inside a key's value is reported on the key itself, which is
// what the agent named; the nested property name carries a character RFC 6901
// escapes, so a pointer read naively would split there.
func TestCheck_NestedPropertyPathNamesTheTopKey(t *testing.T) {
	s := mustLoad(t, newsSchema("",
		`,"news.place":{"type":"object","properties":{"a/b":{"type":"string"}}}`))
	v := s.Check(map[string]any{"news.place": map[string]any{"a/b": 5.0}})
	if v == nil || v.Key != "news.place" {
		t.Fatalf("Check = %+v, want the violation on news.place", v)
	}
}

func TestCheck_OversizedObjectIsAWholeObjectViolation(t *testing.T) {
	s := mustLoad(t, newsSchema("", ""))
	v := s.Check(map[string]any{"news.language": strings.Repeat("a", 17000)})
	if v == nil || v.Key != "" || v.Unsupported {
		t.Fatalf("Check = %+v, want a whole-object violation", v)
	}
}
