package wizard

import (
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// The connect step's two tasks, read off the Exchange's reading of the file and
// the site's answers. Each case is one state the step can be in; the step does
// its two tasks in either order, so each task is decided on its own.
func TestConnectionOf(t *testing.T) {
	t.Parallel()
	missing := exchangeclient.PreflightResult{Reasons: []string{consoleapi.ReasonManifestMissing}}
	served := exchangeclient.PreflightResult{Passed: true}
	wrong := exchangeclient.PreflightResult{Reasons: []string{
		consoleapi.ReasonResourceOwnerMismatch, consoleapi.ReasonContributorNotListed,
	}}
	absent := EdgeReport{State: EdgeAbsent, Checks: []EdgeCheck{agentCheck(EdgeAbsent), passedCheck()}}
	answering := func(document Outcome) EdgeReport {
		return EdgeReport{State: EdgeDeployed, Checks: []EdgeCheck{agentCheck(EdgeDeployed), passedCheck()}, Document: document}
	}

	for _, tc := range []struct {
		name      string
		preflight exchangeclient.PreflightResult
		report    EdgeReport
		file      FileTask
		packageOK bool
		ready     bool
	}{
		{
			name: "nothing done", preflight: missing, report: absent,
			file: FileTask{State: FileMissing, Status: fileMissingText},
		},
		{
			name: "file served, no package yet", preflight: served, report: absent,
			file: FileTask{Passed: true, State: FileServed, Status: fileServedText},
		},
		{
			name: "package answering, no file yet", preflight: missing, report: answering(OutcomeFailed),
			file:      FileTask{State: FileMissing, Status: fileBehindPackageText},
			packageOK: true,
		},
		{
			name: "a file for somebody else", preflight: wrong, report: absent,
			file: FileTask{State: FileMismatch, Status: fileMismatchText, Reasons: reasonSentences(wrong.Reasons)},
		},
		{
			name: "the file refused to agents behind the package", preflight: served, report: answering(OutcomeFailed),
			file:      FileTask{State: FileRefused, Status: fileRefusedText},
			packageOK: true,
		},
		{
			// Before the package answers, a refusal at the address is the
			// origin's own rule about a request from a server, and the file
			// task stands on the Exchange's reading alone.
			name: "a refusal at the address without the package", preflight: served,
			report: EdgeReport{State: EdgeAbsent, Checks: absent.Checks, Document: OutcomeFailed},
			file:   FileTask{Passed: true, State: FileServed, Status: fileServedText},
		},
		{
			name: "an unclear answer at the address behind the package", preflight: served, report: answering(OutcomeUnclear),
			file:      FileTask{Passed: true, State: FileServed, Status: fileServedText},
			packageOK: true, ready: true,
		},
		{
			name: "both done", preflight: served, report: answering(OutcomePassed),
			file:      FileTask{Passed: true, State: FileServed, Status: fileServedText},
			packageOK: true, ready: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := connectionOf(tc.preflight, tc.report)
			if got.File.Passed != tc.file.Passed || got.File.State != tc.file.State ||
				got.File.Status != tc.file.Status || !slices.Equal(got.File.Reasons, tc.file.Reasons) {
				t.Errorf("file task = %+v, want %+v", got.File, tc.file)
			}
			if got.Package.Passed != tc.packageOK || got.Package.State != tc.report.State ||
				got.Package.Status != edgeMessage(tc.report.State) {
				t.Errorf("package task = %+v, want passed %v in state %s", got.Package, tc.packageOK, tc.report.State)
			}
			if got.Ready() != tc.ready {
				t.Errorf("Ready = %v, want %v", got.Ready(), tc.ready)
			}
		})
	}
}

// A package that answers us and refuses an ordinary visitor is not done: the
// failure is the one that costs the publisher readers.
func TestConnectionOf_APackageRefusingVisitorsIsNotDone(t *testing.T) {
	t.Parallel()
	report := EdgeReport{State: EdgeDeployed, Checks: []EdgeCheck{
		agentCheck(EdgeDeployed), {Label: "People and search crawlers are let through", Outcome: OutcomeFailed},
	}, Document: OutcomePassed}
	got := connectionOf(exchangeclient.PreflightResult{Passed: true}, report)
	if got.Package.Passed || got.Ready() {
		t.Fatalf("a package refusing visitors reads as done: %+v", got.Package)
	}
}

func passedCheck() EdgeCheck {
	return EdgeCheck{Label: "People and search crawlers are let through", Outcome: OutcomePassed}
}
