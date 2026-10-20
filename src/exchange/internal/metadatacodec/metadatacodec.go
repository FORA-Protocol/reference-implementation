// Package metadatacodec is the one encoding of a resource's extension metadata
// as stored JSON: fora.catalog.metadata, written by the catalog push and read
// back by discovery and by the automatic import, which sends a stored page's
// metadata back when it pushes that page again. Writer and readers go through
// the same pair of functions, so the metadata that is stored is the metadata
// that is read back.
package metadatacodec

import (
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Marshal serializes the resource extension metadata of a ResourceEntry into
// the JSONB document stored in catalog.metadata. It projects ONLY the metadata
// fields (never domain/path/title/terms -- those live in their own columns) so
// the persisted document is a focused, self-describing record that Unmarshal
// round-trips back into a ResourceEntry carrying just the metadata. Rendered
// with protojson snake_case, mirroring termscodec.Marshal.
//
// When the entry carries no metadata the result is nil, so the nullable
// catalog.metadata column stays NULL (no empty-object sentinel).
func Marshal(e *forav1.ResourceEntry) ([]byte, error) {
	proj := &forav1.ResourceEntry{
		ContentId:           e.ContentId,
		WordCount:           e.WordCount,
		EstimatedQuantity:   e.EstimatedQuantity,
		ContentHash:         e.ContentHash,
		HashMethod:          e.HashMethod,
		Source:              e.Source,
		ProvenanceSource:    e.ProvenanceSource,
		ProvenanceTimestamp: e.ProvenanceTimestamp,
		Attestations:        e.Attestations,
		ResourceMutability:  e.ResourceMutability,
		Ext:                 e.Ext,
		ExtCritical:         e.ExtCritical,
	}
	if proto.Equal(proj, &forav1.ResourceEntry{}) {
		return nil, nil
	}
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(proj)
	if err != nil {
		return nil, fmt.Errorf("marshal resource metadata: %w", err)
	}
	return b, nil
}

// Unmarshal decodes the catalog.metadata JSONB document (produced by Marshal)
// back into a ResourceEntry carrying only the metadata fields. A NULL/empty
// column decodes to nil.
func Unmarshal(raw []byte) (*forav1.ResourceEntry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var e forav1.ResourceEntry
	if err := protojson.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("decode resource metadata: %w", err)
	}
	return &e, nil
}
