// What a publisher reads for each reason the Exchange gives when the file at
// their site does not prove their domain is theirs.

package wizard

import "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"

// reasonText is what a publisher reads for each of the Exchange's reason codes.
//
// The codes are stable and the sentences are not, which is why the mapping is
// here and the code is what travels. It is keyed on the shared constants, and a
// test holds it to consoleapi.AllReasons, so a code added to the contract
// without a sentence here fails the build's tests rather than reaching a
// publisher as a bare token.
//
// The file to serve is offered for download on the step that says these, so
// the repair names it as the fora.json from this page.
var reasonText = map[string]string{
	consoleapi.ReasonManifestMissing: fileMissingText,
	consoleapi.ReasonManifestUnreadable: "The file at /.well-known/fora.json could not be read. " +
		"Replace it with the fora.json from this page, unchanged.",
	consoleapi.ReasonManifestVersionRefused: "The file at /.well-known/fora.json is in a format we " +
		"no longer accept. Replace it with the fora.json from this page.",
	consoleapi.ReasonExchangeNotListed: "The file does not list this exchange. Use the fora.json " +
		"from this page, unchanged.",
	consoleapi.ReasonResourceOwnerMismatch: "The file names a different payee than your account. " +
		"Use the fora.json from this page, unchanged.",
	consoleapi.ReasonContributorNotListed: "The file does not authorise us to submit your catalogue. " +
		"Use the fora.json from this page, unchanged.",
	consoleapi.ReasonDomainTaken: "This domain is already registered to another publisher. " +
		"If it is yours, contact support to have it transferred.",
}

// reasonSentences turns the Exchange's reason codes into what a publisher
// reads.
//
// A code this console does not know still shows something: the person is told
// the file does not match and is given the code, which is better than an empty
// list that reads as though everything is fine. That can only happen when the
// Exchange is newer than the console.
func reasonSentences(codes []string) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		if s, ok := reasonText[c]; ok {
			out = append(out, s)
			continue
		}
		out = append(out, "The file does not match yet ("+c+").")
	}
	return out
}
