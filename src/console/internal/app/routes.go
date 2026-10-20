package app

import (
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/auth"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
)

// handlers are the console's transports over its services: its own pages, the
// dashboard's pages, and the wizard's JSON routes.
type handlers struct {
	pages *web.Handler
	dash  *web.DashboardHandler
	api   *web.WizardAPI
}

// route is one gated route: its ServeMux pattern and the handler it serves.
type route struct {
	pattern string
	serve   func(handlers) http.HandlerFunc
}

// publisherPages is every page that reads or writes a registration. Each needs
// a session whose address the login provider verified. The access tests walk
// this same list, so a page added here is covered by them without a second
// edit.
var publisherPages = []route{
	{"GET " + web.HomePath + "{$}", func(h handlers) http.HandlerFunc { return h.pages.Home }},
	{"GET " + web.DomainsPath, func(h handlers) http.HandlerFunc { return h.pages.Domains }},
	{"GET " + web.NewDomainPath, func(h handlers) http.HandlerFunc { return h.pages.NewDomain }},
	{"GET " + web.DomainPrefix + "{domain}", func(h handlers) http.HandlerFunc { return h.pages.Domain }},
	{"GET " + web.DomainFilePattern, func(h handlers) http.HandlerFunc { return h.pages.Domain }},
	{"GET " + web.DomainSetupPattern, func(h handlers) http.HandlerFunc { return h.pages.FinishSetup }},
	{"GET " + dashboard.PathOverview, func(h handlers) http.HandlerFunc { return h.dash.Overview }},
	{"GET " + dashboard.PathTransactions, func(h handlers) http.HandlerFunc { return h.dash.Transactions }},
	{"GET " + dashboard.PathTransaction, func(h handlers) http.HandlerFunc { return h.dash.Transaction }},
	{"GET " + dashboard.PathCatalog, func(h handlers) http.HandlerFunc { return h.dash.Catalog }},
	{"GET " + dashboard.PathResource, func(h handlers) http.HandlerFunc { return h.dash.Resource }},
	{"GET " + dashboard.PathIncome, func(h handlers) http.HandlerFunc { return h.dash.Income }},
	{"GET " + dashboard.PathIncomeCSV, func(h handlers) http.HandlerFunc { return h.dash.IncomeCSV }},
}

// wizardRoutes is the wizard's JSON routes. They carry the same two gates as
// the pages; only what they answer a refusal with differs.
var wizardRoutes = []route{
	{"POST " + web.APIPublisherPath, func(h handlers) http.HandlerFunc { return h.api.Publisher }},
	{"POST " + web.APILookupPath, func(h handlers) http.HandlerFunc { return h.api.Lookup }},
	{"POST " + web.APIFilesPath, func(h handlers) http.HandlerFunc { return h.api.Files }},
	{"GET " + web.APIFilePath, func(h handlers) http.HandlerFunc { return h.api.File }},
	{"POST " + web.APICheckManifestPath, func(h handlers) http.HandlerFunc { return h.api.CheckManifest }},
	{"POST " + web.APICheckEdgePath, func(h handlers) http.HandlerFunc { return h.api.CheckEdge }},
	{"POST " + web.APIPagePath, func(h handlers) http.HandlerFunc { return h.api.Page }},
	{"POST " + web.APITermsPreviewPath, func(h handlers) http.HandlerFunc { return h.api.TermsPreview }},
	{"POST " + web.APIRegisterPath, func(h handlers) http.HandlerFunc { return h.api.Register }},
	{"POST " + web.APIProgressPath, func(h handlers) http.HandlerFunc { return h.api.Progress }},
}

// routes is the console's routing table, and the gating is its shape.
//
// It is worth reading as one piece: the sign-in routes, the styles and the key
// directory are open, everything else needs a session, and everything that
// reads or writes a registration needs a session whose address the provider
// verified. Nothing is gated twice and nothing is left ungated by being added
// to the wrong group -- a route is in exactly one of them.
func routes(signin *auth.Handler, h handlers, directory http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	// Unsigned and unauthenticated, deliberately: it carries a public key, and
	// the party that most needs to read it -- the Exchange -- has no session
	// here and never will.
	mux.Handle("GET "+DirectoryPath, directory)
	mux.Handle("GET "+web.StaticPrefix, web.StaticHandler())
	signin.RegisterRoutes(mux)

	// Signed in, address unconfirmed: the person can read why and sign out,
	// and nothing else.
	mux.Handle("GET "+web.VerifyEmailPath, signin.RequireSession(http.HandlerFunc(h.pages.VerifyEmail)))

	for _, rt := range publisherPages {
		mux.Handle(rt.pattern, signin.RequireSession(signin.RequireVerifiedEmail(web.VerifyEmailPath, rt.serve(h))))
	}
	// These answer a script rather than a person, so a visitor without a
	// session gets a refusal it can read instead of a redirect to the login
	// provider, whose sign-in page a fetch call would swallow as its answer.
	for _, rt := range wizardRoutes {
		mux.Handle(rt.pattern, signin.RequireSessionOr(
			http.HandlerFunc(web.APIUnauthorized),
			signin.RequireVerifiedEmailOr(http.HandlerFunc(web.APIEmailUnverified), rt.serve(h))))
	}
	return mux
}
