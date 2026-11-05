package searchfilters

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

// Dialect is the one $schema value a filter schema may name: the protocol SDK's
// registration-schema dialect, written one way, without the trailing "#" the SDK
// also accepts (the package comment says why).
const Dialect = helpers.RegistrationSchemaDialect

// Schema is a loaded filter schema. A nil *Schema is no schema: it declares no
// key, so every filter is refused as one the search does not apply.
type Schema struct {
	raw      []byte
	keys     []string
	declared map[string]bool
	compiled *helpers.RegistrationSchema
}

// Violation is the first reason a request's filters are refused. Key is the
// offending key, or "" for the whole object. Unsupported means the vocabulary
// does not declare the key; otherwise its value breaks the key's constraint.
// Constraint states the rule that was broken and never quotes the submitted
// value.
type Violation struct {
	Key         string
	Unsupported bool
	Constraint  string
}

// topLevelKeywords are the keywords a filter schema may use at its top level:
// annotations, and the three that make it a flat object of keys. Anything else
// there is a rule over the whole object.
var topLevelKeywords = map[string]bool{
	"$schema": true, "$comment": true, "title": true, "description": true,
	"type": true, "properties": true, "additionalProperties": true,
}

// referenceKeywords create or follow a reference. In sorted order, so the first
// one a document uses is reported the same way on every run.
var referenceKeywords = []string{"$anchor", "$defs", "$dynamicAnchor", "$dynamicRef", "$id", "$ref"}

// defaultKeyword is refused for what the MCP SDK does with it: the identity
// service's tool fills a declared default into every call that lacks the key,
// URL-only and empty calls included, while the Broker applies no default to a
// request that reaches it directly. A default that breaks its own key's rule
// would also stop the tool from mounting.
var defaultKeyword = []string{"default"}

// dataKeywords hold values, not schemas, so a keyword inside one is a value a
// filter may equal.
var dataKeywords = map[string]bool{"const": true, "default": true, "enum": true, "examples": true}

// namedSchemaKeywords hold schemas under names; the names are not keywords.
var namedSchemaKeywords = map[string]bool{"properties": true, "patternProperties": true, "dependentSchemas": true}

// keySegment is one dot-separated part of a filter key: how a search template
// names it, as filters.<segment>.<segment>.
var keySegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// The constraints of an unsupported key: with no schema, and with a schema that
// does not declare it.
const (
	noFilterApplied   = "this Broker's catalog search applies no search filter"
	notAFilterApplied = "not a filter this Broker's catalog search applies"
)

// Load compiles a filter schema and checks the rules in the package comment.
// Blank input, JSON whitespace only, is no schema: nil and no error. A refusal
// names the rule it broke.
func Load(raw []byte) (*Schema, error) {
	compiled, verdict := helpers.CompileRegistrationSchema(raw)
	switch verdict {
	case helpers.SchemaNotPublished:
		return nil, nil
	case helpers.SchemaAccepted:
	default:
		return nil, fmt.Errorf("refused by the protocol SDK's schema rules: %s", verdict)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		// The compiler parsed the same bytes as an object, so this cannot fail.
		return nil, fmt.Errorf("decode: %w", err)
	}
	if kw := keywordIn(doc, referenceKeywords); kw != "" {
		return nil, fmt.Errorf("uses %s: a filter schema is one self-contained document with no reference keyword", kw)
	}
	if keywordIn(doc, defaultKeyword) != "" {
		return nil, errors.New("uses default: a filter applies only when the agent sends it, so no key has a default")
	}
	keys, err := declaredKeys(doc)
	if err != nil {
		return nil, err
	}
	declared := make(map[string]bool, len(keys))
	for _, k := range keys {
		declared[k] = true
	}
	return &Schema{raw: slices.Clone(raw), keys: keys, declared: declared, compiled: compiled}, nil
}

// keywordIn returns the first of keywords that node uses as a keyword, at any
// depth, or "".
func keywordIn(node any, keywords []string) string {
	switch n := node.(type) {
	case map[string]any:
		for _, kw := range keywords {
			if _, used := n[kw]; used {
				return kw
			}
		}
		for _, k := range sortedKeys(n) {
			if kw := keywordUnder(k, n[k], keywords); kw != "" {
				return kw
			}
		}
	case []any:
		for _, item := range n {
			if kw := keywordIn(item, keywords); kw != "" {
				return kw
			}
		}
	}
	return ""
}

