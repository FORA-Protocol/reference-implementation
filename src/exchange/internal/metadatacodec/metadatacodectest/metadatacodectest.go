// Package metadatacodectest holds the metadata fixture more than one suite
// arranges: a resource entry that sets every field the stored metadata
// document carries. The codec's own round-trip test, the catalog snapshot's
// decode test and the persistence test all build from it, so a metadata field
// added to one of them reaches every suite at once.
package metadatacodectest

import (
	"testing"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FullEntry builds a ResourceEntry carrying every metadata field, plus the
// domain, path and terms that the stored metadata must NOT carry.
func FullEntry(tb testing.TB) *forav1.ResourceEntry {
	tb.Helper()
	ext, err := structpb.NewStruct(map[string]any{
		"previews": []any{map[string]any{"url": "https://cdn/x.jpg", "media_type": "image/jpeg"}},
	})
	if err != nil {
		tb.Fatalf("ext struct: %v", err)
	}
	claims, err := structpb.NewStruct(map[string]any{"language": "de"})
	if err != nil {
		tb.Fatalf("claims struct: %v", err)
	}
	return &forav1.ResourceEntry{
		Domain:              "publisher.example",
		Path:                "/article/meta",
		Terms:               []*forav1.LicenseTerm{{Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED}},
		ContentId:           proto.String("pub-rt-0001"),
		WordCount:           proto.Int32(812),
		EstimatedQuantity:   proto.Int32(1072),
		ContentHash:         proto.String("sha256:abc"),
		HashMethod:          proto.String("sha256"),
		Source:              forav1.IngestionSource_INGESTION_SOURCE_CMS_API.Enum(),
		ProvenanceSource:    proto.String("wordpress-plugin"),
		ProvenanceTimestamp: timestamppb.New(rfc3339(tb, "2026-03-18T09:30:00Z")),
		ResourceMutability:  forav1.ResourceMutability_RESOURCE_MUTABILITY_STATIC.Enum(),
		Ext:                 ext,
		ExtCritical:         []string{"previews"},
		Attestations: []*forav1.ResourceAttestation{{
			Verifier: "publisher.example", Keyid: "pub-key-1", Uri: "https://publisher.example/article/meta",
			AttestedAt: timestamppb.New(rfc3339(tb, "2026-03-18T09:31:00Z")), Claims: claims, Signature: "FIXTURE_SIG",
		}},
	}
}

// rfc3339 parses a fixture timestamp, failing the test on a malformed value.
func rfc3339(tb testing.TB, s string) time.Time {
	tb.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		tb.Fatalf("parse time %q: %v", s, err)
	}
	return ts
}
