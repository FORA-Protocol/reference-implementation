//go:build integration

package app_test

import (
	"flag"
	"os"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
)

// pickerFixture is the markup the period picker's DOM tests mount the picker
// on, kept beside those tests.
const pickerFixture = "../../js/tests/testdata/period-picker.html"

var updatePicker = flag.Bool("update-picker", false,
	"rewrite the period picker's markup fixture from what the overview renders")

// TestDashboard_PickerFixtureIsWhatThePageRenders keeps the picker's DOM tests
// on the page's own markup. It renders the overview over a named period, so the
// markup is the same on every run, and takes its period form: the two hidden
// fields and the picker. With -update-picker it writes that as the fixture;
// without it, the fixture must be exactly that, so a change to the markup
// cannot leave the tests driving a picker the page no longer draws.
func TestDashboard_PickerFixtureIsWhatThePageRenders(t *testing.T) {
	f := startSeller(t)
	body := f.get2(t, dashboard.PathOverview+"?from=2026-09-01&to=2026-09-30")
	start := strings.Index(body, `<form class="period`)
	if start < 0 {
		t.Fatal("the overview has no period form")
	}
	end := strings.Index(body[start:], "</form>")
	if end < 0 {
		t.Fatal("the overview's period form does not end")
	}
	markup := body[start:start+end+len("</form>")] + "\n"

	if *updatePicker {
		if err := os.WriteFile(pickerFixture, []byte(markup), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	raw, err := os.ReadFile(pickerFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if string(raw) != markup {
		t.Fatalf("the fixture differs from the period form the overview renders; rerun with -update-picker")
	}
}
