package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 9, 17, 8, 30, 15, 0, time.UTC)

// textEscaper is what html/template does to a value in text context, so a
// test can look for a detail string containing quotes or a plus sign.
var textEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;", "+", "&#43;")

func renderFixtureHTML(t *testing.T, s Sources) string {
	t.Helper()
	var b strings.Builder
	if err := RenderHTML(&b, BuildLedger(s), fixedNow); err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	return b.String()
}

// The HTML report carries everything the terminal shows: both identifiers,
// every recorded step with its correlator and crypto, every assertion by name,
// and the same one-line summary. Anything dropped here would make the saved
// report a weaker record than the terminal output it stands in for.
func TestRenderHTML_carriesTheWholeConsoleReport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	l := BuildLedger(f.Sources)
	page := renderFixtureHTML(t, f.Sources)

	mustShow := func(values ...string) {
		for _, w := range values {
			if !strings.Contains(page, textEscaper.Replace(w)) {
				t.Errorf("report is missing %q", w)
			}
		}
	}
	mustShow(fixtureTx, fixtureTenant, Summarize(l.Assertions), "Verified: every checked assertion holds")
	for _, r := range l.Rows {
		mustShow(r.Party, r.Step, r.Event, r.Correlator, r.Crypto)
	}
	for _, a := range l.Assertions {
		mustShow(a.Name, a.Detail)
	}
	if strings.Contains(page, ">Unchecked<") || strings.Contains(page, ">Failed<") {
		t.Error("an intact chain rendered a non-passing badge")
	}
}

// The signed URL is a live bearer capability until it expires, and the report
// is a file that gets attached to tickets and mailed around. Only its digest
// may appear.
func TestRenderHTML_neverCarriesTheSignedURL(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	html := renderFixtureHTML(t, f.Sources)
	if strings.Contains(html, fixtureSignedURL) || strings.Contains(html, "sig=deadbeef") {
		t.Error("the bearer signed URL leaked into the report")
	}
	if !strings.Contains(html, f.URLDigest[:16]) {
		t.Error("the URL digest, which is the join key, is missing")
	}
}

// The page must open from a file:// URL on a machine with no network: no
// script, stylesheet, font or image may be fetched from anywhere.
func TestRenderHTML_isSelfContained(t *testing.T) {
	t.Parallel()
	html := renderFixtureHTML(t, newFixture(t).Sources)
	for _, external := range []string{"<script", "<link", "@import", "url(", "src="} {
		if strings.Contains(html, external) {
			t.Errorf("report references an external asset via %q", external)
		}
	}
	if !strings.Contains(html, "<style>") {
		t.Error("styling is not embedded")
	}
}

// Every dynamic value comes from a database or a log line another party wrote,
// so it is untrusted markup. html/template must neutralise it.
func TestRenderHTML_escapesDynamicContent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	l := BuildLedger(f.Sources)
	payload := `<script>alert("x")</script>`
	l.TransactionID = payload
	l.Rows[0].Event = payload
	l.Assertions[0].Detail = payload

	var b strings.Builder
	if err := RenderHTML(&b, l, fixedNow); err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	html := b.String()
	if strings.Contains(html, payload) {
		t.Fatal("raw markup from a dynamic value reached the page")
	}
	if n := strings.Count(html, "&lt;script&gt;"); n < 3 {
		t.Errorf("expected the payload escaped at 3 sites, found %d", n)
	}
}

// A leg that was not read is rendered as a gap with its reason, and the
// assertions that needed it are marked unchecked, never failed. The callout at
// the top says the report is incomplete, not that anything is wrong.
func TestRenderHTML_incompleteEvidenceIsIncompleteNotFailed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	s := f.Sources
	s.Selection = nil
	s.SelectionAbsence = "not read: no Broker database URL configured"
	html := renderFixtureHTML(t, s)

	if !strings.Contains(html, `<td class="absence" colspan="3">— not read: no Broker database URL configured</td>`) {
		t.Error("the missing Broker leg is not rendered as a gap with its reason")
	}
	if !strings.Contains(html, `class="badge badge-unchecked">Unchecked<`) {
		t.Error("an assertion that needed the missing leg is not marked unchecked")
	}
	if strings.Contains(html, `class="badge badge-failed"`) {
		t.Error("a missing source was rendered as a failed check")
	}
	if !strings.Contains(html, `class="aside aside-caution"`) || !strings.Contains(html, "could not be checked") {
		t.Error("the summary callout does not say the report is incomplete")
	}
}

// A forged row produces a failed badge and a danger callout that names the
// count; the failed assertion keeps its detail so the operator sees what did
// not line up.
func TestRenderHTML_failedAssertionIsMarkedAndCounted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.Sources.Evidence.OfferSig = strings.Repeat("00", 64)
	html := renderFixtureHTML(t, f.Sources)

	if !strings.Contains(html, `class="badge badge-failed">Failed<`) {
		t.Error("the forged signature is not marked failed")
	}
	if !strings.Contains(html, `class="aside aside-danger"`) || !strings.Contains(html, " FAILED") {
		t.Error("the summary callout does not report the failure")
	}
}

