package testutil

import (
	"fmt"
	"regexp"
	"strconv"
)

// Reading the agent-binding proof a fetcher sends. The signer is the protocol
// SDK's (helpers.SignAgentBinding), and the production verifier is the edge's
// TypeScript one. The Go SDK ships a verifier too (helpers.VerifyAgentBinding),
// which the enforcing edge double deliberately does not call: it rebuilds the
// signature base itself (edgedouble_proof.go), and the shared vectors
// (testdata/pop-sign-vectors.json, replayed in edgedouble_test.go) hold it to
// the SDK's signer byte for byte and to the SDK verifiers' verdicts.

// popWindow finds the two freshness instants in a Signature-Input parameter
// list.
var popWindow = regexp.MustCompile(`created=(\d+);expires=(\d+)`)

// PoPWindowOf reads the created and expires instants out of a Signature-Input
// header, as unix seconds. The difference between the two IS the window a bound
// fetch can be repeated in, so a test that asserts that lifetime reads it here.
func PoPWindowOf(signatureInput string) (created, expires int64, err error) {
	m := popWindow.FindStringSubmatch(signatureInput)
	if m == nil {
		return 0, 0, fmt.Errorf("testutil: no created/expires in Signature-Input %q", signatureInput)
	}
	if created, err = strconv.ParseInt(m[1], 10, 64); err != nil {
		return 0, 0, fmt.Errorf("testutil: parse created in %q: %w", signatureInput, err)
	}
	if expires, err = strconv.ParseInt(m[2], 10, 64); err != nil {
		return 0, 0, fmt.Errorf("testutil: parse expires in %q: %w", signatureInput, err)
	}
	return created, expires, nil
}
