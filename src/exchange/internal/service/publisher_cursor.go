package service

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// pageSize resolves a requested page size against the paging bounds consoleapi
// states. A request that names none gets the default; one that asks for more
// than the maximum is refused rather than capped, so a caller never mistakes a
// capped page for the whole list. The refusal names the limit, so a caller
// learns it from the answer.
func pageSize(requested int) (int32, error) {
	switch {
	case requested == 0:
		return consoleapi.DefaultPageSize, nil
	case requested < 0 || requested > consoleapi.MaxPageSize:
		return 0, exchange.Newf(exchange.KindInvalidRequest,
			"page_size must be between 1 and %d", consoleapi.MaxPageSize)
	default:
		return int32(requested), nil
	}
}

// cursorBody is what a cursor carries: the creation time and id of the row a
// page starts beside, and which side of it the page is. The time is kept to
// the microsecond, which is what Postgres stores, so a cursor names exactly the
// row it came from.
type cursorBody struct {
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"id"`
	// Before marks a cursor for the page above the row, which a Previous link
	// carries. A cursor without it is for the page below the row.
	Before bool `json:"b,omitempty"`
}

// encodeCursor makes the opaque token a caller passes back for the page on the
// before side of k, or the after side.
func encodeCursor(k repo.Keyset, before bool) string {
	raw, err := json.Marshal(cursorBody{CreatedAt: k.CreatedAt.UTC(), ID: k.ID, Before: before})
	if err != nil {
		// A struct of a time, a string and a bool always marshals.
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCursor reads a token encodeCursor made. An empty token is the first
// page. Anything else that does not decode to a time and an id is refused: a
// cursor is opaque to the caller, so a malformed one is a caller fault, not a
// request for the first page.
func decodeCursor(token string) (repo.PagePosition, error) {
	if token == "" {
		return repo.PagePosition{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return repo.PagePosition{}, errMalformedCursor()
	}
	var body cursorBody
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.CreatedAt.IsZero() || body.ID == "" {
		return repo.PagePosition{}, errMalformedCursor()
	}
	return repo.PagePosition{Key: &repo.Keyset{CreatedAt: body.CreatedAt, ID: body.ID}, Before: body.Before}, nil
}

func errMalformedCursor() error {
	return exchange.Newf(exchange.KindInvalidRequest, "cursor is malformed")
}

// PageLinks are the cursors of the pages beside one: Next reads the page
// below it and Prev the page above it. Each is empty when there is no such
// page.
type PageLinks struct {
	Next string
	Prev string
}

// pageOf trims a page read with one row more than size, and returns the
// cursors of the pages beside it. A read below a key, or of the first page,
// finds its extra row at the bottom: there is a next page. A read above a key
// finds it at the top: there is a page before this one. The page read from a
// key always has the key's own page on its other side.
func pageOf[T any](rows []T, size int32, pos repo.PagePosition, keyset func(T) repo.Keyset) ([]T, PageLinks) {
	more := len(rows) > int(size)
	var links PageLinks
	if pos.Before {
		if more {
			rows = rows[len(rows)-int(size):]
			links.Prev = encodeCursor(keyset(rows[0]), true)
		}
		if len(rows) > 0 {
			links.Next = encodeCursor(keyset(rows[len(rows)-1]), false)
		}
		return rows, links
	}
	if more {
		rows = rows[:size]
		links.Next = encodeCursor(keyset(rows[len(rows)-1]), false)
	}
	if pos.Key != nil && len(rows) > 0 {
		links.Prev = encodeCursor(keyset(rows[0]), true)
	}
	return rows, links
}
