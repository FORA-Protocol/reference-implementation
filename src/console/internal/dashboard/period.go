package dashboard

import (
	"encoding/json"
	"net/url"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
)

// dateLayout is how a date travels in a page's address and in a date field.
const dateLayout = "2006-01-02"

// maxPeriodDays is the longest period a page reads. Every page reads its
// totals by day, so it is the most buckets the Exchange splits a totals read
// into.
const maxPeriodDays = consoleapi.MaxBuckets

// The refusals of a period the picker can see coming before it submits one.
// ParsePeriod refuses with them, and PickerRules hands them to the picker.
const (
	msgReversed = "The last day comes before the first day."
	msgTooLong  = "Choose a period of at most a year."
)

// pickerRules is PickerRules' JSON: the longest period and the two refusals.
// Marshalling a struct of an int and two strings cannot fail.
var pickerRules, _ = json.Marshal(struct {
	MaxDays  int    `json:"max_days"`
	Reversed string `json:"reversed"`
	TooLong  string `json:"too_long"`
}{maxPeriodDays, msgReversed, msgTooLong})

// Period is the stretch of whole days a page reads, in UTC. From is the first
// day and To the last, both inclusive, both at midnight UTC.
type Period struct {
	From time.Time
	To   time.Time
}

// MonthOf is the calendar month, in UTC, that t falls in, up to the day now
// falls in when that is inside the month. No page reads days that have not
// happened, as the period picker offers none: the current month is the month
// so far, the picker's "This month", and an earlier month is the whole of it.
func MonthOf(t, now time.Time) Period {
	t = t.UTC()
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1)
	if today := dayOf(now); today.Before(last) && !today.Before(first) {
		last = today
	}
	return Period{From: first, To: last}
}

// dayOf is the midnight, in UTC, that starts the day t falls in.
func dayOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// ParsePeriod reads the from and to dates a page's address carries. With
// neither, the page reads the month so far. A period is refused when a date is
// missing or not a date, when it ends before it starts, and when it is longer
// than a year of days.
func ParsePeriod(from, to string, now time.Time) (Period, error) {
	if from == "" && to == "" {
		return MonthOf(now, now), nil
	}
	if from == "" || to == "" {
		return Period{}, invalid("Choose both a first and a last day.")
	}
	start, err := time.Parse(dateLayout, from)
	if err != nil {
		return Period{}, invalid("The first day is not a date. Use the form 2026-09-01.")
	}
	end, err := time.Parse(dateLayout, to)
	if err != nil {
		return Period{}, invalid("The last day is not a date. Use the form 2026-09-30.")
	}
	p := Period{From: start, To: end}
	switch {
	case end.Before(start):
		return Period{}, invalid(msgReversed)
	case p.Days() > maxPeriodDays:
		return Period{}, invalid(msgTooLong)
	}
	return p, nil
}

// Days is how many days the period covers.
func (p Period) Days() int {
	return int(p.To.Sub(p.From).Hours()/24) + 1
}

// Window is the period as the Exchange reads it: half-open, so the end is the
// midnight after the last day.
func (p Period) Window() exchangeclient.Period {
	return exchangeclient.Period{Start: p.From, End: p.To.AddDate(0, 0, 1)}
}

// FromValue and ToValue are the period's days as a date field holds them.
func (p Period) FromValue() string { return p.From.Format(dateLayout) }

// ToValue is the last day as a date field holds it.
func (p Period) ToValue() string { return p.To.Format(dateLayout) }

// PickerRules is what the period picker checks a range against before it
// submits one, as JSON: the longest period and the sentences ParsePeriod
// refuses a reversed or too long one with. The picker is given them rather
// than keeping its own copy, so it cannot offer a range the server refuses or
// say it differently.
func (Period) PickerRules() string { return string(pickerRules) }

// Label says the period in a line, marked UTC.
func (p Period) Label() string {
	return p.From.Format("2 Jan 2006") + " – " + p.To.Format("2 Jan 2006") + " (UTC)"
}

// query adds the period to an address's query.
func (p Period) query(q url.Values) {
	q.Set("from", p.FromValue())
	q.Set("to", p.ToValue())
}

// invalid refuses something in a page's address.
func invalid(message string) *refusal.Error {
	return refusal.New(refusal.KindInvalid, message)
}

// Period reads the period a page's address asks for, defaulting to the month
// so far by the service's clock.
func (s *Service) Period(from, to string) (Period, error) {
	return ParsePeriod(from, to, s.clk.Now())
}
