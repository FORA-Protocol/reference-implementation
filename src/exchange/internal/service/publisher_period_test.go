package service

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// The bucket arithmetic is unit-tested here rather than through the totals
// route because the ledger stamps settlements with its own cluster clock, which
// no test can move: an integration test cannot put a settlement on a chosen
// boundary.

func utc(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }

func mustPeriod(t *testing.T, start, end time.Time) Period {
	t.Helper()
	p, err := NewPeriod(start, end)
	if err != nil {
		t.Fatalf("NewPeriod: %v", err)
	}
	return p
}

func assertInvalid(t *testing.T, err error) {
	t.Helper()
	var de *exchange.Error
	if !errors.As(err, &de) || de.Kind != exchange.KindInvalidRequest {
		t.Fatalf("want an invalid_request error, got %v", err)
	}
}

func TestNewPeriod_RefusesAnEmptyBackwardOrUnreadableWindow(t *testing.T) {
	at := utc(2026, 3, 1, 0)
	epoch := time.Unix(0, 0).UTC()
	cases := map[string][2]time.Time{
		"end equals start":           {at, at},
		"end before start":           {at, at.Add(-time.Second)},
		"no start":                   {{}, at},
		"no end":                     {at, {}},
		"starts before the epoch":    {epoch.Add(-time.Nanosecond), at},
		"ends past the ledger clock": {at, utc(2262, 4, 12, 0)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewPeriod(c[0], c[1])
			assertInvalid(t, err)
		})
	}
}

func TestNewPeriod_AcceptsTheLedgerClocksWholeRange(t *testing.T) {
	if _, err := NewPeriod(time.Unix(0, 0), utc(2262, 4, 11, 0)); err != nil {
		t.Fatalf("a period from the epoch to 2262-04-11 is refused: %v", err)
	}
}

func TestBuckets_BoundariesPerGrouping(t *testing.T) {
	cases := []struct {
		name       string
		grouping   Grouping
		start, end time.Time
		want       [][2]time.Time
	}{
		{
			name: "days split at UTC midnight, clipped at both ends", grouping: GroupDay,
			start: utc(2026, 3, 1, 18), end: utc(2026, 3, 3, 6),
			want: [][2]time.Time{
				{utc(2026, 3, 1, 18), utc(2026, 3, 2, 0)},
				{utc(2026, 3, 2, 0), utc(2026, 3, 3, 0)},
				{utc(2026, 3, 3, 0), utc(2026, 3, 3, 6)},
			},
		},
		{
			// 2026-03-04 is a Wednesday; the next ISO week starts Monday 2026-03-09.
			name: "weeks start on Monday", grouping: GroupWeek,
			start: utc(2026, 3, 4, 0), end: utc(2026, 3, 20, 0),
			want: [][2]time.Time{
				{utc(2026, 3, 4, 0), utc(2026, 3, 9, 0)},
				{utc(2026, 3, 9, 0), utc(2026, 3, 16, 0)},
				{utc(2026, 3, 16, 0), utc(2026, 3, 20, 0)},
			},
		},
		{
			// 2026-03-08 is a Sunday, the last day of its ISO week.
			name: "a Sunday closes its week the next day", grouping: GroupWeek,
			start: utc(2026, 3, 8, 12), end: utc(2026, 3, 10, 0),
			want: [][2]time.Time{
				{utc(2026, 3, 8, 12), utc(2026, 3, 9, 0)},
				{utc(2026, 3, 9, 0), utc(2026, 3, 10, 0)},
			},
		},
		{
			name: "months split on the first, across a year end", grouping: GroupMonth,
			start: utc(2025, 12, 15, 0), end: utc(2026, 2, 10, 0),
			want: [][2]time.Time{
				{utc(2025, 12, 15, 0), utc(2026, 1, 1, 0)},
				{utc(2026, 1, 1, 0), utc(2026, 2, 1, 0)},
				{utc(2026, 2, 1, 0), utc(2026, 2, 10, 0)},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Buckets(mustPeriod(t, c.start, c.end), c.grouping)
			if err != nil {
				t.Fatalf("Buckets: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %d buckets %v, want %d", len(got), got, len(c.want))
			}
			for i, w := range c.want {
				if !got[i].Start.Equal(w[0]) || !got[i].End.Equal(w[1]) {
					t.Errorf("bucket %d = [%s, %s), want [%s, %s)", i, got[i].Start, got[i].End, w[0], w[1])
				}
			}
		})
	}
}

