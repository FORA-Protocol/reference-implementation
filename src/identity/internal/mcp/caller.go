package mcp

import (
	"context"
	"log/slog"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/agentsign"
)

// caller is the authenticated agent behind one tool call, resolved once at the
// top of each handler.
type caller struct {
	// subdomain is the agent's directory subdomain — the durable FORA identity
	// the outbound signature is made with.
	subdomain string
	// requestID correlates this tool call with the HTTP request that carried it.
	requestID string
}

// callerFrom resolves who is making THIS call.
//
// The endpoint is stateless, so the context a tool handler receives is the one
// of the HTTP request being served: the bearer gate put that request's agent on
// it (withAgentIdentity in auth.go), and the request-id middleware put that
// request's correlation id on it. The SDK also hands the handler the verified
// token of the same request as TokenInfo.
//
// Both should name the same agent. Prefer the TokenInfo value, fall back to the
// context when the SDK supplies none, and FAIL CLOSED when the two disagree: a
// disagreement means the context no longer belongs to this request, and the safe
// reading of "two answers to whose key signs this" is neither.
func callerFrom(ctx context.Context, req *mcpsdk.CallToolRequest) (caller, error) {
	c := caller{
		subdomain: agentsign.SubdomainFromContext(ctx),
		requestID: reqctx.IDFromContext(ctx),
	}
	extra := extraOf(req)
	if extra == nil || extra.TokenInfo == nil || extra.TokenInfo.UserID == "" {
		if c.subdomain == "" {
			return caller{}, errNoIdentity
		}
		return c, nil
	}
	if c.subdomain != "" && c.subdomain != extra.TokenInfo.UserID {
		return caller{}, errIdentityMismatch
	}
	c.subdomain = extra.TokenInfo.UserID
	return c, nil
}

// extraOf returns the per-request extras, tolerating a nil request so a direct
// unit call does not panic.
func extraOf(req *mcpsdk.CallToolRequest) *mcpsdk.RequestExtra {
	if req == nil {
		return nil
	}
	return req.Extra
}

// logger returns the request-scoped logger the middleware put on the context,
// so every tool line carries this request's request_id like the rest of the
// service. The construction-time logger is the fallback for a context without one.
func (t *toolset) logger(ctx context.Context) *slog.Logger {
	if l := reqctx.FromContext(ctx); l != nil {
		return l
	}
	return t.log
}
