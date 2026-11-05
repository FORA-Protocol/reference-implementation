// Package setuptest holds what tests of the console's setup files share: the
// edge packages a release ships, as stand-ins a test can hand the console, a
// reader for the packages the console hands back and for the sections and lists
// of their READMEs, and the checks a test holds a package's README to.
package setuptest

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/setupsteps"
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

// Entries reads every file out of a package the console built, by name. A body
// that is not a zip fails the test.
func Entries(t testing.TB, raw []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("the package is not a zip: %v", err)
	}
	out := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(body)
	}
	return out
}

// CommandTexts is what a README has to show of each command, in order: its
// label, its command when it has one, and its answer when it has one.
func CommandTexts(commands []setupsteps.Command) []string {
	var out []string
	for _, c := range commands {
		for _, text := range []string{c.Label, c.Run, c.Expect} {
			if text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

// AssertInOrder fails unless every part appears in text, each after the one
// before it. what names the text in the failure.
func AssertInOrder(t testing.TB, what, text string, parts []string) {
	t.Helper()
	at := 0
	for _, part := range parts {
		i := strings.Index(text[at:], part)
		if i < 0 {
			t.Fatalf("%s does not carry %q after what comes before it:\n%s", what, part, text)
		}
		at += i + len(part)
	}
}

// ReadmeSection is a README's text under heading, a "## " heading at the start
// of a line, up to the next heading of the same level. A README without the
// heading fails the test.
func ReadmeSection(t testing.TB, readme, heading string) string {
	t.Helper()
	start := strings.Index(readme, "\n"+heading+"\n")
	if start < 0 {
		t.Fatalf("the README has no %q:\n%s", heading, readme)
	}
	body := readme[start+len(heading)+2:]
	if end := strings.Index(body, "\n## "); end >= 0 {
		body = body[:end+1]
	}
	return body
}

// ReadmeItems reads the list under heading, by key. A README draws every list
// the same way, "- **key**: text", with the key in bold so the eye finds it
// first. A line under the heading in any other form, or a key listed twice,
// fails the test, so a list cannot lose its form while a test reads nothing.
func ReadmeItems(t testing.TB, readme, heading string) map[string]string {
	t.Helper()
	items := map[string]string{}
	for _, line := range strings.Split(ReadmeSection(t, readme, heading), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rest, bold := strings.CutPrefix(line, "- **")
		key, text, ok := strings.Cut(rest, "**: ")
		if !bold || !ok || key == "" || strings.TrimSpace(text) == "" {
			t.Fatalf("%q under %q is not a list line with its key in bold:\n%s", line, heading, readme)
		}
		if _, twice := items[key]; twice {
			t.Fatalf("%q is listed twice under %q:\n%s", key, heading, readme)
		}
		items[key] = text
	}
	return items
}