func TestBuckets_ALeapYearOfDaysFitsAndOneMoreDayDoesNot(t *testing.T) {
	start := utc(2028, 1, 1, 0)
	got, err := Buckets(mustPeriod(t, start, utc(2029, 1, 1, 0)), GroupDay)
	if err != nil {
		t.Fatalf("366 days: %v", err)
	}
	if len(got) != consoleapi.MaxBuckets {
		t.Fatalf("got %d buckets, want %d", len(got), consoleapi.MaxBuckets)
	}
	_, err = Buckets(mustPeriod(t, start, utc(2029, 1, 1, 1)), GroupDay)
	assertInvalid(t, err)
}

func TestBuckets_RefusesAnUnknownGrouping(t *testing.T) {
	_, err := Buckets(mustPeriod(t, utc(2026, 3, 1, 0), utc(2026, 3, 2, 0)), "hour")
	assertInvalid(t, err)
}

func TestBucketIndex_HalfOpenBounds(t *testing.T) {
	buckets, err := Buckets(mustPeriod(t, utc(2026, 3, 1, 0), utc(2026, 3, 3, 0)), GroupDay)
	if err != nil {
		t.Fatalf("Buckets: %v", err)
	}
	cases := map[string]struct {
		at   time.Time
		want int
	}{
		"first instant":            {utc(2026, 3, 1, 0), 0},
		"last instant of a bucket": {utc(2026, 3, 2, 0).Add(-time.Nanosecond), 0},
		"boundary opens the next":  {utc(2026, 3, 2, 0), 1},
		"period end is outside":    {utc(2026, 3, 3, 0), -1},
		"before the period":        {utc(2026, 2, 28, 23), -1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := bucketIndex(buckets, c.at); got != c.want {
				t.Errorf("bucketIndex(%s) = %d, want %d", c.at, got, c.want)
			}
		})
	}
}

func TestCursor_RoundTripsAndRefusesMalformedInput(t *testing.T) {
	k := repo.Keyset{CreatedAt: time.Date(2026, 3, 1, 9, 30, 0, 123456000, time.UTC), ID: "tx-1"}
	for _, before := range []bool{false, true} {
		got, err := decodeCursor(encodeCursor(k, before))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Key == nil || !got.Key.CreatedAt.Equal(k.CreatedAt) || got.Key.ID != k.ID || got.Before != before {
			t.Fatalf("round trip of %+v before=%v = %+v", k, before, got)
		}
	}
	if first, err := decodeCursor(""); err != nil || first.Key != nil || first.Before {
		t.Fatalf("empty cursor = %+v, %v; want the first page", first, err)
	}
	for _, bad := range []string{"not base64!", "e30", "eyJ0IjoiMjAyNiJ9", "eyJ4IjoxfQ"} {
		_, err := decodeCursor(bad)
		assertInvalid(t, err)
	}
}

// TestPageOf_LinksThePagesBesideIt pins the links of a page read either way,
// with a size of 2 over rows named by the number of their row in the list.
func TestPageOf_LinksThePagesBesideIt(t *testing.T) {
	key := func(i int) repo.Keyset { return repo.Keyset{CreatedAt: utc(2026, 3, 1, 0), ID: strconv.Itoa(i)} }
	keyOf := func(i int) repo.Keyset { return key(i) }
	after := func(i int) string { return encodeCursor(key(i), false) }
	before := func(i int) string { return encodeCursor(key(i), true) }
	cases := []struct {
		name     string
		rows     []int
		pos      repo.PagePosition
		want     []int
		wantLink PageLinks
	}{
		{"first page with more", []int{1, 2, 3}, repo.PagePosition{}, []int{1, 2}, PageLinks{Next: after(2)}},
		{"first page, all of it", []int{1, 2}, repo.PagePosition{}, []int{1, 2}, PageLinks{}},
		{
			"a middle page read forward",
			[]int{3, 4, 5},
			repo.PagePosition{Key: new(key(2))},
			[]int{3, 4},
			PageLinks{Next: after(4), Prev: before(3)},
		},
		{
			"the last page read forward",
			[]int{5},
			repo.PagePosition{Key: new(key(4))},
			[]int{5},
			PageLinks{Prev: before(5)},
		},
		{
			"a middle page read back",
			[]int{2, 3, 4},
			repo.PagePosition{Key: new(key(5)), Before: true},
			[]int{3, 4},
			PageLinks{Next: after(4), Prev: before(3)},
		},
		{
			"the first page read back",
			[]int{1, 2},
			repo.PagePosition{Key: new(key(3)), Before: true},
			[]int{1, 2},
			PageLinks{Next: after(2)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, links := pageOf(c.rows, 2, c.pos, keyOf)
			if !slices.Equal(got, c.want) || links != c.wantLink {
				t.Fatalf("pageOf = %v %+v, want %v %+v", got, links, c.want, c.wantLink)
			}
		})
	}
}

