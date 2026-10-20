// Package setupsteps carries the onboarding path a publisher is shown: what they
// do, in what order, and the commands for the network they already run.
//
// The text lives in a data file next to this code rather than on the public
// website, so the website has nothing to keep in step and two callers read one
// copy of it: the public preview page, which shows a visitor what onboarding
// would involve, and the publisher console, which shows the same steps to
// someone who has registered and gives them the real values. The wording is
// provisional: the onboarding guide being written against a real publisher
// integration will own it, and this file is where that guide lands.
package setupsteps

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
)

//go:embed steps.json
var stepsJSON []byte

// hostPlaceholder is replaced with the publisher's own host wherever it appears
// in the data file, so a publisher reads commands they can run rather than
// commands they have to edit first.
const hostPlaceholder = "<host>"

// Command is one thing to run, and what it should answer.
//
// Checked marks a command a caller may be able to answer on the publisher's
// behalf. Every such command is a curl against a public address, so a caller
// that can make requests -- the console, on a site it has registered -- gets
// the same answer without the publisher opening a terminal. The deployment
// commands are not marked: they run against the publisher's own CDN account
// with the publisher's own credentials, which is exactly what we do not hold.
//
// Run is empty for a step done by hand in a web console, which has nothing to
// paste: its instruction is Expect alone, and no code block is drawn for it.
// A long command is split with " \" continuations so it fits its block
// without scrolling sideways.
type Command struct {
	Label  string `json:"label"`
	Run    string `json:"run,omitempty"`
	Expect string `json:"expect,omitempty"`
	// Checked says the answer can be obtained by asking the site. See the type
	// comment; RunnableByHand drops these.
	Checked bool `json:"checked,omitempty"`
}

// Step is one stage of onboarding.
type Step struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Integration names the path this step takes for the detected network. It
	// is set on the edge step only, and is "contact_us" when no supported
	// network was found.
	Integration string `json:"integration,omitempty"`
	// Commands are what the publisher runs, when the step has commands.
	Commands []Command `json:"commands,omitempty"`
	// Manifest is the discovery document this publisher would serve, carried
	// on the step that asks them to serve it.
	Manifest json.RawMessage `json:"manifest,omitempty"`
	// Note is a caveat about this step, such as an integration whose written
	// procedure does not exist yet.
	Note string `json:"note,omitempty"`
}

// data is the parsed file: the steps that are the same for everyone, plus the
// per-network variant of the edge step.
type data struct {
	Steps        []Step          `json:"steps"`
	Integrations map[string]Step `json:"integrations"`
}

// parsed is decoded once. A malformed data file is a build-time mistake that
// would otherwise surface as an empty step list on a live request, so it panics
// here instead: the file is compiled in, so if it parses at startup it parses
// for every request.
var parsed = mustParse()

func mustParse() data {
	var d data
	if err := json.Unmarshal(stepsJSON, &d); err != nil {
		panic(fmt.Sprintf("steps: the embedded step data does not parse: %v", err))
	}
	return d
}

// For returns the onboarding path for one publisher: the shared steps in order,
// with the edge step replaced by the variant for the network they run and the
// manifest attached to the step that asks them to serve it.
//
// A publisher on no supported network gets the same path with a different third
// step, rather than a truncated one. The other three steps are the same work
// whichever network they use, and showing them is what makes the conversation
// they are invited into a short one.
func For(provider cdn.Provider, host string, manifest json.RawMessage) []Step {
	out := make([]Step, 0, len(parsed.Steps))
	for _, step := range parsed.Steps {
		if step.ID == edgeStepID {
			step = integrationStep(provider)
		}
		if step.ID == manifestStepID {
			step.Manifest = manifest
		}
		out = append(out, substituteHost(step, host))
	}
	return out
}

// RunnableByHand drops the commands a caller has already answered by asking
// the site, leaving the steps that still need a person.
//
// It exists so one data file serves two readers. The public preview page shows
// a visitor the whole procedure, checks included, because it has no site of
// theirs to ask and nothing to report. The console has both, so showing a
// publisher a command whose answer is printed further up the same page asks
// them to do work to learn what they have just been told.
//
// A step whose commands were all checked keeps its title, summary and note: the
// step still happened, and the note is where an integration says its written
// procedure is not finished.
func RunnableByHand(steps []Step) []Step {
	out := make([]Step, 0, len(steps))
	for _, step := range steps {
		kept := make([]Command, 0, len(step.Commands))
		for _, c := range step.Commands {
			if !c.Checked {
				kept = append(kept, c)
			}
		}
		if len(kept) == 0 {
			kept = nil
		}
		step.Commands = kept
		out = append(out, step)
	}
	return out
}

// The two steps For has to recognise: the one whose content depends on the
// network, and the one that carries the manifest.
const (
	edgeStepID     = "edge"
	manifestStepID = "manifest"
)

// The two paths an edge step can take, as the data file writes them.
const (
	// IntegrationEdgePackage means a package is shipped for the network and
	// the step says how to deploy it.
	IntegrationEdgePackage = "edge_package"
	// IntegrationContactUs means no package is shipped and the step is a
	// conversation.
	IntegrationContactUs = "contact_us"
)

// Integration names the path the edge step of a route takes. The preview
// copies it into the answer's cdn.integration rather than deciding again, so
// the data file is the one place that says which networks have a package.
func Integration(path []Step) string {
	for _, step := range path {
		if step.ID == edgeStepID {
			return step.Integration
		}
	}
	return ""
}

// integrationStep picks the variant for a network, falling back to the
// contact-us path for anything the data file does not describe.
func integrationStep(provider cdn.Provider) Step {
	if step, ok := parsed.Integrations[string(provider)]; ok {
		return step
	}
	return parsed.Integrations[string(cdn.ProviderNone)]
}

// substituteHost puts the publisher's own host into the text.
func substituteHost(step Step, host string) Step {
	if host == "" {
		return step
	}
	step.Title = strings.ReplaceAll(step.Title, hostPlaceholder, host)
	step.Summary = strings.ReplaceAll(step.Summary, hostPlaceholder, host)
	step.Note = strings.ReplaceAll(step.Note, hostPlaceholder, host)
	if len(step.Commands) == 0 {
		return step
	}
	commands := make([]Command, len(step.Commands))
	for i, c := range step.Commands {
		commands[i] = Command{
			Label:   c.Label,
			Run:     strings.ReplaceAll(c.Run, hostPlaceholder, host),
			Expect:  strings.ReplaceAll(c.Expect, hostPlaceholder, host),
			Checked: c.Checked,
		}
	}
	step.Commands = commands
	return step
}
