package setupsteps_test

import (
	"encoding/json"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/setupsteps"
)

var allProviders = []cdn.Provider{
	cdn.ProviderCloudFront,
	cdn.ProviderCloudflare,
	cdn.ProviderFastly,
	cdn.ProviderAkamai,
	cdn.ProviderNone,
}

// Every publisher sees the same four stages in the same order. A publisher on
// no supported network gets a different third step, not a shorter path.
func TestEveryProviderGetsTheWholePath(t *testing.T) {
	t.Parallel()
	want := []string{"register", "manifest", "edge", "terms"}
	for _, provider := range allProviders {
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()
			got := setupsteps.For(provider, "publisher.example", nil)
			if len(got) != len(want) {
				t.Fatalf("steps = %d, want %d", len(got), len(want))
			}
			for i, id := range want {
				if got[i].ID != id {
					t.Fatalf("step %d is %q, want %q", i, got[i].ID, id)
				}
				if got[i].Title == "" || got[i].Summary == "" {
					t.Fatalf("step %q has no title or summary: %+v", id, got[i])
				}
			}
		})
	}
}

// The edge step is the one that depends on the network, and it must name the
// network the publisher actually runs.
func TestEdgeStepNamesTheDetectedNetwork(t *testing.T) {
	t.Parallel()
	cases := map[cdn.Provider]string{
		cdn.ProviderCloudFront: "CloudFront",
		cdn.ProviderCloudflare: "Cloudflare",
	}
	for provider, name := range cases {
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()
			step := edgeStep(t, setupsteps.For(provider, "publisher.example", nil))
			if step.Integration != "edge_package" {
				t.Fatalf("integration = %q, want the edge package path", step.Integration)
			}
			if !strings.Contains(step.Title, name) {
				t.Fatalf("title %q does not name %s", step.Title, name)
			}
			if len(step.Commands) == 0 {
				t.Fatal("the edge step carries no commands")
			}
			for _, c := range step.Commands {
				if c.Label == "" || (c.Run == "" && c.Expect == "") {
					t.Fatalf("a command is incomplete: %+v", c)
				}
			}
		})
	}
}

// The data file is the one place that says which networks have a package, so
// every variant has to declare its path, a variant with commands to run has to
// be a package, and every recognised network has to have a variant of its own
// rather than falling through to the "we could not tell" text.
func TestEveryNetworkDeclaresItsPath(t *testing.T) {
	t.Parallel()
	none := edgeStep(t, setupsteps.For(cdn.ProviderNone, "publisher.example", nil))
	for _, provider := range allProviders {
		path := setupsteps.For(provider, "publisher.example", nil)
		step := edgeStep(t, path)
		switch step.Integration {
		case setupsteps.IntegrationEdgePackage:
			if len(step.Commands) == 0 {
				t.Fatalf("%s: a package with nothing to run", provider)
			}
		case setupsteps.IntegrationContactUs:
			if len(step.Commands) != 0 {
				t.Fatalf("%s: a conversation with commands to run: %+v", provider, step.Commands)
			}
		default:
			t.Fatalf("%s: integration = %q, want edge_package or contact_us", provider, step.Integration)
		}
		if got := setupsteps.Integration(path); got != step.Integration {
			t.Fatalf("%s: Integration() = %q, want the edge step's %q", provider, got, step.Integration)
		}
		if provider != cdn.ProviderNone && step.Title == none.Title {
			t.Fatalf("%s has no variant of its own and fell through to the unrecognised-network text", provider)
		}
	}
}

// A site we could not identify routes to a conversation rather than to a
// package.
func TestNoNetworkRoutesToContactUs(t *testing.T) {
	t.Parallel()
	step := edgeStep(t, setupsteps.For(cdn.ProviderNone, "publisher.example", nil))
	if step.Integration != "contact_us" {
		t.Fatalf("integration = %q, want contact_us", step.Integration)
	}
	if len(step.Commands) != 0 {
		t.Fatalf("commands = %+v, want none: there is nothing to deploy yet", step.Commands)
	}
}

