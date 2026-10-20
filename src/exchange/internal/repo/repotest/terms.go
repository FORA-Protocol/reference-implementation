//go:build integration

package repotest

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

// fixtureTermsFile is the licensing every seeded listing carries: free, every
// offered use permitted, no attribution. It is one file at the repository root
// because the e2e seed writes the same listing terms, and two copies of one
// document drift. The path is relative to the repository root.
const fixtureTermsFile = "testdata/free-listing-terms.json"

// fixtureTerms reads the shared listing terms. It first decodes them into the
// shared licensing control set, refusing a member that set does not know, so
// the file cannot hold a document no publisher could have agreed; the row then
// stores the file's own bytes. It is read from a file rather than built
// through the service, because this package seeds rows and the service is what
// the suites under test drive.
func fixtureTerms(tb testing.TB) []byte {
	tb.Helper()
	// This file sits five directories below the repository root; the caller's
	// working directory varies with the package under test, so the path is
	// taken from here instead.
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		tb.Fatal("locate the repotest package on disk")
	}
	root := filepath.Join(filepath.Dir(self), "..", "..", "..", "..", "..")
	raw, err := fs.ReadFile(os.DirFS(root), fixtureTermsFile)
	if err != nil {
		tb.Fatalf("read the shared listing terms: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var terms termspreset.Controls
	if err := dec.Decode(&terms); err != nil {
		tb.Fatalf("decode the shared listing terms: %v", err)
	}
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		tb.Fatalf("compact the shared listing terms: %v", err)
	}
	return out.Bytes()
}