// The default name is built from an operator-typed id and lands on their
// filesystem, so the id is reduced to a safe alphabet and capped in length.
func TestDefaultReportName_sanitizesTheTransactionID(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		fixtureTx:                "ledger-" + fixtureTx + "-20260917-083015-UTC.html",
		"../../etc/passwd":       "ledger-.._.._etc_passwd-20260917-083015-UTC.html",
		"a b;c|d$(e)/f\\g":       "ledger-a_b_c_d_e_f_g-20260917-083015-UTC.html",
		"":                       "ledger-tx-20260917-083015-UTC.html",
		"..":                     "ledger-tx-20260917-083015-UTC.html",
		"日本":                     "ledger-_-20260917-083015-UTC.html",
		strings.Repeat("x", 200): "ledger-" + strings.Repeat("x", 64) + "-20260917-083015-UTC.html",
	}
	safe := regexp.MustCompile(`^ledger-[A-Za-z0-9._-]+-\d{8}-\d{6}-UTC\.html$`)
	for id, want := range cases {
		got := defaultReportName(id, fixedNow)
		if got != want {
			t.Errorf("defaultReportName(%q) = %q, want %q", id, got, want)
		}
		if !safe.MatchString(got) || strings.Contains(got, "/") {
			t.Errorf("defaultReportName(%q) = %q is not a safe bare filename", id, got)
		}
	}
}

// A report is never silently replaced: the second write to the same name is
// refused, the first file is untouched, and the file is private to the
// operator.
func TestWriteReport_refusesToOverwriteAndKeepsTheFilePrivate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "report.html")

	abs, err := writeReport(path, []byte("first"))
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if !filepath.IsAbs(abs) {
		t.Errorf("returned path %q is not absolute", abs)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("report permissions = %o, want 600", perm)
	}

	if _, err := writeReport(path, []byte("second")); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second write to the same name should be refused, got err=%v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "first" {
		t.Errorf("the existing report was overwritten: %q", got)
	}
}

// browserSpy records whether the launcher was called and can be made to fail,
// so no test opens a window on the developer's desktop.
type browserSpy struct {
	opened []string
	err    error
}

func (s *browserSpy) open(_ context.Context, path string) error {
	s.opened = append(s.opened, path)
	return s.err
}

func emitHTML(t *testing.T, out Output) (stdout string, spy *browserSpy) {
	t.Helper()
	spy = &browserSpy{err: out.openBrowser(context.Background(), t.Name())}
	out.openBrowser, out.now = spy.open, func() time.Time { return fixedNow }
	var b strings.Builder
	if err := emit(context.Background(), &b, out, BuildLedger(newFixture(t).Sources)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	return b.String(), spy
}

func failingBrowser(context.Context, string) error {
	return errors.New("exec: \"xdg-open\": not found")
}

func workingBrowser(context.Context, string) error { return nil }

// A browser that cannot be launched is a warning, not an error: the report is
// on disk, its path was printed, and the operator can open it by hand.
func TestEmit_browserFailureKeepsTheReport(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	stdout, spy := emitHTML(t, Output{Format: formatHTML, Open: true, openBrowser: failingBrowser})

	wantPath := filepath.Join(dir, "ledger-"+fixtureTx+"-20260917-083015-UTC.html")
	if len(spy.opened) != 1 || spy.opened[0] != wantPath {
		t.Errorf("browser opened on %v, want [%s]", spy.opened, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("report was not kept after the browser failed: %v", err)
	}
	if !strings.Contains(stdout, "report saved to "+wantPath) {
		t.Errorf("absolute path not printed:\n%s", stdout)
	}
	if !strings.Contains(stdout, "warning: could not open a browser") || !strings.Contains(stdout, "by hand") {
		t.Errorf("no actionable warning printed:\n%s", stdout)
	}
}

// -open=false saves and prints the path but never touches the launcher;
// -output picks the path. Text output touches neither.
func TestEmit_honoursOpenAndOutputOverrides(t *testing.T) {
	t.Chdir(t.TempDir())
	custom := filepath.Join(t.TempDir(), "custom.html")

	stdout, spy := emitHTML(t, Output{Format: formatHTML, Path: custom, Open: false, openBrowser: workingBrowser})
	if len(spy.opened) != 0 {
		t.Errorf("browser opened with -open=false: %v", spy.opened)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Errorf("-output was not honoured: %v", err)
	}
	if !strings.Contains(stdout, "report saved to "+custom) || strings.Contains(stdout, "warning") {
		t.Errorf("unexpected stdout:\n%s", stdout)
	}

	stdout, spy = emitHTML(t, Output{Format: formatText, Open: true, openBrowser: workingBrowser})
	if len(spy.opened) != 0 {
		t.Errorf("text output opened a browser: %v", spy.opened)
	}
	if !strings.HasPrefix(stdout, "FORA evidence chain") || strings.Contains(stdout, "report saved") {
		t.Errorf("text output changed:\n%s", stdout)
	}
	if entries, _ := os.ReadDir("."); len(entries) != 0 {
		t.Errorf("text output wrote files: %v", entries)
	}
}

// The flags default to the unchanged terminal behaviour; the HTML options are
// parsed when given, and options that contradict each other are refused before
// any source is read.
func TestParseConfig_outputOptions(t *testing.T) {
	t.Parallel()
	base := []string{"-tx", fixtureTx, "-admin-url", "http://127.0.0.1:8082"}

	cfg, err := parseConfig(base)
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if cfg.Output.Format != formatText || cfg.Output.Path != "" || !cfg.Output.Open {
		t.Errorf("defaults = %+v, want text, no path, open", cfg.Output)
	}

	cfg, err = parseConfig(append(base, "-format", "html", "-output", "r.html", "-open=false"))
	if err != nil {
		t.Fatalf("html options: %v", err)
	}
	if cfg.Output.Format != formatHTML || cfg.Output.Path != "r.html" || cfg.Output.Open {
		t.Errorf("html options = %+v", cfg.Output)
	}

	if _, err := parseConfig(append(base, "-format", "pdf")); err == nil || !strings.Contains(err.Error(), "-format must be") {
		t.Errorf("an unknown format was accepted: %v", err)
	}
	if _, err := parseConfig(append(base, "-output", "r.html")); err == nil || !strings.Contains(err.Error(), "html only") {
		t.Errorf("-output without -format html was accepted: %v", err)
	}
}