// A recognised network with no package is named, so the publisher reads that we
// know what serves their site, and is routed to the conversation with nothing
// to deploy. Fastly is one: the build script has presets for the Cloudflare and
// AWS runtimes only, so handing a Fastly publisher a deployment step would name
// a package that does not exist.
func TestARecognisedUnsupportedNetworkIsNamedAndRoutedToContactUs(t *testing.T) {
	t.Parallel()
	cases := map[cdn.Provider]string{
		cdn.ProviderFastly: "Fastly",
		cdn.ProviderAkamai: "Akamai",
	}
	for provider, name := range cases {
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()
			step := edgeStep(t, setupsteps.For(provider, "publisher.example", nil))
			if step.Integration != "contact_us" {
				t.Fatalf("integration = %q, want contact_us", step.Integration)
			}
			if !strings.Contains(step.Title, name) {
				t.Fatalf("title %q does not name %s", step.Title, name)
			}
			if len(step.Commands) != 0 {
				t.Fatalf("commands = %+v, want none: there is no package to deploy", step.Commands)
			}
		})
	}
}

// A network the data file does not describe falls back to the conversation
// rather than to an empty step.
func TestUnknownNetworkFallsBackToContactUs(t *testing.T) {
	t.Parallel()
	step := edgeStep(t, setupsteps.For(cdn.Provider("someothercdn"), "publisher.example", nil))
	if step.Integration != "contact_us" {
		t.Fatalf("integration = %q, want contact_us", step.Integration)
	}
}

// A publisher reads commands they can run, not commands they have to edit.
func TestHostIsSubstitutedEverywhere(t *testing.T) {
	t.Parallel()
	for _, provider := range allProviders {
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()
			for _, step := range setupsteps.For(provider, "news.publisher.example", nil) {
				assertNoPlaceholder(t, step.Title)
				assertNoPlaceholder(t, step.Summary)
				assertNoPlaceholder(t, step.Note)
				for _, c := range step.Commands {
					assertNoPlaceholder(t, c.Run)
					assertNoPlaceholder(t, c.Expect)
				}
			}
		})
	}
}

// At least one command has to actually carry the host, or the substitution is
// passing because there was nothing to substitute.
func TestHostReachesTheCommands(t *testing.T) {
	t.Parallel()
	found := false
	for _, step := range setupsteps.For(cdn.ProviderCloudflare, "news.publisher.example", nil) {
		for _, c := range step.Commands {
			if strings.Contains(c.Run, "news.publisher.example") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no command names the publisher's host")
	}
}

// The manifest is attached to the step that asks the publisher to serve it, and
// to no other.
func TestManifestRidesOnTheManifestStep(t *testing.T) {
	t.Parallel()
	manifest := json.RawMessage(`{"ver":"1.0","role":"ROLE_PUBLISHER"}`)
	for _, step := range setupsteps.For(cdn.ProviderCloudflare, "publisher.example", manifest) {
		switch step.ID {
		case "manifest":
			if string(step.Manifest) != string(manifest) {
				t.Fatalf("manifest = %s, want the document that was passed in", step.Manifest)
			}
		default:
			if step.Manifest != nil {
				t.Fatalf("step %q carries a manifest it should not: %s", step.ID, step.Manifest)
			}
		}
	}
}

// A preview that could not build a manifest still shows the path; the step just
// has no document attached.
func TestStepsWithoutAManifest(t *testing.T) {
	t.Parallel()
	for _, step := range setupsteps.For(cdn.ProviderCloudflare, "publisher.example", nil) {
		if step.Manifest != nil {
			t.Fatalf("step %q carries a manifest: %s", step.ID, step.Manifest)
		}
	}
}

// An empty host leaves the text alone rather than producing commands addressed
// to nothing. Substituting an empty string would strip the placeholder and
// leave `curl https:///.well-known/fora.json`, which reads as a command a
// publisher can run and is not one — so the placeholder surviving is the
// property, not the step count.
func TestEmptyHostLeavesTheTextIntact(t *testing.T) {
	t.Parallel()
	got := setupsteps.For(cdn.ProviderCloudflare, "", nil)
	if len(got) != 4 {
		t.Fatalf("steps = %d, want the whole path", len(got))
	}
	found := false
	for _, step := range got {
		for _, c := range step.Commands {
			if strings.Contains(c.Run, "<host>") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no command still carries the placeholder; an empty host was substituted in")
	}
}

func edgeStep(t *testing.T, path []setupsteps.Step) setupsteps.Step {
	t.Helper()
	for _, step := range path {
		if step.ID == "edge" {
			return step
		}
	}
	t.Fatal("the path has no edge step")
	return setupsteps.Step{}
}

func assertNoPlaceholder(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "<host>") {
		t.Fatalf("a placeholder survived into the text shown to a publisher: %q", text)
	}
}

// A command naming one of the files the console hands over has to name one
// that exists.
//
// This replaces a guard on a step that used to tell publishers to run a build
// script from this repository, which is work they no longer do: the packages
// are built once per release and handed over ready. What the instructions name
// now is those downloads, and the way they go wrong is the same way the script
// argument did -- a rename on one side, an instruction on the other still
// naming what used to be there, and a publisher following a step that points
// at nothing.
//
// The names come from the setup package, which is what produces the files, so
// this fails the moment a download is renamed without its instruction.
func TestCommandsNameFilesTheConsoleHandsOver(t *testing.T) {
	t.Parallel()
	handedOver := map[string]bool{
		"fora.json":            true,
		"edge-config.json":     true,
		"sample-feed.jsonl":    true,
		"fora-worker.mjs":      true,
		"fora-cloudflare.zip":  true,
		"fora-lambda-edge.zip": true,
	}
	named := 0
	for _, provider := range allProviders {
		for _, step := range setupsteps.For(provider, "publisher.example", nil) {
			for _, c := range step.Commands {
				for _, word := range fileNames(c.Run + " " + c.Expect) {
					named++
					if !handedOver[word] {
						t.Fatalf("step %q names %q, which the console does not hand over; "+
							"either the file was renamed or the instruction was not",
							step.ID, word)
					}
				}
			}
		}
	}
	if named == 0 {
		t.Fatal("no instruction names a file the console hands over, so this proved nothing")
	}
}

// fileNames picks out the words that look like one of our artifacts: a bare
// name ending in an extension we produce. It deliberately ignores paths and
// URLs, which are the publisher's own.
func fileNames(text string) []string {
	var out []string
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '"' || r == '\''
	}) {
		field = strings.TrimPrefix(field, "fileb://")
		// Trailing punctuation first. Skipping any word with a colon was meant
		// to ignore URLs, and it also ignored every name that ended a clause --
		// which is where the one wrong name in these instructions sat.
		field = strings.TrimRight(field, ".,;:)")
		if strings.Contains(field, "/") || strings.Contains(field, "://") {
			continue
		}
		if strings.HasSuffix(field, ".json") || strings.HasSuffix(field, ".mjs") ||
			strings.HasSuffix(field, ".zip") || strings.HasSuffix(field, ".jsonl") {
			out = append(out, field)
		}
	}
	return out
}

