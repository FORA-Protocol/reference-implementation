//go:build integration

package app_test

import (
	"encoding/json"
	"flag"
	"html"
	"os"
	"reflect"
	"regexp"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
)

// periodRulesFixture is the rules the period picker's module is tested with,
// kept beside that module's tests.
const periodRulesFixture = "../../js/tests/testdata/period-rules.json"

var updatePeriodRules = flag.Bool("update-period-rules", false,
	"rewrite the period picker's rules fixture from what a page hands over")

// rePeriodRules finds the rules a page hands its period picker.
var rePeriodRules = regexp.MustCompile(`data-daterange-rules="([^"]*)"`)

// TestDashboard_PeriodRulesFixtureIsWhatThePagesHandOver keeps the picker's
// tests on the server's rules rather than a copy of them. It reads the rules
// the overview hands its picker: the longest period and the sentences the
// server refuses a reversed or too long one with. With -update-period-rules
// it writes them as the fixture; without it, the fixture must be exactly
// them, so a changed limit or sentence cannot leave the picker tested against
// the old one.
func TestDashboard_PeriodRulesFixtureIsWhatThePagesHandOver(t *testing.T) {
	f := startSeller(t)
	m := rePeriodRules.FindStringSubmatch(f.get2(t, dashboard.PathOverview))
	if m == nil {
		t.Fatal("the overview hands its period picker no rules")
	}
	var page map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &page); err != nil {
		t.Fatalf("the picker's rules are not JSON: %v", err)
	}
	if *updatePeriodRules {
		raw, err := json.MarshalIndent(page, "", "  ")
		if err != nil {
			t.Fatalf("marshal rules: %v", err)
		}
		if err := os.WriteFile(periodRulesFixture, append(raw, '\n'), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	raw, err := os.ReadFile(periodRulesFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("the fixture is not JSON: %v", err)
	}
	if !reflect.DeepEqual(fixture, page) {
		t.Fatalf("the fixture holds %v, the page hands over %v; rerun with -update-period-rules", fixture, page)
	}
}
