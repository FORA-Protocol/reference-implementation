package service

import (
	"math"
	"slices"
	"strings"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// Period is a half-open UTC window: Start is inside it, End is not. A totals
// bucket is a Period too, clipped to the period it splits.
type Period struct {
	Start time.Time
	End   time.Time
}

// The earliest and latest instants a period may name. The ledger counts time
// in nanoseconds from the Unix epoch in an int64, so it cannot represent an
// instant before the epoch or after the year 2262, and a period that reached
// outside them could not be read from it.
var (
	earliestPeriodTime = time.Unix(0, 0).UTC()
	latestPeriodTime   = time.Unix(0, math.MaxInt64).UTC()
)

// NewPeriod validates a window a publisher asked for. Both bounds are
// required and End must be after Start: a read that silently meant
// "everything" or "nothing" would answer a question nobody asked. Both bounds
// must also be instants the ledger can represent.
func NewPeriod(start, end time.Time) (Period, error) {
	if start.IsZero() || end.IsZero() {
		return Period{}, exchange.Newf(exchange.KindInvalidRequest, "period start and end are both required")
	}
	if !end.After(start) {
		return Period{}, exchange.Newf(exchange.KindInvalidRequest, "period end must be after its start")
	}
	if start.Before(earliestPeriodTime) || end.After(latestPeriodTime) {
		return Period{}, exchange.Newf(exchange.KindInvalidRequest,
			"period must lie between %s and %s", earliestPeriodTime.Format(time.RFC3339),
			latestPeriodTime.Format(time.RFC3339))
	}
	return Period{Start: start.UTC(), End: end.UTC()}, nil
}

// Grouping is the interval a totals read splits its period into.
type Grouping string

// The groupings, all in UTC.
const (
	GroupDay   Grouping = "day"
	GroupWeek  Grouping = "week"
	GroupMonth Grouping = "month"
)

// Buckets splits p into the calendar intervals of g, in UTC. A day starts at
// midnight, a week is an ISO week and starts on Monday, and a month starts on
// its first day. The first and last buckets are clipped to the period, so a
// period that starts mid-week has a short first week rather than one that
// reaches back before the period.
//
// A period that would need more than consoleapi.MaxBuckets buckets is refused
// rather than truncated: a truncated answer would read as the whole period.
func Buckets(p Period, g Grouping) ([]Period, error) {
	step, err := stepFor(g)
	if err != nil {
		return nil, err
	}
	var out []Period
	for start := p.Start; start.Before(p.End); {
		if len(out) == consoleapi.MaxBuckets {
			return nil, exchange.Newf(exchange.KindInvalidRequest,
				"the period splits into more than %d %s buckets", consoleapi.MaxBuckets, g)
		}
		end := step(start)
		if end.After(p.End) {
			end = p.End
		}
		out = append(out, Period{Start: start, End: end})
		start = end
	}
	return out, nil
}

// stepFor returns, for a grouping, the function that gives the start of the
// interval after the one t is in.
func stepFor(g Grouping) (func(time.Time) time.Time, error) {
	switch g {
	case GroupDay:
		return func(t time.Time) time.Time {
			y, m, d := t.Date()
			return time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
		}, nil
	case GroupWeek:
		return func(t time.Time) time.Time {
			y, m, d := t.Date()
			// Days since Monday: Go numbers Sunday 0, ISO numbers it 7.
			sinceMonday := (int(t.Weekday()) + 6) % 7
			return time.Date(y, m, d-sinceMonday+7, 0, 0, 0, 0, time.UTC)
		}, nil
	case GroupMonth:
		return func(t time.Time) time.Time {
			y, m, _ := t.Date()
			return time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
		}, nil
	default:
		return nil, exchange.Newf(exchange.KindInvalidRequest,
			"grouping must be one of day, week or month, not %q", g)
	}
}

// bucketIndex returns the index of the bucket that holds t, or -1 when t is
// outside every bucket. The buckets are contiguous and sorted, so it is a
// binary search.
func bucketIndex(buckets []Period, t time.Time) int {
	i, found := slices.BinarySearchFunc(buckets, t, func(b Period, t time.Time) int {
		switch {
		case t.Before(b.Start):
			return 1
		case !t.Before(b.End):
			return -1
		default:
			return 0
		}
	})
	if !found {
		return -1
	}
	return i
}

// ReportingStatus is what a publisher is told about the usage report a
// transaction owes.
type ReportingStatus string

// The reporting statuses.
const (
	ReportingNotRequired ReportingStatus = "not_required"
	ReportingPending     ReportingStatus = "pending"
	ReportingOverdue     ReportingStatus = "overdue"
	ReportingReceived    ReportingStatus = "received"
)

// ReportingStatuses lists every reporting status.
func ReportingStatuses() []ReportingStatus {
	return []ReportingStatus{ReportingNotRequired, ReportingPending, ReportingOverdue, ReportingReceived}
}

// UnknownReportingStatus is the refusal of a reporting status that is not one
// of ReportingStatuses, naming every one that is.
func UnknownReportingStatus(status string) error {
	names := make([]string, 0, len(ReportingStatuses()))
	for _, s := range ReportingStatuses() {
		names = append(names, string(s))
	}
	return exchange.Newf(exchange.KindInvalidRequest,
		"reporting_status must be one of %s, not %q", strings.Join(names, ", "), status)
}

// statusInstant is now as a reporting status is decided at: truncated to the
// microsecond, the precision Postgres keeps a deadline in and the precision the
// list query's as_of reaches it with. The label a row shows and the filter the
// list applies then compare the same two values; at nanosecond precision a row
// could be labelled overdue for the first microsecond after its deadline while
// the filter still listed it as pending.
func statusInstant(now time.Time) time.Time { return now.Truncate(time.Microsecond) }

// obligationMatch is the condition on a transaction's latest obligation under
// which ReportingStatusOf gives it status as of now. It is found by asking the
// rule about every state, before its deadline and after it, rather than written
// out a second time, so the filter a list applies and the status each of its
// rows shows cannot come to disagree.
//
// A status that is not one of ReportingStatuses is refused. It would match no
// transaction, and an empty list would read as "none of these" rather than as
// the mistake it is.
func obligationMatch(status ReportingStatus, now time.Time) (repo.ObligationMatch, error) {
	if !slices.Contains(ReportingStatuses(), status) {
		return repo.ObligationMatch{}, UnknownReportingStatus(string(status))
	}
	none, err := reportingStatus("", false)
	if err != nil {
		return repo.ObligationMatch{}, err
	}
	m := repo.ObligationMatch{None: none == status, AsOf: now}
	for _, state := range repo.ObligationStates() {
		open, err := reportingStatus(state, false)
		if err != nil {
			return repo.ObligationMatch{}, err
		}
		past, err := reportingStatus(state, true)
		if err != nil {
			return repo.ObligationMatch{}, err
		}
		if open == status {
			m.Open = append(m.Open, state)
		}
		if past == status {
			m.Past = append(m.Past, state)
		}
	}
	return m, nil
}

// ReportingStatusOf derives the status from the transaction's latest
// obligation, as of now. An empty state means the transaction minted no
// obligation. A pending obligation is overdue once its deadline has passed:
// the obligation row is not moved when the deadline passes, so the status is
// decided when it is read.
//
// The Exchange writes only PENDING and RECEIVED. ACCEPTED and CHALLENGED
// follow a report that arrived, so they read as received; EXPIRED is a
// deadline that passed without one, so it reads as overdue. A state outside
// the enum is an internal error rather than a guess, because showing it as
// received would tell a publisher an agent reported when it may not have.
func ReportingStatusOf(state repo.ObligationState, deadline *time.Time, now time.Time) (ReportingStatus, error) {
	return reportingStatus(state, deadline != nil && now.After(*deadline))
}

// reportingStatus is ReportingStatusOf once the deadline has been compared with
// now: past reports whether it has passed. The deadline enters the rule only
// through that comparison, which is what lets obligationMatch derive a list
// filter from this function by asking it about every state, past and not.
func reportingStatus(state repo.ObligationState, past bool) (ReportingStatus, error) {
	switch state {
	case "":
		return ReportingNotRequired, nil
	case repo.ObligationStatePending:
		if past {
			return ReportingOverdue, nil
		}
		return ReportingPending, nil
	case repo.ObligationStateExpired:
		return ReportingOverdue, nil
	case repo.ObligationStateReceived, repo.ObligationStateAccepted, repo.ObligationStateChallenged:
		return ReportingReceived, nil
	default:
		return "", exchange.Newf(exchange.KindInternal, "obligation state %q has no reporting status", state)
	}
}
