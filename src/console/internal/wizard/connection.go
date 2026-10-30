// The wizard's last step, Connect your site: what the publisher's site answers
// about its discovery file and its package, in the words the step shows, and
// the one place the console asks the Exchange to verify a domain.

package wizard

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// FileState is where the publisher's discovery file stands.
type FileState string

const (
	// FileServed means the Exchange read the file and it proves the domain is
	// the publisher's. Once the package answers, it also means an agent the
	// package refuses can read the file.
	FileServed FileState = "served"
	// FileMissing means there is no file at the address.
	FileMissing FileState = "missing"
	// FileMismatch means there is a file and it is not the one this account
	// was handed.
	FileMismatch FileState = "mismatch"
	// FileRefused means the Exchange can read the file and an AI agent cannot.
	// The package sends every agent it refuses to that address, so a file only
	// the Exchange can read is one agents are sent to and never see.
	FileRefused FileState = "refused"
)

// The sentences the file task says. One of each: the missing file is said the
// same way here and wherever a reason code says it (see reasonText).
const (
	fileMissingText = "We found no file at /.well-known/fora.json on your site yet."
	// fileBehindPackageText is the missing file when the package already
	// answers: the publisher has done one half and is told the request went
	// through it to a site with nothing there.
	fileBehindPackageText = "Your package passed the request on to your site, and your site has no " +
		"file at /.well-known/fora.json yet."
	fileMismatchText = "We read the file at your site and it does not match yet."
	fileRefusedText  = "Your site does not give AI agents the file at /.well-known/fora.json. " +
		"Let them read it: it is where your package sends every agent it refuses."
	fileServedText = "Your file is in place and names your account."
)

// FileTask is the step's first task: the discovery file the publisher serves
// from their own site.
type FileTask struct {
	Passed bool
	State  FileState
	// Status is the sentence under the task.
	Status string
	// Reasons say what is wrong with a file that is there and does not match,
	// one sentence per rule it breaks.
	Reasons []string
}

// PackageTask is the step's second task: the package on the publisher's CDN.
type PackageTask struct {
	Passed bool
	State  EdgeState
	// Status is the sentence under the task.
	Status string
	// Checks are the two questions asked at the licensed path: as an agent,
	// and as an ordinary visitor.
	Checks []EdgeCheck
}

// Connection is what one check of the step found: both tasks, each on its own,
// because the publisher does them in either order.
type Connection struct {
	File    FileTask
	Package PackageTask
}

// Ready reports whether both tasks are done, which is when the console asks
// the Exchange to verify the domain.
func (c Connection) Ready() bool { return c.File.Passed && c.Package.Passed }

// connectionOf says what the Exchange's reading of the file and the site's
// answers mean for each task.
func connectionOf(preflight exchangeclient.PreflightResult, report EdgeReport) Connection {
	return Connection{File: fileTask(preflight, report), Package: packageTask(report)}
}

// fileTask is the file task's state and sentence.
//
// The Exchange's reading decides it, with one addition the Exchange cannot
// see: whether an agent the package refuses can read the file too. That is
// asked only once the package answers, because the package is what sends
// agents there; before that a refusal at the address is the origin's own rule
// about a request from a server, and says nothing the publisher has to fix yet.
func fileTask(preflight exchangeclient.PreflightResult, report EdgeReport) FileTask {
	answered := report.State == EdgeDeployed
	switch {
	case containsFold(preflight.Reasons, consoleapi.ReasonManifestMissing):
		status := fileMissingText
		if answered {
			status = fileBehindPackageText
		}
		return FileTask{State: FileMissing, Status: status}
	case !preflight.Passed:
		return FileTask{State: FileMismatch, Status: fileMismatchText, Reasons: reasonSentences(preflight.Reasons)}
	case answered && report.Document == OutcomeFailed:
		return FileTask{State: FileRefused, Status: fileRefusedText}
	default:
		return FileTask{Passed: true, State: FileServed, Status: fileServedText}
	}
}

// packageTask is the package task's state and sentence.
func packageTask(report EdgeReport) PackageTask {
	return PackageTask{
		Passed: report.PackageAnswers(),
		State:  report.State,
		Status: edgeMessage(report.State),
		Checks: report.Checks,
	}
}

// checkConnection is the last step's check of a saved domain that has its
// terms: what the Exchange reads in the discovery file, what the site answers
// about its package, and -- once both tasks are done -- the Exchange's
// verification. It answers both tasks, and whether the domain is verified now.
//
// It is the only place the console asks the Exchange to verify a domain, and it
// asks the site itself first, in this same call. The Exchange verifies the
// discovery document itself -- it is the party that sells, so it reads the
// document again rather than trusting the reading above -- but it cannot see
// the package, which runs on the publisher's own CDN. So the tasks are built
// from what the site answers here, and the Exchange is asked only once both
// are done: the value that draws the page is the value that decides, so the
// page never shows both tasks done while the console holds back. A console
// with no site reader cannot see the package answer at all, so it verifies
// nothing.
//
// A failed verification is reported as not verified rather than as an error.
// The domain is saved, so an answer saying nothing was changed would be false,
// and the next check asks again. A failure to read the file is an error:
// nothing about either task is known then.
func (s *Service) checkConnection(
	ctx context.Context, actor exchangeclient.Actor, publisherID string, reg exchangeclient.Registration,
) (Connection, bool, error) {
	preflight, err := s.registry.PreflightDomain(ctx, actor, publisherID, reg.Domain)
	if err != nil {
		return Connection{}, false, err
	}
	conn := connectionOf(preflight, s.inspectEdge(ctx, reg.Domain, reg.ContentPathScope))
	if !conn.Ready() {
		return conn, false, nil
	}
	checked, err := s.registry.CheckDomain(ctx, actor, publisherID, reg.Domain)
	if err != nil {
		reqctx.FromContext(ctx).ErrorContext(ctx, "console.verify_failed", "domain", reg.Domain, "err", err)
		return conn, false, nil
	}
	return conn, checked.Passed, nil
}
