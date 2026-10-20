package web

import (
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/auth"
)

// VerifyEmail is where someone whose address the login provider has not checked
// lands.
//
// They can sign in, so they have an account; what they cannot do is register
// anything, because the Exchange refuses an unverified address on a grant. The
// page says so and sends them back to their provider rather than leaving them
// on a form that would fail.
func (h *Handler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	h.render.RenderMessage(w, r, http.StatusForbidden,
		"Please confirm your email address",
		"Your login provider has not confirmed your email address yet. Open the message it "+
			"sent you, follow the link, then sign in here again.",
		auth.LoginPath, "Sign in again")
}
