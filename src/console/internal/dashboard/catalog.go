package dashboard

import (
	"context"
	"encoding/json"
	"net/url"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// CatalogQuery is what a catalog page reads: which of the publisher's
// domains to show, and where in the list the page is.
type CatalogQuery struct {
	Domain   string
	Position Position
}

// CatalogList is one page of the publisher's resources, newest first.
type CatalogList struct {
	Rows []CatalogRow
	Page Page
	// Domain is the site the page is narrowed to, empty for all of them, and
	// Domains are the ones the filter offers.
	Domain  string
	Domains []string
}

// CatalogRow is one resource as the list shows it. The terms and the sales
// are on the resource's own page, so the list stays one line per resource.
type CatalogRow struct {
	URI     string
	Title   string
	Updated string
	// Link is the resource's own page.
	Link string
}

// ResourceView is one catalog entry on its own page: what it is, the terms it
// is offered under, and where its sales are listed.
type ResourceView struct {
	ResourceID string
	URI        string
	Title      string
	// Terms says each license term the resource was listed under, one line
	// per term.
	Terms   []string
	Created string
	Updated string
	// Transactions is this resource's sales over the current month.
	Transactions string
	// Income is what this resource earned over the current month.
	Income string
	// Catalog is the list this resource came from.
	Catalog string
}

// Catalog reads one page of the publisher's resources.
func (s *Service) Catalog(
	ctx context.Context, actor exchangeclient.Actor, nav Nav, q CatalogQuery,
) (CatalogList, error) {
	got, err := s.reader.ListCatalog(ctx, exchangeclient.ListCatalogRequest{
		Actor: actor, PublisherID: nav.PublisherID, Domain: q.Domain,
		PageSize: s.pageSize, Cursor: q.Position.Cursor,
	})
	if err != nil {
		return CatalogList{}, failure(err)
	}
	rows := make([]CatalogRow, 0, len(got.Resources))
	for _, r := range got.Resources {
		rows = append(rows, CatalogRow{
			URI:     r.URI,
			Title:   r.Title,
			Updated: timeText(r.UpdatedAt),
			Link:    nav.Resource(r.ResourceID),
		})
	}
	filters := url.Values{}
	if q.Domain != "" {
		filters.Set("domain", q.Domain)
	}
	return CatalogList{
		Rows: rows,
		Page: paging{
			path: nav.Catalog(), filters: filters, pos: q.Position,
			rows: len(rows), size: s.pageSize, of: -1, next: got.NextCursor, prev: got.PrevCursor,
		}.page(),
		Domain:  q.Domain,
		Domains: nav.Domains,
	}, nil
}

// Resource reads one of the publisher's catalog entries for its own page.
func (s *Service) Resource(
	ctx context.Context, actor exchangeclient.Actor, nav Nav, resourceID string,
) (ResourceView, error) {
	got, err := s.reader.CatalogDetail(ctx, exchangeclient.CatalogDetailRequest{
		Actor: actor, PublisherID: nav.PublisherID, ResourceID: resourceID,
	})
	if err != nil {
		return ResourceView{}, failure(err)
	}
	now := s.clk.Now()
	month := MonthOf(now, now)
	filter := TransactionQuery{Period: month, ResourceID: got.ResourceID}
	return ResourceView{
		ResourceID:   got.ResourceID,
		URI:          got.URI,
		Title:        got.Title,
		Terms:        termsText(got.Terms),
		Created:      timeText(got.CreatedAt),
		Updated:      timeText(got.UpdatedAt),
		Transactions: withQuery(nav.Transactions(), filter.values()),
		Income:       withQuery(nav.Income(), IncomeQuery{Period: month, ResourceID: got.ResourceID}.values()),
		Catalog:      nav.Catalog(),
	}, nil
}

// termsText says each term of a resource's term list, the protocol JSON the
// catalog stores. A list that does not read is said to be unreadable rather
// than shown as no terms, which would read as a free resource.
func termsText(raw json.RawMessage) []string {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return []string{"(the terms could not be read)"}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		var term forav1.LicenseTerm
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(item, &term); err != nil {
			out = append(out, "(a term that could not be read)")
			continue
		}
		out = append(out, termspreset.DescribeTerm(&term))
	}
	return out
}

// describeTerms says each of terms, one line per term.
func describeTerms(terms []*forav1.LicenseTerm) []string {
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		out = append(out, termspreset.DescribeTerm(term))
	}
	return out
}
