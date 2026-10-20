package forawellknown

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

//go:embed schema/fora-well-known.json
var manifestSchemaSource []byte

//go:embed schema/fora-wba-directory.json
var wbaSchemaSource []byte

//go:embed schema/fora-key-revocation.json
var revocationSchemaSource []byte

// Compiled once at package init. The embedded schemas are developer-controlled
// build assets, so a compile failure is a build-time invariant violation, not
// a runtime condition — panicking surfaces it at the first import in tests.
var (
	manifestSchema   = mustCompileSchema("fora-well-known.json", manifestSchemaSource)
	wbaSchema        = mustCompileSchema("fora-wba-directory.json", wbaSchemaSource)
	revocationSchema = mustCompileSchema("fora-key-revocation.json", revocationSchemaSource)
)

func mustCompileSchema(name string, source []byte) *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(source))
	if err != nil {
		panic(fmt.Sprintf("forawellknown: parse embedded schema %s: %v", name, err))
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(name, doc); err != nil {
		panic(fmt.Sprintf("forawellknown: add embedded schema %s: %v", name, err))
	}
	sch, err := c.Compile(name)
	if err != nil {
		panic(fmt.Sprintf("forawellknown: compile embedded schema %s: %v", name, err))
	}
	return sch
}

// ValidateManifest applies the document version gate, then checks raw against
// the WellKnownManifest JSON Schema. A refused version returns ErrVersionRefused;
// a document that parsed and broke a rule returns ErrSchemaInvalid, wrapping the
// detailed violation; a body that is not JSON at all returns ErrNotJSON. This
// function performs no fetch and says nothing about one.
//
// The version runs first, and on the raw bytes rather than the decoded message:
// the manifest's rule is that a consumer reads `ver` before it acts on any other
// member, and the schema is itself an act on every member. A document whose
// layout this reader cannot classify is refused without being classified.
func ValidateManifest(raw []byte) error {
	if err := checkManifestVersion(raw); err != nil {
		return err
	}
	return validateAgainst(manifestSchema, raw)
}

// checkManifestVersion reads only the `ver` member out of raw and applies the
// SDK's receive-side rule to it. It answers nil or ErrVersionRefused and nothing
// else. A body that is not JSON, or is JSON but not an object — an array, a
// string, a number, null — is left for the schema to report: that is a shape
// failure rather than a version, and the schema owns the wording of shape
// failures, so this gate must not produce a second spelling of one. A `ver`
// that is present but not a string is treated as absent: it is not a version
// this rule can recognise, and the rule refuses absent.
func checkManifestVersion(raw []byte) error {
	members, isObject := manifestMembers(raw)
	if !isObject {
		return nil
	}
	var ver string
	if member, ok := members["ver"]; ok {
		_ = json.Unmarshal(member, &ver) // a non-string ver stays "", which is refused
	}
	if err := helpers.CheckWellKnownManifestVersion(ver); err != nil {
		return fmt.Errorf("%w: %w", ErrVersionRefused, err)
	}
	return nil
}

// manifestMembers decodes raw as a JSON object, reporting whether it is one at
// all. Both answers are the version gate's business and neither is its failure,
// which is why the question has a name and a bool rather than an error: a body
// that does not decode into an object has no `ver` to read, and the caller
// stands aside for the schema rather than spelling the shape failure a second
// way. `null` is the member of that set worth naming — it decodes into a struct
// as a no-op, so a gate that read `ver` through one saw an absent version and
// diagnosed an origin serving nothing as a version mismatch.
func manifestMembers(raw []byte) (map[string]json.RawMessage, bool) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, false
	}
	return members, members != nil
}

// ValidateWBA checks raw against the WBAFile (WBA directory) JSON Schema.
func ValidateWBA(raw []byte) error {
	return validateAgainst(wbaSchema, raw)
}

// ValidateRevocation checks raw against the KeyRevocationList JSON Schema.
func ValidateRevocation(raw []byte) error {
	return validateAgainst(revocationSchema, raw)
}

func validateAgainst(sch *jsonschema.Schema, raw []byte) error {
	// A body that is not JSON at all is ErrNotJSON, not ErrSchemaInvalid, and the
	// difference is whether a retry can clear it. Nothing was validated here:
	// there is no document to hold to a rule, so this says nothing about what the
	// origin publishes. The shape it actually describes is a proxy or a CDN
	// answering 200 with an error page, or a body truncated in transit, and the
	// next fetch may well return the real document.
	//
	// It is NOT ErrFetch either, and that is the point of the separate sentinel.
	// This function is pure — it is handed bytes and never learns where they came
	// from — so it cannot honestly report a transport event. The functions that
	// do fetch wrap this in ErrFetch on the way out; a caller that read a
	// local file gets the plain answer.
	//
	// ErrSchemaInvalid is the opposite fact — bytes that decoded, and then broke a
	// rule this participant enforces. That one returns the same answer forever, so
	// a caller classifying retryability reads it as final. Folding the two would
	// make every such caller either retry a permanent refusal or give up on a
	// transient one, and the sentinel's own doc records that consumers already
	// disagree about which.
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotJSON, err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("%w: %w", ErrSchemaInvalid, err)
	}
	return nil
}

// decodeValidated runs validate over raw, then protojson-decodes it into msg
// (ignoring unknown fields). The caller passes the schema validator so the
// manifest and revocation-list decode paths share one validate-then-unmarshal
// sequence without sharing a schema.
//
// Everything refused from here down is ErrSchemaInvalid, and every one of them
// is a fact about a document that ARRIVED. validate has already established the
// bytes are JSON — a body that is not gets ErrNotJSON there, because nothing was
// read and the next attempt may return the real document. So a protojson refusal
// is not "it would not decode": the bytes parsed as JSON and satisfied the wire
// schema, and proto-JSON's own mapping is the rule they broke. Same for the
// field constraints below. The next fetch returns the same bytes and the same
// answer, which is what makes these final rather than worth retrying.
func decodeValidated[T proto.Message](raw []byte, msg T, validate func([]byte) error) error {
	if err := validate(raw); err != nil {
		return err
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, msg); err != nil {
		return fmt.Errorf("%w: protojson: %w", ErrSchemaInvalid, err)
	}
	// The embedded schema checks the WIRE SHAPE — which members exist, what JSON
	// type each carries, which enum names are spelled. The protocol's own field
	// and cross-field rules live in the proto as protovalidate constraints, and
	// running them here is what keeps this package from restating them in a
	// second language. A rule written twice drifts, and these two drift
	// invisibly: a producer that only ever emits the valid combination would
	// pass every behavioral test in this repo while the copy disagreed with the
	// protocol about the rest.
	if err := helpers.Validate(msg); err != nil {
		return fmt.Errorf("%w: %w", ErrSchemaInvalid, err)
	}
	return nil
}
