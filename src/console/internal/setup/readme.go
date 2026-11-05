package setup

import (
	"bytes"
	_ "embed" // the README template
	"fmt"
	"text/template"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/setupsteps"
)

// readmeFile is the instructions file every package carries.
const readmeFile = "README.md"

//go:embed readme.md.tmpl
var readmeSource string

// readmeTemplate is parsed once, when the console starts. The template is
// compiled in, so one that does not parse is a build mistake, and it fails at
// start-up rather than on a publisher's download.
//
// It is a text template on purpose: an HTML template would escape the quotes
// in every command, and a publisher would paste &#34; into their terminal.
var readmeTemplate = template.Must(template.New(readmeFile).Parse(readmeSource))

// readmeInput is what one package's README is written from.
//
// Every value in it is one the console produces or the deployment's operator
// set: the normalized domain and values derived from it, the Exchange the
// package was built for, the operator's address and the step text compiled
// into the console. Nothing a publisher typed reaches it, so nothing in the
// README can be read as Markdown or HTML somebody else wrote.
type readmeInput struct {
	network cdn.Provider
	domain  string
	// supportEmail is who the README tells a publisher to write to. Empty
	// leaves that line out.
	supportEmail string
	// beforeDeploy is the sentence the network needs in front of its deploy
	// commands, about where to run them.
	beforeDeploy string
	// botVerification is the sentence the README adds after it says the
	// package recognises AI agents by the names they send: what else the
	// network's runtime trusts. Only the Cloudflare entry hands the app the
	// platform's own bot verification (src/edge/src/entries/cloudflare.ts),
	// and only when Cloudflare provides it; the Lambda@Edge entry hands none,
	// so that README adds nothing. A test reads both entries and holds this
	// sentence to them.
	botVerification string
	// files are the package's files in the order the README lists them,
	// README.md included.
	files []packageEntry
	// values are the publisher's own values. One with no value is left out.
	values []readmeItem
	// links are the pages the README lists for the network, by name: the
	// vendor's consoles, its reference for what the deploy steps do, and where
	// to get the tool the commands run in.
	links []readmeItem
}

// readmeItem is one line of a README list: a key, drawn in bold so the eye
// finds it first, and what the README says about it. The package's files, the
// publisher's values and the links are all lists of these, drawn by one
// template block.
type readmeItem struct{ Key, Text string }

// readmeCommand is one command as the README draws it. Every command carries
// its number in its section, deploy steps and checks alike: a publisher works
// through both in order, and the README refers to the checks by number.
//
// Answer marks a check. A check's Expect is the answer a working package
// gives, so the README labels it as the one to expect; a deploy step's Expect
// is a note or an instruction, and is drawn as it is.
type readmeCommand struct {
	Number             int
	Label, Run, Expect string
	Answer             bool
}

// readmeData is what the template reads.
type readmeData struct {
	Title, Summary, Note, Domain                string
	SupportEmail, BeforeDeploy, BotVerification string
	Files, Values, Links                        []readmeItem
	Deploy, Checks                              []readmeCommand
}

// renderReadme writes a package's README from the network's edge step, which
// is the step the wizard's last step draws, so the two cannot say different
// things. The README keeps the checks the wizard leaves out: the wizard asks
// the site those questions itself, and the README is read without it.
//
// A network whose edge step is not the package path gets no README. The
// onboarding steps and the switch in EdgePackages.For both say which networks
// get a package, and refusing here keeps the two from disagreeing silently.
func renderReadme(in readmeInput) ([]byte, error) {
	step := setupsteps.EdgeStep(in.network, in.domain)
	if step.Integration != setupsteps.IntegrationEdgePackage {
		return nil, fmt.Errorf("setup: the onboarding steps hand %q no package, so there is no README to write",
			in.network)
	}
	files := make([]readmeItem, 0, len(in.files))
	for _, f := range in.files {
		files = append(files, readmeItem{Key: f.name, Text: f.does})
	}
	values := make([]readmeItem, 0, len(in.values))
	for _, v := range in.values {
		if v.Text != "" {
			values = append(values, v)
		}
	}
	var out bytes.Buffer
	if err := readmeTemplate.Execute(&out, readmeData{
		Title: step.Title, Summary: step.Summary, Note: step.Note, Domain: in.domain,
		SupportEmail: in.supportEmail, BeforeDeploy: in.beforeDeploy, BotVerification: in.botVerification,
		Files: files, Values: values, Links: in.links,
		Deploy: readmeCommands(step.ByHand()), Checks: readmeCommands(step.Checks()),
	}); err != nil {
		return nil, fmt.Errorf("setup: write the README: %w", err)
	}
	return out.Bytes(), nil
}

// readmeCommands draws commands the way the README shows them: numbered from 1
// in their section, and a check's answer marked as the one to expect. Whether
// a command is a check is the step data's own Checked flag, so the label cannot
// disagree with the list the command came from.
func readmeCommands(commands []setupsteps.Command) []readmeCommand {
	out := make([]readmeCommand, 0, len(commands))
	for i, c := range commands {
		out = append(out, readmeCommand{Number: i + 1, Label: c.Label, Run: c.Run, Expect: c.Expect, Answer: c.Checked})
	}
	return out
}