// RunnableByHand drops the commands a caller answers by asking the site, and
// keeps everything that needs a person. The step itself survives either way:
// its title, summary and note describe work that happened, and a note is where
// an integration says its written procedure is not finished yet.
func TestRunnableByHandKeepsOnlyTheWorkAPersonDoes(t *testing.T) {
	t.Parallel()
	for _, provider := range allProviders {
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()
			full := setupsteps.For(provider, "publisher.example", nil)
			trimmed := setupsteps.RunnableByHand(full)
			if len(trimmed) != len(full) {
				t.Fatalf("steps = %d after trimming, want %d", len(trimmed), len(full))
			}
			for i, step := range trimmed {
				if step.Title != full[i].Title || step.Summary != full[i].Summary ||
					step.Note != full[i].Note {
					t.Fatalf("step %q lost its text: %+v", step.ID, step)
				}
				for _, c := range step.Commands {
					if c.Checked {
						t.Fatalf("step %q kept a command the caller answers: %q",
							step.ID, c.Label)
					}
				}
			}
		})
	}
}

// Every command marked as answerable from outside is a request to a public
// address. That is what makes it answerable: a caller with no access to the
// publisher's CDN account can still make it.
//
// The guard is the point. Marking "terraform apply" or "fastly compute publish"
// would make the console drop a deployment step from the instructions and
// report nothing in its place, leaving a publisher with no way to learn the
// work exists.
func TestOnlyPublicRequestsAreMarkedAnswerable(t *testing.T) {
	t.Parallel()
	marked := 0
	for _, provider := range allProviders {
		for _, step := range setupsteps.For(provider, "publisher.example", nil) {
			for _, c := range step.Commands {
				if !c.Checked {
					continue
				}
				marked++
				if !strings.HasPrefix(strings.TrimSpace(c.Run), "curl ") {
					t.Fatalf("step %q marks %q answerable, but it runs %q",
						step.ID, c.Label, c.Run)
				}
			}
		}
	}
	if marked == 0 {
		t.Fatal("no command is marked answerable, so the console has nothing to take over")
	}
}
