package dashboard

import (
	"strings"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// Every absence reason the Exchange can answer with has its own sentence, so a
// new reason added to the contract fails here until the page can explain it.
func TestAbsenceText_AnswersEveryReason(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, reason := range consoleapi.AllAbsenceReasons() {
		text := absenceText(reason, "USD")
		if text == "" || strings.Contains(text, reason) {
			t.Errorf("reason %s has no sentence of its own: %q", reason, text)
		}
		if other, dup := seen[text]; dup {
			t.Errorf("reasons %s and %s read the same", reason, other)
		}
		seen[text] = reason
	}
	if absenceText("", "USD") != "" {
		t.Error("an answer with amounts has a notice")
	}
}

func TestStatusOf_LabelsEveryReportingStatus(t *testing.T) {
	t.Parallel()
	for _, value := range consoleapi.AllReportingStatuses() {
		s := statusOf(value)
		if s.Value != value || s.Label == value || s.Tone == "" {
			t.Errorf("status %s = %+v, want its own label and a tone", value, s)
		}
	}
	if s := statusOf("disputed"); s.Label != "disputed" {
		t.Errorf("an unknown status = %+v, want it shown as the Exchange wrote it", s)
	}
}

func TestDurationText(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		24 * time.Hour: "1 day", 72 * time.Hour: "3 days", 36 * time.Hour: "36 hours",
		time.Hour: "1 hour", 90 * time.Minute: "90 minutes",
	} {
		if got := durationText(d); got != want {
			t.Errorf("durationText(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestTermsText_SaysWhenTermsDoNotRead(t *testing.T) {
	t.Parallel()
	free := `[{"semantics":"TERM_SEMANTICS_ENUMERATED","pricing":{"model":"PRICING_MODEL_FREE","currency":"EUR"}}]`
	if got := termsText([]byte(free)); len(got) != 1 || got[0] != "free" {
		t.Fatalf("free term = %q", got)
	}
	if got := termsText([]byte(`{"not":"a list"}`)); len(got) != 1 || !strings.Contains(got[0], "could not be read") {
		t.Fatalf("unreadable list = %q, want it said", got)
	}
}

func TestNewChart_OneColumnPerDayScaledToTheBusiest(t *testing.T) {
	t.Parallel()
	start := day("2026-09-01")
	buckets := make([]exchangeclient.RevenueBucket, 30)
	for i := range buckets {
		buckets[i].Start = start.AddDate(0, 0, i)
		buckets[i].End = start.AddDate(0, 0, i+1)
	}
	buckets[3].TransactionCount = 7
	buckets[4].TransactionCount = 3
	c := newChart(buckets)
	if len(c.Columns) != 30 {
		t.Fatalf("columns = %d, want 30", len(c.Columns))
	}
	if c.Columns[0].Path != "" || c.Columns[3].Path == "" || c.Columns[4].Path == "" {
		t.Fatal("only days with transactions are drawn")
	}
	if got := c.Ticks[len(c.Ticks)-1].Value; got != "10" {
		t.Fatalf("top tick = %s, want the round 10 above the busiest day's 7", got)
	}
	if c.Columns[0].Label != "1 Sep" || c.Columns[1].Label != "" || c.Columns[7].Label != "8 Sep" {
		t.Fatalf("labels = %q %q %q, want every seventh day", c.Columns[0].Label, c.Columns[1].Label, c.Columns[7].Label)
	}
	if c.Columns[3].Day != "4 Sep 2026" || c.Columns[3].Count != 7 {
		t.Fatalf("column 3 = %+v", c.Columns[3])
	}
}

func TestNiceCeiling(t *testing.T) {
	t.Parallel()
	for n, want := range map[int64]int64{0: 1, 1: 1, 2: 2, 3: 5, 7: 10, 11: 20, 51: 100, 1000: 1000} {
		if got := niceCeiling(n); got != want {
			t.Errorf("niceCeiling(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestNewChart_LabelsALongPeriodByTheMonth(t *testing.T) {
	t.Parallel()
	start := day("2025-10-01")
	buckets := make([]exchangeclient.RevenueBucket, 365)
	for i := range buckets {
		buckets[i].Start = start.AddDate(0, 0, i)
		buckets[i].TransactionCount = int64(i % 3)
	}
	c := newChart(buckets)
	var labels, years []string
	for _, col := range c.Columns {
		if col.Label != "" {
			labels = append(labels, col.Label)
		}
		if col.SubLabel != "" {
			years = append(years, col.SubLabel)
		}
	}
	if len(labels) != 12 || labels[0] != "Oct" || labels[3] != "Jan" {
		t.Fatalf("labels = %v, want one per month from Oct", labels)
	}
	if strings.Join(years, ",") != "2025,2026" {
		t.Fatalf("years = %v, want the first label's and January's", years)
	}
	for _, col := range c.Columns {
		if col.Path != "" && strings.Contains(col.Path, "-") {
			t.Fatalf("a column on a long period has a negative coordinate: %s", col.Path)
		}
	}
}
