package setupsteps

import (
	"encoding/json"
	"testing"
)

// TestTheChecksAreWrittenOnlyAsPackageChecks reads the data file as it is
// written, before the package checks are added to any variant.
//
// The checks are the questions the console asks every site, so they live once,
// under package_checks, and every one of them is marked checked. A variant that
// writes a checked command of its own adds a question to its network alone. If
// every package variant wrote the same one, each network would still hand over
// the same checks, so only the file as written shows it.
func TestTheChecksAreWrittenOnlyAsPackageChecks(t *testing.T) {
	t.Parallel()
	var d data
	if err := json.Unmarshal(stepsJSON, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.PackageChecks) == 0 {
		t.Fatal("the data file writes no package checks")
	}
	for _, c := range d.PackageChecks {
		if !c.Checked {
			t.Fatalf("the package check %q is not marked checked", c.Label)
		}
	}
	for name, step := range d.Integrations {
		for _, c := range step.Commands {
			if c.Checked {
				t.Fatalf("%s writes the checked command %q of its own; "+
					"the checks belong under package_checks", name, c.Label)
			}
		}
	}
}
