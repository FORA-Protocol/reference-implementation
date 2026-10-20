// Package termscodec is the one encoding of a license-term list as stored JSON:
// fora.catalog.terms, written by the catalog push and read back by discovery.
// Writer and reader go through the same pair of functions, so a term that is
// stored is the term that is read back.
package termscodec

import (
	"encoding/json"
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Marshal serializes the repeated LicenseTerm into the JSONB array stored in
// catalog.terms. Terms reaching the catalog are
// canonicalized in place (the SDK's helpers.NormalizeResourceEntry) and have
// passed the SDK's ingest-tier checks (helpers.ValidateLicenseTerm) before this
// runs, so the persisted document is canonical. Each element is rendered with
// protojson proto (snake_case) field names so it round-trips back into a
// forav1.LicenseTerm via Unmarshal without surprise.
func Marshal(terms []*forav1.LicenseTerm) ([]byte, error) {
	if len(terms) == 0 {
		return []byte(`[]`), nil
	}
	mo := protojson.MarshalOptions{UseProtoNames: true}
	out := make([]byte, 0, len(terms)*64)
	out = append(out, '[')
	for i, t := range terms {
		if i > 0 {
			out = append(out, ',')
		}
		elem, err := mo.Marshal(t)
		if err != nil {
			return nil, fmt.Errorf("marshal term %d: %w", i, err)
		}
		out = append(out, elem...)
	}
	out = append(out, ']')
	return out, nil
}

// Unmarshal decodes a JSONB array produced by Marshal back into a slice of
// LicenseTerm. It is the read-side inverse used by discovery to project
// persisted terms onto Offer.terms. Empty input decodes to nil.
func Unmarshal(raw []byte) ([]*forav1.LicenseTerm, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, fmt.Errorf("decode terms array: %w", err)
	}
	out := make([]*forav1.LicenseTerm, 0, len(elems))
	for i, e := range elems {
		var term forav1.LicenseTerm
		if err := protojson.Unmarshal(e, &term); err != nil {
			return nil, fmt.Errorf("decode term %d: %w", i, err)
		}
		out = append(out, &term)
	}
	return out, nil
}
