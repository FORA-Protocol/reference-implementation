package web

import (
	"errors"
	"mime"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
)

// DashboardHandler serves the dashboard's pages. A signed-in person holds one
// publisher on the Exchange, so each page first asks the dashboard service
// which one, then reads that publisher's data as the person. The console never
// decides whose data a person may see: a transaction of somebody else's comes
// back not found, and the page says so and shows nothing.
type DashboardHandler struct {
	render *Renderer
	dash   *dashboard.Service
}

// NewDashboard builds the dashboard's page handler.
func NewDashboard(render *Renderer, dash *dashboard.Service) (*DashboardHandler, error) {
	switch {
	case render == nil:
		return nil, errors.New("web: a renderer is required")
	case dash == nil:
		return nil, errors.New("web: the dashboard is required")
	}
	return &DashboardHandler{render: render, dash: dash}, nil
}

// Overview shows one period's totals and transactions per day.
func (h *DashboardHandler) Overview(w http.ResponseWriter, r *http.Request) {
	nav, ok := h.navFor(w, r, dashboard.SectionOverview)
	if !ok {
		return
	}
	q := r.URL.Query()
	period, err := h.dash.Period(q.Get("from"), q.Get("to"))
	if err != nil {
		h.readFailure(w, r, nav, err)
		return
	}
	view, err := h.dash.Overview(r.Context(), actorOf(r.Context()), nav, period)
	if err != nil {
		h.readFailure(w, r, nav, err)
		return
	}
	h.render.Render(w, r, "overview", Page{Title: "Overview", Nav: &nav, Overview: &view})
}

// Transactions shows one page of the publisher's transactions.
func (h *DashboardHandler) Transactions(w http.ResponseWriter, r *http.Request) {
	nav, ok := h.navFor(w, r, dashboard.SectionTransactions)
	if !ok {
		return
	}
	q := r.URL.Query()
	period, err := h.dash.Period(q.Get("from"), q.Get("to"))
	if err != nil {
		h.readFailure(w, r, nav, err)
		return
	}
	view, err := h.dash.Transactions(r.Context(), actorOf(r.Context()), nav, dashboard.TransactionQuery{
		Period:     period,
		Domain:     q.Get("domain"),
		Status:     q.Get("status"),
		ResourceID: q.Get("resource"),
		Position:   dashboard.ParsePosition(q),
	})
	if err != nil {
		h.readFailure(w, r, nav, err)
		return
	}
	h.render.Render(w, r, "transactions", Page{Title: "Transactions", Nav: &nav, Transactions: &view})
}

// Transaction shows one transaction and the evidence the Verify button checks.
func (h *DashboardHandler) Transaction(w http.ResponseWriter, r *http.Request) {
	nav, ok := h.navFor(w, r, dashboard.SectionTransactions)
	if !ok {
		return
	}
	view, err := h.dash.Transaction(r.Context(), actorOf(r.Context()), nav, r.PathValue("transaction"))
	if err != nil {
		h.readFailure(w, r, nav, err)
		return
	}
	h.render.Render(w, r, "transaction", Page{Title: "Transaction", Nav: &nav, Transaction: &view})
}

// Resource shows one catalog entry: what it is, the terms it is offered
// under, and a way to its sales.
func (h *DashboardHandler) Resource(w http.ResponseWriter, r *http.Request) {
	nav, ok := h.navFor(w, r, dashboard.SectionCatalog)
	if !ok {
		return
	}
	view, err := h.dash.Resource(r.Context(), actorOf(r.Context()), nav, r.PathValue("resource"))
	if err != nil {
		h.readFailure(w, r, nav, err)
		return
	}
	h.render.Render(w, r, "resource", Page{Title: "Resource", Nav: &nav, Resource: &view})
}

// Income shows the publisher's income in a period, a row per day.
func (h *DashboardHandler) Income(w http.ResponseWriter, r *http.Request) {
	report, nav, ok := h.income(w, r)
	if !ok {
		return
	}
	h.render.Render(w, r, "income", Page{Title: "Income", Nav: &nav, Income: &report})
}

