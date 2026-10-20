package dashboard

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
)

func day(s string) time.Time {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParsePeriod_DefaultsToTheMonthSoFar(t *testing.T) {
	t.Parallel()
	// 23:30 at UTC+3 is still the 17th in UTC, the zone every period is in.
	now := time.Date(2026, 2, 17, 23, 30, 0, 0, time.FixedZone("UTC+3", 3*3600))
	got, err := ParsePeriod("", "", now)
	if err != nil {
		t.Fatalf("ParsePeriod: %v", err)
	}
	if !got.From.Equal(day("2026-02-01")) || !got.To.Equal(day("2026-02-17")) {
		t.Fatalf("period = %s..%s, want 2026-02-01..2026-02-17", got.FromValue(), got.ToValue())
	}
	w := got.Window()
	if !w.Start.Equal(day("2026-02-01")) || !w.End.Equal(day("2026-02-18")) {
		t.Fatalf("window = %v..%v, want the half-open month so far", w.Start, w.End)
	}
}

func TestMonthOf_AnEarlierMonthIsWhole(t *testing.T) {
	t.Parallel()
	got := MonthOf(day("2026-01-20"), day("2026-02-17"))
	if !got.From.Equal(day("2026-01-01")) || !got.To.Equal(day("2026-01-31")) {
		t.Fatalf("period = %s..%s, want 2026-01-01..2026-01-31", got.FromValue(), got.ToValue())
	}
}

func TestParsePeriod_ReadsBothDates(t *testing.T) {
	t.Parallel()
	got, err := ParsePeriod("2026-09-01", "2026-09-01", time.Time{})
	if err != nil {
		t.Fatalf("ParsePeriod: %v", err)
	}
	if got.Days() != 1 || !got.Window().End.Equal(day("2026-09-02")) {
		t.Fatalf("one day = %d days ending %v", got.Days(), got.Window().End)
	}
	if got.Label() != "1 Sep 2026 – 1 Sep 2026 (UTC)" {
		t.Fatalf("label = %q", got.Label())
	}
}

func TestParsePeriod_Refusals(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, from, to string }{
		{"only from", "2026-09-01", ""},
		{"only to", "", "2026-09-30"},
		{"from not a date", "yesterday", "2026-09-30"},
		{"to not a date", "2026-09-01", "30/09/2026"},
		{"ends before it starts", "2026-09-30", "2026-09-01"},
		{"longer than a year", "2025-01-01", "2026-01-02"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParsePeriod(tc.from, tc.to, time.Now())
			if de, ok := errors.AsType[*refusal.Error](err); !ok || de.Kind != refusal.KindInvalid || de.Message == "" {
				t.Fatalf("ParsePeriod(%q, %q) = %v, want an invalid refusal with a message", tc.from, tc.to, err)
			}
		})
	}
}

func TestParsePeriod_AcceptsAFullLeapYear(t *testing.T) {
	t.Parallel()
	got, err := ParsePeriod("2028-01-01", "2028-12-31", time.Time{})
	if err != nil || got.Days() != maxPeriodDays {
		t.Fatalf("leap year = %d days, %v; want %d and no error", got.Days(), err, maxPeriodDays)
	}
}

// A list's links carry the Exchange's own cursors: Next the one for the page
// after, Previous the one for the page before, each with the filters and the
// row count that numbers the page it opens.
func TestPaging_LinksForwardAndBack(t *testing.T) {
	t.Parallel()
	filters := TransactionQuery{Period: MonthOf(day("2026-09-10"), day("2026-10-01")), ResourceID: "res 1"}.values()
	at := func(pos Position, rows int, next, prev string) Page {
		return paging{
			path: "/transactions", filters: filters, pos: pos,
			rows: rows, size: 50, of: 230, next: next, prev: prev,
		}.page()
	}

	first := at(Position{}, 50, "c2", "")
	if first.First != 1 || first.Last != 50 || first.Prev != "" {
		t.Fatalf("first page = %+v, want rows 1..50 and no Previous", first)
	}
	if want := "/transactions?cursor=c2&from=2026-09-01&resource=res+1&seen=50&to=2026-09-30"; first.Next != want {
		t.Fatalf("first page next = %q\n want %q", first.Next, want)
	}

	second := at(Position{Cursor: "c2", Seen: 50}, 50, "c3", "b2")
	if want := "/transactions?cursor=b2&from=2026-09-01&resource=res+1&to=2026-09-30"; second.Prev != want {
		t.Fatalf("second page prev = %q\n want the first page %q", second.Prev, want)
	}
	if want := "/transactions?cursor=c3&from=2026-09-01&resource=res+1&seen=100&to=2026-09-30"; second.Next != want {
		t.Fatalf("second page next = %q\n want %q", second.Next, want)
	}

	third := at(Position{Cursor: "c3", Seen: 100}, 20, "", "b3")
	if third.First != 101 || third.Last != 120 || third.Of != 230 || third.Next != "" {
		t.Fatalf("last page = %+v, want rows 101..120 of 230 and no Next", third)
	}
	if want := "/transactions?cursor=b3&from=2026-09-01&resource=res+1&seen=50&to=2026-09-30"; third.Prev != want {
		t.Fatalf("last page prev = %q\n want %q", third.Prev, want)
	}

	// A page reached by Previous has a cursor of its own, and is still the
	// first page when the Exchange gives no cursor before it.
	back := at(Position{Cursor: "b2"}, 50, "c2", "")
	if back.Prev != "" || back.First != 1 {
		t.Fatalf("the first page reached by Previous = %+v, want rows from 1 and no Previous", back)
	}
}

func TestParsePosition(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int{"": 0, "40": 40, "-3": 0, "many": 0} {
		if got := ParsePosition(url.Values{"seen": {in}}).Seen; got != want {
			t.Errorf("seen %q = %d, want %d", in, got, want)
		}
	}
	if got := ParsePosition(url.Values{"cursor": {"c4"}}); got.Cursor != "c4" {
		t.Fatalf("position = %+v, want cursor c4", got)
	}
}