func TestPageSize_DefaultsAndBounds(t *testing.T) {
	if got, err := pageSize(0); err != nil || got != consoleapi.DefaultPageSize {
		t.Fatalf("pageSize(0) = %d, %v", got, err)
	}
	if got, err := pageSize(consoleapi.MaxPageSize); err != nil || got != consoleapi.MaxPageSize {
		t.Fatalf("pageSize(max) = %d, %v", got, err)
	}
	for _, bad := range []int{-1, consoleapi.MaxPageSize + 1} {
		_, err := pageSize(bad)
		assertInvalid(t, err)
	}
}

func TestReportingStatusOf(t *testing.T) {
	now := utc(2026, 3, 2, 0)
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	cases := map[string]struct {
		state    repo.ObligationState
		deadline *time.Time
		want     ReportingStatus
	}{
		"no obligation":             {"", nil, ReportingNotRequired},
		"pending before deadline":   {repo.ObligationStatePending, &future, ReportingPending},
		"pending at the deadline":   {repo.ObligationStatePending, &now, ReportingPending},
		"pending after deadline":    {repo.ObligationStatePending, &past, ReportingOverdue},
		"received after deadline":   {repo.ObligationStateReceived, &past, ReportingReceived},
		"expired without a report":  {repo.ObligationStateExpired, &past, ReportingOverdue},
		"accepted after a report":   {repo.ObligationStateAccepted, &past, ReportingReceived},
		"challenged after a report": {repo.ObligationStateChallenged, &past, ReportingReceived},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ReportingStatusOf(c.state, c.deadline, now)
			if err != nil || got != c.want {
				t.Errorf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestReportingStatusOf_RefusesAnUnknownState(t *testing.T) {
	past := utc(2026, 3, 1, 0)
	_, err := ReportingStatusOf("SETTLED", &past, utc(2026, 3, 2, 0))
	if de, ok := errors.AsType[*exchange.Error](err); !ok || de.Kind != exchange.KindInternal {
		t.Fatalf("a state outside the enum gave %v, want an internal error", err)
	}
}

// obligationMatch is derived from the status rule rather than written out, so
// this pins what the derivation gives, over the states the Exchange never
// writes as well as the two it does.
func TestObligationMatch(t *testing.T) {
	now := utc(2026, 3, 2, 0)
	cases := map[ReportingStatus]repo.ObligationMatch{
		ReportingNotRequired: {None: true, AsOf: now},
		ReportingPending:     {Open: []repo.ObligationState{repo.ObligationStatePending}, AsOf: now},
		ReportingOverdue: {
			Open: []repo.ObligationState{repo.ObligationStateExpired},
			Past: []repo.ObligationState{repo.ObligationStatePending, repo.ObligationStateExpired},
			AsOf: now,
		},
		ReportingReceived: {
			Open: []repo.ObligationState{
				repo.ObligationStateReceived, repo.ObligationStateAccepted, repo.ObligationStateChallenged,
			},
			Past: []repo.ObligationState{
				repo.ObligationStateReceived, repo.ObligationStateAccepted, repo.ObligationStateChallenged,
			},
			AsOf: now,
		},
	}
	for _, status := range ReportingStatuses() {
		t.Run(string(status), func(t *testing.T) {
			got, err := obligationMatch(status, now)
			if err != nil || !reflect.DeepEqual(got, cases[status]) {
				t.Errorf("got %+v, %v; want %+v", got, err, cases[status])
			}
		})
	}
	_, err := obligationMatch("disputed", now)
	assertInvalid(t, err)
}
