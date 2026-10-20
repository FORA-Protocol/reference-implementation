//go:build integration

package app_test

import (
	"html"
	"regexp"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// reReporting finds the sentence a transaction page says the usage report
// stands with.
var reReporting = regexp.MustCompile(`<p class="mb-0" data-reporting>([^<]*)</p>`)

// TestDashboard_TransactionPageSaysTheExchangesReportStatus makes one sale in
// each report status, the way the status filter's test does, and opens each
// one's page. The page's sentence about the usage report follows the status
// and the deadline the Exchange's detail route returns, not a rule of the
// console's own about which sales owe one.
func TestDashboard_TransactionPageSaysTheExchangesReportStatus(t *testing.T) {
	deployment, clk := testClock()
	f := startSellerOver(t, deployment)
	buyer := f.exchange.Buyer(t)
	late := sell(t, buyer, "/articles/late")
	reported := sell(t, buyer, "/articles/reported")
	if err := buyer.Report(t, reported, 20); err != nil {
		t.Fatalf("report: %v", err)
	}
	free := buyer.Buy(t, "stoa.example", "/articles/free", exchangetest.UnmeteredTerm("0.05"))
	clk.Advance(25 * time.Hour)
	owed := sell(t, f.exchange.Buyer(t), "/articles/owed")

	due := func(id string) string {
		t.Helper()
		d, err := f.reads().TransactionDetail(t.Context(), exchangeclient.TransactionDetailRequest{
			Actor: f.actor(), PublisherID: f.publisherID, TransactionID: id,
		})
		if err != nil || d.ObligationState == nil {
			t.Fatalf("detail route for %s: %+v, %v; want an obligation", id, d.ObligationState, err)
		}
		return d.ObligationState.WindowEnd.UTC().Format("2006-01-02 15:04 UTC")
	}
	want := map[string]struct{ status, sentence string }{
		late.TransactionID: {
			consoleapi.ReportingOverdue,
			"The agent's usage report was due by " + due(late.TransactionID) + " and has not been received.",
		},
		reported.TransactionID: {consoleapi.ReportingReceived, "The agent's usage report was received."},
		free.TransactionID:     {consoleapi.ReportingNotRequired, "No usage report is owed for this sale."},
		owed.TransactionID: {
			consoleapi.ReportingPending,
			"The agent owes a usage report by " + due(owed.TransactionID) + ".",
		},
	}
	for id, w := range want {
		body := f.get2(t, "/transactions/"+id)
		mustContain(t, "the "+w.status+" sale's page", body, `data-status="`+w.status+`"`)
		m := reReporting.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("the %s sale's page says nothing about its usage report", w.status)
		}
		if got := html.UnescapeString(m[1]); got != w.sentence {
			t.Errorf("the %s sale's page says %q, want %q", w.status, got, w.sentence)
		}
	}
}
