// Package setuptest holds what tests of the console's setup files share: the
// edge packages a release ships, as stand-ins a test can hand the console.
package setuptest

import (
	"os"
	"path/filepath"
	"testing"
)

// ReleaseDir writes the edge packages a release ships into a fresh directory
// and returns it: what CONSOLE_EDGE_PACKAGE_DIR names in a deployment that hands
// publishers a package to install.
//
// The bodies are markers rather than real bundles. What a test of the setup
// files holds is how a package is assembled, and a real bundle would make every
// assertion about esbuild's output instead. The file names are written out here
// rather than taken from the setup package: they are the names
// src/edge/scripts/release-targets.mjs gives the bundles a release builds, so a
// rename on one side only fails the tests instead of being followed by them.
func ReleaseDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"cloudflare-worker.mjs": "export default { fetch() {} };\n",
		"lambda-edge.mjs":       "export { handler };\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
