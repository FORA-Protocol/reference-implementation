// This file keeps the hand-written key-directory reader deleted. Reading a Web
// Bot Auth key directory is the SDK's: https only, no redirect, a 200 answer under
// the directory media type, a capped body, and response signatures checked so
// only the keys that signed are used. Production code reads a directory through
// forawellknown.NewDirectoryFetcher (the SDK's resolvers.NewWBADirectoryFetcher)
// or through internal/agentkeys (the SDK's resolvers.WBAKeyResolver), so request
// verification, agent registration and offer-key resolution cannot disagree about
// one document.
package guards

import (
	"regexp"
	"testing"
)

// directoryReadCall matches the load-bearing token of a hand-written directory
// reader: checking a key directory's response signatures, which no reader can
// skip, and the name of the reader this repository used to keep. The scan reads
// raw bytes, comments included, so a call through an aliased helpers import still
// matches on the function name.
var directoryReadCall = regexp.MustCompile(`\bVerifyDirectoryResponse\(|\bFetchWBA\b`)

// directoryReadAllowlist is EMPTY: no app file is sanctioned to check a key
// directory's response itself. The SDK does it inside every directory read, and
// the services' own directory handlers only sign responses. The mechanism stays
// so a future, justified exception is a one-line entry.
var directoryReadAllowlist = map[string]bool{}

// TestNoHandWrittenDirectoryReader fails when app source checks a key
// directory's response itself or brings the deleted reader back.
func TestNoHandWrittenDirectoryReader(t *testing.T) {
	t.Parallel()
	assertNoMatchOutsideAllowlist(t, directoryReadCall, directoryReadAllowlist,
		"reads a key directory by hand — read it through forawellknown.NewDirectoryFetcher "+
			"(the SDK's directory fetcher) or internal/agentkeys instead")
}

// --- meta-tests: prove the detector itself works ---------------------------

// TestMeta_DirectoryReadDetectorFlagsHandWrittenReaders is the positive
// meta-test: the old reader's call and a response check, plain or through an
// aliased import, all match.
func TestMeta_DirectoryReadDetectorFlagsHandWrittenReaders(t *testing.T) {
	t.Parallel()
	for _, dirty := range []string{
		`f, err := forawellknown.FetchWBA(ctx, h, o)`,
		`verified, err := helpers.VerifyDirectoryResponse(a, h, b, k, now)`,
		`verified, err := sdkhelpers.VerifyDirectoryResponse(a, h, b, k, now)`,
	} {
		if !directoryReadCall.MatchString(dirty) {
			t.Fatalf("directory-read detector missed a hand-written reader: %q", dirty)
		}
	}
}

// TestMeta_DirectoryReadDetectorPassesSDKReads is the negative meta-test: the
// two sanctioned ways to read a directory must NOT match.
func TestMeta_DirectoryReadDetectorPassesSDKReads(t *testing.T) {
	t.Parallel()
	for _, clean := range []string{
		`readDirectory := forawellknown.NewDirectoryFetcher(c, clk, 0)`,
		`fetch := resolvers.NewWBADirectoryFetcher(c, "", "", o)`,
	} {
		if directoryReadCall.MatchString(clean) {
			t.Fatalf("directory-read detector wrongly flagged an SDK read: %q", clean)
		}
	}
}

// TestMeta_DirectoryReadAllowlistIsEmpty pins the allowlist at zero entries: an
// allowlist may only shrink.
func TestMeta_DirectoryReadAllowlistIsEmpty(t *testing.T) {
	t.Parallel()
	if len(directoryReadAllowlist) != 0 {
		t.Fatalf("directoryReadAllowlist must stay empty, got %v", directoryReadAllowlist)
	}
}
