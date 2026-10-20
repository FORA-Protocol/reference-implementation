package metadatacodec_test

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/proto"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/metadatacodec"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/metadatacodec/metadatacodectest"
)

// TestMarshalRoundTrip asserts the metadata projection round-trips: every
// metadata field survives Marshal->Unmarshal, and the non-metadata fields
// (domain/path/terms) are intentionally absent.
func TestMarshalRoundTrip(t *testing.T) {
	in := metadatacodectest.FullEntry(t)
	raw, err := metadatacodec.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if raw == nil {
		t.Fatal("expected non-nil metadata JSON for a metadata-bearing entry")
	}
	got, err := metadatacodec.Unmarshal(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The projection must preserve EVERY metadata field and drop ONLY the
	// non-metadata columns (domain/path/title/terms). Deriving want from in --
	// rather than re-listing the metadata fields -- means a field that Marshal
	// forgets to project fails this test instead of passing by parallel
	// omission.
	want := proto.Clone(in).(*forav1.ResourceEntry)
	want.Domain = ""
	want.Path = ""
	want.Title = nil
	want.Terms = nil
	if !proto.Equal(got, want) {
		t.Errorf("round-trip mismatch:\n got=%v\nwant=%v", got, want)
	}
	if got.GetDomain() != "" || got.GetPath() != "" || len(got.GetTerms()) != 0 {
		t.Errorf("projection leaked non-metadata fields: domain=%q path=%q terms=%d",
			got.GetDomain(), got.GetPath(), len(got.GetTerms()))
	}
}

// TestMarshalEmpty asserts an entry without metadata marshals to nil so the
// nullable catalog.metadata column stays NULL.
func TestMarshalEmpty(t *testing.T) {
	raw, err := metadatacodec.Marshal(&forav1.ResourceEntry{Domain: "d", Path: "/p"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if raw != nil {
		t.Errorf("expected nil metadata for an entry with no metadata fields, got %s", raw)
	}
	got, err := metadatacodec.Unmarshal(nil)
	if err != nil {
		t.Fatalf("unmarshal(nil): %v", err)
	}
	if got != nil {
		t.Errorf("expected nil entry decoding nil metadata, got %v", got)
	}
}

// TestUnmarshalRefusesADocumentItCannotRead asserts a stored document that is
// not the codec's JSON is an error rather than an empty entry, so a reader
// never mistakes an unreadable document for "no metadata".
func TestUnmarshalRefusesADocumentItCannotRead(t *testing.T) {
	if got, err := metadatacodec.Unmarshal([]byte(`{"content_hash": 7}`)); err == nil {
		t.Fatalf("Unmarshal of a mistyped document = %v, nil; want an error", got)
	}
}
