// The two checks that gate the wizard's file and edge steps. Each answers one
// question about the publisher's own live site, and each one's answer is what
// unlocks the step's Next button -- the browser decides nothing.

package web

import (
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

// checkBody is the shape both checks answer in: did it pass, and if not, what
// the publisher has to change. One shape for both, because the wizard treats
// them identically -- it enables a button and lists sentences.
type checkBody struct {
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons"`
}

// checkBodyOf renders the Exchange's reason codes as what a publisher reads.
func checkBodyOf(res exchangeclient.PreflightResult) checkBody {
	return checkBody{Passed: res.Passed, Reasons: checkReasonText(res.Reasons)}
}

// CheckManifest answers whether the domain's discovery document proves the
// publisher controls it. The Exchange reads the document; nothing is written.
func (a *WizardAPI) CheckManifest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	res, err := a.wizard.CheckManifest(r.Context(), actorOf(r.Context()), req.Domain)
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	writeAPI(w, r, checkBodyOf(res))
}

// CheckEdge answers what the publisher's own site now says to an agent, to a
// visitor, and at its discovery document's address.
func (a *WizardAPI) CheckEdge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
		Paths  string `json:"content_paths"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	report, err := a.wizard.CheckEdge(r.Context(), req.Domain, req.Paths)
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	writeAPI(w, r, edgeBodyOf(report))
}

// edgeBody is the package check on the wire: whether it lets the publisher on,
// what the site answered, and each question that did not come back clean.
type edgeBody struct {
	Passed  bool               `json:"passed"`
	State   wizard.EdgeState   `json:"state"`
	Message string             `json:"message"`
	Checks  []wizard.EdgeCheck `json:"checks"`
}

func edgeBodyOf(report wizard.EdgeReport) edgeBody {
	return edgeBody{
		Passed: report.CanAdvance(), State: report.State, Message: report.Message, Checks: report.Checks,
	}
}

// Progress answers where the setup of a saved domain stands: verified, or the
// step the wizard reopens on, with what the checks on the way there found.
func (a *WizardAPI) Progress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	at, err := a.resumer.Progress(r.Context(), actorOf(r.Context()), req.Domain)
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	writeAPI(w, r, struct {
		Verified bool      `json:"verified"`
		Step     string    `json:"step,omitempty"`
		Document checkBody `json:"document"`
		Edge     edgeBody  `json:"edge"`
	}{
		Verified: at.Verified,
		Step:     at.Step,
		Document: checkBodyOf(at.Document),
		Edge:     edgeBodyOf(at.Edge),
	})
}
