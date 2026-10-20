package service

import (
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// applyMetadata projects a row's decoded resource extension metadata onto the
// Offer. It must be called BEFORE the offer is signed so every field
// is signature-covered, and it derives ONLY from the stored metadata (no clock,
// no requester state), so a snapshot rebuild renders the same projection for
// the same stored row. resource_mutability is sourced from the typed
// ingest field; previews is still promoted from ext (its typed promotion is a
// separate follow-up); the full ext is also carried verbatim.
func applyMetadata(offer *forav1.Offer, md *forav1.ResourceEntry) {
	if offer.Identity == nil {
		offer.Identity = &forav1.ResourceIdentity{}
	}
	offer.Identity.ContentHash = md.ContentHash
	offer.Identity.HashMethod = md.HashMethod
	// Mutability is OVERRIDDEN only when the publisher explicitly supplied a typed
	// resource_mutability value; otherwise the offer keeps buildOffer's STATIC
	// default. buildOffer documents that default as the value that satisfies the
	// proto rule ResourceIdentity.resource_mutability {not_in:[0]} (now enforced at
	// the execute request boundary). The typed field is an optional pointer, so a
	// nil (omitted) value leaves the default in place rather than downgrading it to
	// UNSPECIFIED — which would produce an offer that fails its OWN
	// ExecuteTransaction validation. Only an explicit publisher value moves it.
	if md.ResourceMutability != nil {
		offer.Identity.ResourceMutability = md.GetResourceMutability()
	}
	offer.Previews = extPreviews(md.Ext)
	offer.Attestations = md.Attestations
	offer.Ext = md.Ext
	offer.ExtCritical = md.ExtCritical
	offer.DataAsOf = md.ProvenanceTimestamp
}

// extPreviews promotes the ext.previews array into typed Offer previews. An
// absent or empty array yields nil (no previews).
func extPreviews(ext *structpb.Struct) []*forav1.Preview {
	lv := ext.GetFields()["previews"].GetListValue()
	if lv == nil {
		return nil
	}
	out := make([]*forav1.Preview, 0, len(lv.GetValues()))
	for _, v := range lv.GetValues() {
		if s := v.GetStructValue(); s != nil {
			out = append(out, previewFromStruct(s))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// previewFromStruct maps one ext.previews element into a typed Preview. Optional
// dimensions (width/height/duration/size) are set only when present so the
// emitted preview is proto-equal to what the publisher pushed.
func previewFromStruct(s *structpb.Struct) *forav1.Preview {
	f := s.GetFields()
	p := &forav1.Preview{
		Url:       f["url"].GetStringValue(),
		MediaType: f["media_type"].GetStringValue(),
	}
	if v, ok := f["width"]; ok {
		w := int32(v.GetNumberValue())
		p.Width = &w
	}
	if v, ok := f["height"]; ok {
		h := int32(v.GetNumberValue())
		p.Height = &h
	}
	if v, ok := f["duration"]; ok {
		d := int32(v.GetNumberValue())
		p.Duration = &d
	}
	if v, ok := f["size"]; ok {
		sz := v.GetStringValue()
		p.Size = &sz
	}
	return p
}
