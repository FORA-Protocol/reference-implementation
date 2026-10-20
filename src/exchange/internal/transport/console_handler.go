package transport

import (
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// ConsoleHandler serves the publisher console's registration routes. It decodes,
// calls the service, and encodes -- no business logic, no repository, and no
// authorization decision of its own: the service resolves the actor's grants,
// and this layer never learns enough to shortcut that.
//
// The bodies are the shared consoleapi shapes, so the console and this handler
// read and write one definition of each. The service keeps its own types,
// because service.Terms is also the stored encoding of a listing's licensing;
// this file is where the two meet. The licensing converts by a plain type
// conversion, so a field added to one shape and not the other stops this file
// compiling rather than being dropped on the way through.
type ConsoleHandler struct {
	svc *service.RegistrationService
}

// NewConsoleHandler constructs the handler over the registration service.
func NewConsoleHandler(svc *service.RegistrationService) *ConsoleHandler {
	return &ConsoleHandler{svc: svc}
}

// RegisterRoutes mounts the registration routes.
func (h *ConsoleHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(ConsoleRegisterPublisherPath, h.registerPublisher)
	mux.HandleFunc(ConsoleRegisterDomainPath, h.registerDomain)
	mux.HandleFunc(ConsoleCheckDomainPath, h.checkDomain)
	mux.HandleFunc(ConsolePreflightDomainPath, h.preflightDomain)
	mux.HandleFunc(ConsoleSetDomainTermsPath, h.setDomainTerms)
	mux.HandleFunc(ConsoleListRegistrationsPath, h.listRegistrations)
	mux.HandleFunc(ConsoleDenominationPath, h.denomination)
}

// denomination reports what this Exchange prices registered arrangements in.
// The body names no actor, but it is still decoded, so a request carrying
// fields this Exchange does not know is refused like on every other route.
func (h *ConsoleHandler) denomination(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.DenominationRequest
	if err := decodeConsoleRequest(r, &req); err != nil {
		writeConsoleError(w, r, err)
		return
	}
	d := h.svc.Denomination()
	writeConsoleJSON(w, consoleapi.Denomination{Currency: d.Currency, Unit: d.Unit})
}

func (h *ConsoleHandler) registerPublisher(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.RegisterPublisherRequest
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	p, err := h.svc.RegisterPublisher(r.Context(), caller, req.DisplayName)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	writeConsoleJSON(w, consoleapi.Publisher{PublisherID: p.ID, DisplayName: p.DisplayName})
}

func (h *ConsoleHandler) registerDomain(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.NewSite
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	reg, err := h.svc.RegisterDomain(r.Context(), caller, service.NewSite{
		PublisherID:               req.PublisherID,
		Domain:                    req.Domain,
		CDN:                       req.CDN,
		ContentPathScope:          req.ContentPathScope,
		Terms:                     (*service.Terms)(req.Terms),
		SitemapURL:                req.SitemapURL,
		ImportReplacesManualTerms: req.ImportReplacesManualTerms,
	})
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	writeConsoleJSON(w, registrationBodyOf(reg))
}

// setDomainTerms sets the terms of a site that is not verified yet and answers
// with the site as it now stands.
func (h *ConsoleHandler) setDomainTerms(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.DomainTerms
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	reg, err := h.svc.SetDomainTerms(r.Context(), caller, service.DomainTerms{
		PublisherID:               req.PublisherID,
		Domain:                    req.Domain,
		Terms:                     service.Terms(req.Terms),
		SitemapURL:                req.SitemapURL,
		ImportReplacesManualTerms: req.ImportReplacesManualTerms,
	})
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	writeConsoleJSON(w, registrationBodyOf(reg))
}

// checkDomain reads a registered site's published document and verifies the
// site when it proves the caller controls it, answering with every reason it
// did not and the listing as it now stands. A site whose terms are not set yet
// is refused before anything is read: it cannot be verified without them.
//
// A failed check is a 200 with reasons, not an error status. The Exchange
// fetched the document the publisher published and is reporting what it says.
// An error status would tell the console to retry the call, when the repair is
// an edit to a file on another host.
func (h *ConsoleHandler) checkDomain(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.DomainRequest
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	res, err := h.svc.CheckDomain(r.Context(), caller, req.PublisherID, req.Domain)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	writeCheckResult(w, r, res.Reasons, res.Registration)
}

// preflightDomain answers the question without the listing the other two
// carry, because a preflight is asked about a domain that has none. The body is
// the check route's: the two differ in what they do with it, not in what they
// take.
func (h *ConsoleHandler) preflightDomain(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.DomainRequest
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	reasons, err := h.svc.PreflightDomain(r.Context(), caller, req.PublisherID, req.Domain)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	codes, err := wireReasons(reasons)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	writeConsoleJSON(w, consoleapi.PreflightResult{Passed: len(codes) == 0, Reasons: codes})
}

// listRegistrations is the console's resume read. Registered is false for
// someone who has signed in and registered nothing, which is a normal state and
// not an error: the console sends them to the registration form.
func (h *ConsoleHandler) listRegistrations(w http.ResponseWriter, r *http.Request) {
	var req consoleapi.ListRegistrationsRequest
	caller, err := decodeConsoleCall(r, &req, func() consoleapi.Actor { return req.Actor })
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	publisher, regs, err := h.svc.ListRegistrations(r.Context(), caller)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	sites := make([]consoleapi.Registration, 0, len(regs))
	for _, reg := range regs {
		sites = append(sites, registrationBodyOf(reg))
	}
	writeConsoleJSON(w, consoleapi.Registrations{
		Registered:  publisher.ID != "",
		PublisherID: publisher.ID,
		DisplayName: publisher.DisplayName,
		Sites:       sites,
	})
}

// writeCheckResult answers a check: passed when the site's document met every
// rule, and otherwise every reason it did not.
func writeCheckResult(
	w http.ResponseWriter, r *http.Request, reasons []service.CheckReason, reg service.Registration,
) {
	codes, err := wireReasons(reasons)
	if err != nil {
		writeConsoleError(w, r, err)
		return
	}
	writeConsoleJSON(w, consoleapi.CheckResult{
		Passed:       len(codes) == 0,
		Reasons:      codes,
		Registration: registrationBodyOf(reg),
	})
}

func registrationBodyOf(reg service.Registration) consoleapi.Registration {
	scope := reg.ContentPathScope
	if scope == nil {
		scope = []string{}
	}
	return consoleapi.Registration{
		Domain:                    reg.Domain,
		CDN:                       reg.CDN,
		SigningScheme:             reg.SigningScheme,
		State:                     reg.State,
		ContentPathScope:          scope,
		Terms:                     (*consoleapi.Terms)(reg.Terms),
		SitemapURL:                reg.SitemapURL,
		ImportReplacesManualTerms: reg.ImportReplacesManualTerms,
		VerifiedAt:                reg.VerifiedAt,
		LastCheckedAt:             reg.LastCheckedAt,
	}
}
