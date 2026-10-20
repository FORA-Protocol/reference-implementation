package termspreset

import (
	"encoding/json"
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraoffer"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/publicerr"
)

// OfferInput is everything the preview knows about one page.
type OfferInput struct {
	// Exchange is the domain of the Exchange the deployment would list.
	Exchange string
	// CanonicalURL is the page's address, as the page itself declares it.
	CanonicalURL string
	// Title is the page's headline, when it has one.
	Title string
	// Terms are the ways this page is licensed. Each one becomes its own
	// sample offer, in the publisher's order.
	Terms []*forav1.LicenseTerm
}

// BuildOffers assembles the offers an agent would receive for this page: one
// per license term, in the order the publisher gave them. An offer sells
// exactly one term and carries that term's price, so a page licensed two ways
// — free for academic use, paid per unit — previews as two offers, which is
// what discovery answers with.
//
// The shape is not written here: it comes from the projection the Exchange's
// own discovery path uses, so a field the Exchange adds reaches the preview
// with it rather than leaving the preview quietly answering with an older
// shape. What this function owns is the preview's half — checking there is a
// term to price from. The shared pricing projection normalizes its price
// exactly as discovery does without modifying the input term.
//
// Three fields are deliberately absent from every offer, each of which only an
// Exchange can produce: the offer id, which an Exchange assigns; the expiry,
// which is a window an Exchange commits to; and the signature, which is the
// Exchange's statement that the price and terms are its own. Leaving them out
// is what makes this a preview rather than something that could be mistaken
// for an offer and replayed.
func BuildOffers(in OfferInput) ([]*forav1.Offer, error) {
	if len(in.Terms) == 0 {
		return nil, publicerr.Newf(publicerr.KindInternal, "an offer needs at least one term")
	}
	offers := make([]*forav1.Offer, 0, len(in.Terms))
	for i, term := range in.Terms {
		pricing, err := foraoffer.PricingFromTerm(term)
		if err != nil {
			return nil, publicerr.Wrap(publicerr.KindInternal, fmt.Errorf("term %d pricing: %w", i, err),
				"the offer could not be priced from these terms")
		}
		resource := foraoffer.Resource{
			Exchange:     in.Exchange,
			CanonicalURL: in.CanonicalURL,
			Pricing:      pricing,
			Terms:        []*forav1.LicenseTerm{term},
		}
		if in.Title != "" {
			resource.Title = strPtr(in.Title)
		}
		offers = append(offers, foraoffer.Build(resource))
	}
	return offers, nil
}

// Verdict is what the Exchange would say about these terms for this page.
//
// It runs the SDK's entry validation, which is the last of the three stages
// fora-ingest --check runs and the only one that has anything to say about a
// term set on its own. The other two — parsing a feed line and mapping it —
// are about the text of a feed, and the preview walks them separately over the
// sample line it renders, so between the two nothing on a publisher's path to
// the Exchange is asserted rather than run.
//
// A term set that fails is reported with its findings, never quietly
// corrected: correcting it would mean showing a publisher terms they did not
// choose.
//
// The entry is normalized first, as the Exchange normalizes a pushed entry
// before validating it. Every ingest-tier check but one reads its tokens as
// already canonical, so validating an unnormalized entry answers a question
// about spellings rather than about tokens — and would report a violation the
// Exchange itself would not.
func Verdict(domain, path, title string, licenseTerms []*forav1.LicenseTerm) helpers.EntryVerdict {
	entry := &forav1.ResourceEntry{
		Domain: domain,
		Path:   path,
		Terms:  licenseTerms,
	}
	if title != "" {
		entry.Title = strPtr(title)
	}
	helpers.NormalizeResourceEntry(entry)
	return helpers.ValidateResourceEntry(entry)
}

// RenderProtoJSON renders a protocol message the way the wire carries it:
// snake_case field names, enum values as names, and unset fields left out.
// The page shows this to a publisher, so it has to be the same text an agent
// would receive rather than a second rendering with its own conventions.
func RenderProtoJSON(m proto.Message) (json.RawMessage, error) {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	return json.RawMessage(raw), nil
}

func strPtr(s string) *string { return &s }
