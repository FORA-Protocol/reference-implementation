package web

import (
	"errors"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/auth"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
)

// How every page and every wizard route answers a call that did not succeed.
// There are two kinds of failure: a refusal the console made on its own
// account, and a failure the Exchange reported. Each is mapped here once.

// backToDomains is the way out of every failure page: the domains list, which
// every signed-in person has.
const backToDomains = "Back to your domains"

// renderFailure answers a page whose call did not succeed.
//
// A refusal is shown in its own words, which are written for the publisher. A
// refusal of what the page's address asked for leads to again, where the person
// can start over; when again is empty, and for every other failure, the page
// leads back to the domains list.
func renderFailure(render *Renderer, w http.ResponseWriter, r *http.Request, err error, again string) {
	if re, ok := errors.AsType[*refusal.Error](err); ok {
		logRefusal(r, re)
		link, label := DomainsPath, backToDomains
		if re.Kind == refusal.KindInvalid && again != "" {
			link, label = again, "Start again"
		}
		render.RenderMessage(w, r, refusalStatus(re.Kind), refusalTitle(re.Kind), re.Message, link, label)
		return
	}
	title, message := failureText(err)
	reqctx.FromContext(r.Context()).ErrorContext(r.Context(), "console.exchange_call_failed",
		"path", r.URL.Path, "err", err)
	render.RenderMessage(w, r, statusFor(err), title, message, DomainsPath, backToDomains)
}

// logRefusal records a not-found with who asked, so a run of them from one
// person trying ids can be told apart from people following old links. The
// other refusals are the person's own mistakes, except a failed build, which
// the wizard logs where it fails.
func logRefusal(r *http.Request, re *refusal.Error) {
	if re.Kind != refusal.KindNotFound {
		return
	}
	var issuer, subject string
	if s := auth.FromContext(r.Context()); s != nil {
		issuer, subject = s.Issuer, s.Subject
	}
	reqctx.FromContext(r.Context()).InfoContext(r.Context(), "console.read_not_found",
		"path", r.URL.Path, "oidc_issuer", issuer, "oidc_subject", subject, "err", re)
}

// refusalStatus is the status a refusal answers with, on a page and in the
// wizard's JSON alike.
func refusalStatus(k refusal.Kind) int {
	switch k {
	case refusal.KindInvalid:
		return http.StatusBadRequest
	case refusal.KindNotRegistered:
		return http.StatusConflict
	case refusal.KindNotFound:
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

// refusalTitle is the heading of a refused page.
func refusalTitle(k refusal.Kind) string {
	switch k {
	case refusal.KindNotFound:
		return "Not found"
	case refusal.KindBuildFailed:
		return "Your files could not be produced"
	default:
		return "That did not work"
	}
}

// failureText turns a failure the Exchange reported into what a publisher
// reads.
//
// A refusal the Exchange wrote for a publisher is shown as it wrote it: it
// names the site, the field or the integration, and a second wording of the
// same thing here would be one more sentence to keep in step. Anything else is
// ours, and says so without repeating an internal message.
func failureText(err error) (title, message string) {
	if xe, ok := errors.AsType[*exchangeclient.Error](err); ok {
		switch {
		case xe.Unreachable():
			return "We could not reach the exchange",
				"Nothing was changed. Please try again in a moment."
		case xe.CallerFault():
			return "That did not work", xe.Message
		}
	}
	return "Something went wrong",
		"Something went wrong on our side. Please try again in a moment."
}

// statusFor maps a failure the Exchange reported onto the answer's status, so
// a monitor sees the same classes a person does.
func statusFor(err error) int {
	if xe, ok := errors.AsType[*exchangeclient.Error](err); ok {
		switch {
		case xe.Unreachable():
			return http.StatusBadGateway
		case xe.CallerFault():
			return http.StatusBadRequest
		}
	}
	return http.StatusInternalServerError
}
