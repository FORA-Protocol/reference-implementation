package transport

import (
	"encoding/json"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// ConsoleReadHandler serves the publisher reads on the console surface. Like
// ConsoleHandler it decodes, calls the service and encodes: the grant check,
// the scoping and every derived value are the service's.
//
// It is a separate handler over a separate service because the reads share
// nothing with registration but the grant gate, which both services hold.
type ConsoleReadHandler struct {
	svc *service.PublisherReadService
}

// NewConsoleReadHandler constructs the handler over the read service.
func NewConsoleReadHandler(svc *service.PublisherReadService) *ConsoleReadHandler {
	return &ConsoleReadHandler{svc: svc}
}

// RegisterRoutes mounts the read routes.
func (h *ConsoleReadHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(ConsoleRevenueTotalsPath, h.revenueTotals)
	mux.HandleFunc(ConsoleListTransactionsPath, h.listTransactions)
	mux.HandleFunc(ConsoleTransactionDetailPath, h.transactionDetail)
	mux.HandleFunc(ConsoleListCatalogPath, h.listCatalog)
	mux.HandleFunc(ConsoleCatalogDetailPath, h.catalogDetail)
	mux.HandleFunc(ConsolePublisherTermsPath, h.publisherTerms)
}

func (h *ConsoleReadHandler) revenueTotals(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.RevenueTotalsRequest
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	report, err := h.svc.Totals(r.Context(), caller, service.RevenueQuery{
		PublisherID: req.PublisherID,
		Start:       req.Period.Start,
		End:         req.Period.End,
		Grouping:    service.Grouping(req.Grouping),
		Currency:    req.Currency,
		Narrowing:   service.Narrowing{ResourceID: req.ResourceID, Domain: req.Domain},
		CountOnly:   req.CountOnly,
	})
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	body, err := revenueTotalsBody(report)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	writeConsoleJSON(w, body)
}

func revenueTotalsBody(report service.RevenueReport) (consoleapi.RevenueTotals, error) {
	grouping, err := wireGrouping(report.Grouping)
	if err != nil {
		return consoleapi.RevenueTotals{}, err
	}
	absence, err := wireAbsence(report.Absence)
	if err != nil {
		return consoleapi.RevenueTotals{}, err
	}
	total, err := wireFigures(report.Total)
	if err != nil {
		return consoleapi.RevenueTotals{}, err
	}
	denials, err := wireDenials(report.Denials)
	if err != nil {
		return consoleapi.RevenueTotals{}, err
	}
	buckets := make([]consoleapi.RevenueBucket, 0, len(report.Buckets))
	for _, b := range report.Buckets {
		figures, err := wireFigures(b.RevenueFigures)
		if err != nil {
			return consoleapi.RevenueTotals{}, err
		}
		buckets = append(buckets, consoleapi.RevenueBucket{Start: b.Start, End: b.End, RevenueFigures: figures})
	}
	return consoleapi.RevenueTotals{
		Period:        consoleapi.Period{Start: report.Period.Start, End: report.Period.End},
		Grouping:      grouping,
		Currency:      report.Currency,
		Buckets:       buckets,
		Total:         total,
		Denials:       denials,
		AbsenceReason: absence,
	}, nil
}

func (h *ConsoleReadHandler) listTransactions(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.ListTransactionsRequest
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	status, err := reportingStatusFromWire(req.ReportingStatus)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	rows, links, err := h.svc.Transactions(r.Context(), caller, service.TransactionListQuery{
		PublisherID: req.PublisherID,
		Start:       req.Period.Start,
		End:         req.Period.End,
		Narrowing:   service.Narrowing{ResourceID: req.ResourceID, Domain: req.Domain},
		Status:      status,
		PageSize:    req.PageSize,
		Cursor:      req.Cursor,
	})
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	out := make([]consoleapi.TransactionRow, 0, len(rows))
	for _, t := range rows {
		row, err := transactionRowBody(t)
		if err != nil {
			writeConsoleError(w, r, err)
			return
		}
		out = append(out, row)
	}
	writeConsoleJSON(w, consoleapi.TransactionPage{Transactions: out, NextCursor: links.Next, PrevCursor: links.Prev})
}

func transactionRowBody(t service.PublisherTransactionRow) (consoleapi.TransactionRow, error) {
	status, err := wireReportingStatus(t.Status)
	if err != nil {
		return consoleapi.TransactionRow{}, err
	}
	cost, err := wireUnitCost(t.UnitCost)
	if err != nil {
		return consoleapi.TransactionRow{}, err
	}
	return consoleapi.TransactionRow{
		TransactionID:   t.TransactionID,
		CreatedAt:       t.CreatedAt,
		ResourceID:      t.ResourceID,
		ResourceURI:     t.ResourceURI,
		ResourceTitle:   t.ResourceTitle,
		AgentID:         t.AgentID,
		UnitCost:        cost,
		Currency:        t.Currency,
		ReportingStatus: status,
	}, nil
}

// transactionDetail serves the same evidence shape the operator's evidence
// read does, through the same projection, so the signed URL is left out here
// for the reason it is left out there.
func (h *ConsoleReadHandler) transactionDetail(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.TransactionDetailRequest
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	d, err := h.svc.TransactionDetail(r.Context(), caller, req.PublisherID, req.TransactionID)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	status, err := wireReportingStatus(d.Status)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	writeConsoleJSON(w, consoleapi.TransactionDetail{
		Response:        evidenceResponse(d.View),
		ReportingStatus: status,
	})
}

func (h *ConsoleReadHandler) listCatalog(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.ListCatalogRequest
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	rows, links, err := h.svc.Catalog(r.Context(), caller, service.CatalogListQuery{
		PublisherID: req.PublisherID,
		Narrowing:   service.Narrowing{Domain: req.Domain},
		PageSize:    req.PageSize,
		Cursor:      req.Cursor,
	})
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	out := make([]consoleapi.CatalogResource, 0, len(rows))
	for _, res := range rows {
		out = append(out, catalogResourceBody(res))
	}
	writeConsoleJSON(w, consoleapi.CatalogPage{Resources: out, NextCursor: links.Next, PrevCursor: links.Prev})
}

func (h *ConsoleReadHandler) catalogDetail(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.CatalogDetailRequest
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	res, err := h.svc.Resource(r.Context(), caller, req.PublisherID, req.ResourceID)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	writeConsoleJSON(w, catalogResourceBody(res))
}

func catalogResourceBody(res repo.PublisherResource) consoleapi.CatalogResource {
	var title string
	if res.Title != nil {
		title = *res.Title
	}
	return consoleapi.CatalogResource{
		ResourceID: res.ResourceID,
		URI:        res.URI,
		Title:      title,
		Terms:      json.RawMessage(res.Terms),
		CreatedAt:  res.CreatedAt,
		UpdatedAt:  res.UpdatedAt,
	}
}

func (h *ConsoleReadHandler) publisherTerms(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.PublisherTermsRequest
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	t, err := h.svc.Terms(r.Context(), caller, req.PublisherID)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	source := consoleapi.FeeRateSourceTenant
	if t.FeeRate.Overridden {
		source = consoleapi.FeeRateSourcePublisher
	}
	writeConsoleJSON(w, consoleapi.PublisherTerms{
		FeeRateBps:    t.FeeRate.Bps,
		FeeRateSource: source,
		ReportingPolicy: consoleapi.ReportingPolicy{
			RequiredFields:    t.Reporting.RequiredFields,
			QuantityTolerance: t.Reporting.QuantityTolerance,
			WindowSeconds:     t.Reporting.WindowSeconds,
		},
	})
}