// IncomeCSV sends the income report as a CSV file. A report whose amounts
// could not be read is refused with the page's own explanation rather than
// sent as a file of zeros, which a spreadsheet would add up as real.
func (h *DashboardHandler) IncomeCSV(w http.ResponseWriter, r *http.Request) {
	report, nav, ok := h.income(w, r)
	if !ok {
		return
	}
	if !report.HasAmounts {
		h.render.RenderMessage(w, r, http.StatusServiceUnavailable, "Nothing to download",
			report.Notice, nav.Income(), "Back to income")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": report.FileName()}))
	if err := report.WriteCSV(w); err != nil {
		reqctx.FromContext(r.Context()).ErrorContext(r.Context(), "console.income_csv_failed", "err", err)
	}
}

// income reads the report the address asks for. ok is false when the page has
// been answered already.
func (h *DashboardHandler) income(
	w http.ResponseWriter, r *http.Request,
) (dashboard.IncomeReport, dashboard.Nav, bool) {
	nav, ok := h.navFor(w, r, dashboard.SectionIncome)
	if !ok {
		return dashboard.IncomeReport{}, nav, false
	}
	q := r.URL.Query()
	period, err := h.dash.Period(q.Get("from"), q.Get("to"))
	if err != nil {
		h.readFailure(w, r, nav, err)
		return dashboard.IncomeReport{}, nav, false
	}
	report, err := h.dash.Income(r.Context(), actorOf(r.Context()), nav, dashboard.IncomeQuery{
		Period: period, ResourceID: q.Get("resource"), Domain: q.Get("domain"),
		AllDays: dashboard.ParseAllDays(q["days"]),
	})
	if err != nil {
		h.readFailure(w, r, nav, err)
		return dashboard.IncomeReport{}, nav, false
	}
	return report, nav, true
}

// Catalog shows one page of the publisher's resources and their terms.
func (h *DashboardHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	nav, ok := h.navFor(w, r, dashboard.SectionCatalog)
	if !ok {
		return
	}
	q := r.URL.Query()
	view, err := h.dash.Catalog(r.Context(), actorOf(r.Context()), nav, dashboard.CatalogQuery{
		Domain: q.Get("domain"), Position: dashboard.ParsePosition(q),
	})
	if err != nil {
		h.readFailure(w, r, nav, err)
		return
	}
	h.render.Render(w, r, "catalog", Page{Title: "Catalog", Nav: &nav, Catalog: &view})
}

// navFor is a dashboard page's place: the one publisher the signed-in person
// holds, as the Exchange reports it, and the page's section. ok is false when
// the page has been answered already: a person with no domain yet is sent to
// add their first one, and a failed call gets the failure page.
func (h *DashboardHandler) navFor(w http.ResponseWriter, r *http.Request, section string) (dashboard.Nav, bool) {
	nav, err := h.dash.Nav(r.Context(), actorOf(r.Context()), section)
	switch {
	case errors.Is(err, dashboard.ErrNoDomain):
		http.Redirect(w, r, HomePath, http.StatusSeeOther)
		return dashboard.Nav{}, false
	case err != nil:
		renderFailure(h.render, w, r, err, "")
		return dashboard.Nav{}, false
	}
	return nav, true
}

// readFailure renders the page for a dashboard read that did not succeed. A
// refused address starts again from the section's own page. Not found is
// answered with the same page whether the transaction does not exist or
// belongs to somebody else.
func (h *DashboardHandler) readFailure(w http.ResponseWriter, r *http.Request, nav dashboard.Nav, err error) {
	renderFailure(h.render, w, r, err, sectionHome(nav))
}

// sectionHome is the section's own page with nothing in its address, the
// place to start again from after a refused one.
func sectionHome(nav dashboard.Nav) string {
	switch nav.Section {
	case dashboard.SectionTransactions:
		return nav.Transactions()
	case dashboard.SectionCatalog:
		return nav.Catalog()
	case dashboard.SectionIncome:
		return nav.Income()
	default:
		return nav.Overview()
	}
}