// keywordUnder reads the value of keyword k as a schema, a map of named schemas,
// or data.
func keywordUnder(k string, v any, keywords []string) string {
	if dataKeywords[k] {
		return ""
	}
	named, isMap := v.(map[string]any)
	if !isMap || !namedSchemaKeywords[k] {
		return keywordIn(v, keywords)
	}
	for _, name := range sortedKeys(named) {
		if kw := keywordIn(named[name], keywords); kw != "" {
			return kw
		}
	}
	return ""
}

// declaredKeys checks the top-level rules and returns the declared keys, sorted.
func declaredKeys(doc map[string]any) ([]string, error) {
	for _, k := range sortedKeys(doc) {
		if !topLevelKeywords[k] {
			return nil, fmt.Errorf(
				"%s at the top level: a filter schema constrains each key on its own and nothing over the whole object", k)
		}
	}
	if doc["type"] != "object" {
		return nil, errors.New(`type must be "object"`)
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return nil, errors.New("properties must declare at least one filter key")
	}
	if v, present := doc["additionalProperties"]; !present || v != false {
		return nil, errors.New(
			"additionalProperties must be false, so a key outside the schema is refused rather than ignored")
	}
	if s, present := doc["$schema"]; present && s != Dialect {
		return nil, fmt.Errorf("$schema must be absent or exactly %q", Dialect)
	}
	keys := sortedKeys(props)
	for _, k := range keys {
		if err := keyProblem(k, props); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// keyProblem checks one declared key's spelling, and that no shorter declared
// key is one of its dot-prefixes.
func keyProblem(key string, declared map[string]any) error {
	segments := strings.Split(key, ".")
	for _, seg := range segments {
		if !keySegment.MatchString(seg) {
			return fmt.Errorf("key %q: each dot-separated part must be letters, digits, _ or -", key)
		}
	}
	for i := 1; i < len(segments); i++ {
		prefix := strings.Join(segments[:i], ".")
		if _, clash := declared[prefix]; clash {
			return fmt.Errorf("key %q: %q is also a key, so the template would see filters.%s as both a value and an object",
				key, prefix, prefix)
		}
	}
	return nil
}

// Keys returns the declared filter keys, sorted. A nil Schema has none.
func (s *Schema) Keys() []string {
	if s == nil {
		return nil
	}
	return slices.Clone(s.keys)
}

// Raw returns a copy of the document as it was loaded.
func (s *Schema) Raw() json.RawMessage {
	if s == nil {
		return nil
	}
	return slices.Clone(s.raw)
}

// Check returns the first reason filters are refused, or nil when they are
// accepted. An absent or empty object is accepted. The object is bounded first,
// as the protocol bounds any submitted object; then a key the schema does not
// declare is refused, the first in sorted order; then the first value that
// breaks its constraint, in the order of the failing paths. The result never
// quotes a submitted value.
func (s *Schema) Check(filters map[string]any) *Violation {
	if len(filters) == 0 {
		return nil
	}
	if v := helpers.CheckRegistrationData(filters); v != helpers.RegistrationDataAccepted {
		return &Violation{Constraint: "exceeds the bounds on a submitted object: " + v.String()}
	}
	keys := sortedKeys(filters)
	if s == nil {
		return &Violation{Key: keys[0], Unsupported: true, Constraint: noFilterApplied}
	}
	for _, k := range keys {
		if !s.declared[k] {
			return &Violation{Key: k, Unsupported: true, Constraint: notAFilterApplied}
		}
	}
	failures := s.compiled.Validate(filters)
	if len(failures) == 0 {
		return nil
	}
	return &Violation{Key: topKey(failures[0].GetPath()), Constraint: failures[0].GetError()}
}

// topKey is the first segment of an RFC 6901 pointer into the filters object,
// or "" for the object itself. A declared key holds neither '~' nor '/'
// (keySegment), so the segment is the key as written.
func topKey(pointer string) string {
	key, _, _ := strings.Cut(strings.TrimPrefix(pointer, "/"), "/")
	return key
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
