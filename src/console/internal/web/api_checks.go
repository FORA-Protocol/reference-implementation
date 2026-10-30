// The wizard's last step, Connect your site: one check of the publisher's own
// site that answers for both of the step's tasks, and verifies the domain when
// both are done. The browser decides nothing: it draws what the check answers
// and asks again.

package web

import (
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

// fileTaskBody is the file task on the wire.
type fileTaskBody struct {
	Passed  bool             `json:"passed"`
	State   wizard.FileState `json:"state"`
	Status  string           `json:"status"`
	Reasons []string         `json:"reasons"`
}

// packageTaskBody is the package task on the wire.
type packageTaskBody struct {
	Passed bool               `json:"passed"`
	State  wizard.EdgeState   `json:"state"`
	Status string             `json:"status"`
	Checks []wizard.EdgeCheck `json:"checks"`
}

// Check is the last step's question, asked when the step opens, again every
// few seconds while it is shown, and when the publisher presses Check now:
// where the domain's file and its package stand, and whether the domain is
// verified now.
func (a *WizardAPI) Check(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if !readAPI(w, r, &req) {
		return
	}
	at, err := a.resumer.Check(r.Context(), actorOf(r.Context()), req.Domain)
	if err != nil {
		apiFailure(w, r, err)
		return
	}
	file, pkg := at.Connection.File, at.Connection.Package
	writeAPI(w, r, struct {
		Domain   string          `json:"domain"`
		Verified bool            `json:"verified"`
		File     fileTaskBody    `json:"file"`
		Package  packageTaskBody `json:"package"`
	}{
		Domain:   at.Domain,
		Verified: at.Verified,
		File: fileTaskBody{
			Passed: file.Passed, State: file.State, Status: file.Status, Reasons: nonNil(file.Reasons),
		},
		Package: packageTaskBody{
			Passed: pkg.Passed, State: pkg.State, Status: pkg.Status, Checks: nonNil(pkg.Checks),
		},
	})
}

// nonNil is list, or an empty list for none, so the browser always reads an
// array.
func nonNil[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}
