package dashboard

import (
	"maps"
	"net/url"
	"strconv"
)

// The dashboard's routes. None names a publisher: a signed-in person holds
// exactly one on an Exchange, so the person is the publisher, and each page
// reads the publisher's id from the Exchange rather than from its address.
const (
	PathOverview     = "/overview"
	PathTransactions = "/transactions"
	PathTransaction  = PathTransactions + "/{transaction}"
	PathCatalog      = "/catalog"
	PathResource     = PathCatalog + "/{resource}"
	PathIncome       = "/income"
	PathIncomeCSV    = PathIncome + ".csv"
)

// The sections the console's sidebar lists, in its order.
const (
	SectionOverview     = "overview"
	SectionTransactions = "transactions"
	SectionIncome       = "income"
	SectionCatalog      = "catalog"
	SectionDomains      = "domains"
)

// Nav is a dashboard page's place: the publisher its reads are for, and the
// section the sidebar marks as current.
type Nav struct {
	PublisherID string
	Section     string
	// Domains are the publisher's sites, for the filter each list offers.
	// They come from the registrations every dashboard page already reads to
	// learn which publisher the person holds, so the filter costs no second
	// call.
	Domains []string
}

// Overview is the address of the overview.
func (n Nav) Overview() string { return PathOverview }

// Transactions is the address of the transaction list.
func (n Nav) Transactions() string { return PathTransactions }

// Income is the address of the income report.
func (n Nav) Income() string { return PathIncome }

// IncomeCSV is the address of the income report as a file.
func (n Nav) IncomeCSV() string { return PathIncomeCSV }

// Catalog is the address of the catalog.
func (n Nav) Catalog() string { return PathCatalog }

// Resource is the address of one catalog entry.
func (n Nav) Resource(id string) string { return PathCatalog + "/" + url.PathEscape(id) }

// Transaction is the address of one transaction.
func (n Nav) Transaction(id string) string { return PathTransactions + "/" + url.PathEscape(id) }

// Is reports whether the current page is in section.
func (n Nav) Is(section string) bool { return n.Section == section }

// withQuery is path with q appended, or path alone when q is empty.
func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// Page is where a paged list is and how to move through it. First and Last
// are the row numbers shown, counted from 1, and both are 0 on an empty page.
// Of is how many rows the whole list holds, or -1 when that is not known.
// Prev is the address of the page before, empty on the first page, and Next
// the address of the page after, empty on the last.
type Page struct {
	First int
	Last  int
	Of    int64
	Prev  string
	Next  string
}

// Position is where a list page is in its list. Cursor is the Exchange's
// cursor that opened the page, empty on the first one: a Next link carries the
// cursor the Exchange gave for the page after, and a Previous link the one it
// gave for the page before. Seen is how many rows the pages before it showed;
// it only numbers the rows.
type Position struct {
	Cursor string
	Seen   int
}

// ParsePosition reads a list page's position from its address. A count that
// is not one is taken as none, because it only labels the page.
func ParsePosition(q url.Values) Position {
	seen, err := strconv.Atoi(q.Get("seen"))
	if err != nil || seen < 0 {
		seen = 0
	}
	return Position{Cursor: q.Get("cursor"), Seen: seen}
}

// paging is one page of rows about to be shown, and what its links are built
// from.
type paging struct {
	path string
	// filters is the list's query without its position. Every link carries
	// it, so a filter holds on every page, forward and back.
	filters url.Values
	pos     Position
	rows    int
	// size is the rows per page. Every page before this one was a full page,
	// so the page before starts size rows earlier.
	size int
	of   int64
	// next is the Exchange's cursor for the page after, empty on the last,
	// and prev its cursor for the page before, empty on the first.
	next string
	prev string
}

func (p paging) page() Page {
	out := Page{Of: p.of}
	if p.rows > 0 {
		out.First, out.Last = p.pos.Seen+1, p.pos.Seen+p.rows
	}
	if p.next != "" {
		out.Next = p.link(Position{Cursor: p.next, Seen: p.pos.Seen + p.rows})
	}
	if p.prev != "" {
		out.Prev = p.link(Position{Cursor: p.prev, Seen: max(0, p.pos.Seen-p.size)})
	}
	return out
}

// link is the address of the list at pos, with the list's filters.
func (p paging) link(pos Position) string {
	q := maps.Clone(p.filters)
	if q == nil {
		q = url.Values{}
	}
	if pos.Cursor != "" {
		q.Set("cursor", pos.Cursor)
	}
	if pos.Seen > 0 {
		q.Set("seen", strconv.Itoa(pos.Seen))
	}
	return withQuery(p.path, q)
}
