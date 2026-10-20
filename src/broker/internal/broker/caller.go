package broker

import "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentid"

// CallerHost is the canonical host of a verified Signature-Agent directory: the
// agent identity every Broker surface derives the caller from. A directory that
// names no host means the caller has not established who it is, so it is
// Unauthenticated. Resolve and the purchase service both call it, so they derive
// the caller the same way.
func CallerHost(signatureAgent string) (string, *Error) {
	caller, err := agentid.FromDirectory(signatureAgent)
	if err != nil {
		return "", Wrapf(KindUnauthenticated, err,
			"caller directory %q does not name a host", signatureAgent)
	}
	return caller, nil
}
