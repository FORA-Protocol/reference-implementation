package setup_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"maps"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/setupsteps"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup/setuptest"
)

// The README every package carries. It is read where the console is not open,
// often by somebody the publisher forwarded the zip to, so it has to say
// everything the wizard's last step says about the package, and the checks the
// step asks the site itself.

const readmeDomain = "www.publisher.example"

// packageNetworks are the networks a package is built for, with the files each
// package holds.
var packageNetworks = []struct {
	network string
	files   []string
}{
	{"cloudflare", []string{"README.md", "fora-worker.mjs", "wrangler.toml"}},
	{"cloudfront", []string{"README.md", "index.mjs", "lambda-edge.mjs"}},
}

// readmeSettings are package settings as the builder hands them over.
func readmeSettings() map[string]string {
	return map[string]string{"EXCHANGE_URL": "https://exchange.example", "LICENSED_PATHS_JSON": `["/articles/"]`}
}

// readmePackage builds network's package for readmeDomain on a deployment whose
// support address is email, and returns it with the files it holds.
func readmePackage(t *testing.T, network, email string, settings map[string]string) (setup.File, map[string]string) {
	t.Helper()
	p := setup.NewEdgePackages(setup.Deployment{EdgePackageDir: setuptest.ReleaseDir(t), SupportEmail: email})
	pkg, err := p.For(setup.Site{Domain: readmeDomain, CDN: network}, settings)
	if err != nil {
		t.Fatal(err)
	}
	return pkg, setuptest.Entries(t, pkg.Body)
}

// TestEveryPackageCarriesAReadmeThatNamesItsFiles: the README lists exactly the
// files beside it, each with what it is for and its name in bold, so a
// publisher can tell which file they deploy and which they leave alone. The
// list reader fails on a line without a bold name or without its purpose.
func TestEveryPackageCarriesAReadmeThatNamesItsFiles(t *testing.T) {
	t.Parallel()
	for _, pn := range packageNetworks {
		_, entries := readmePackage(t, pn.network, "", readmeSettings())
		if got := keysOf(entries); !slices.Equal(got, pn.files) {
			t.Fatalf("%s: the package holds %v, want %v", pn.network, got, pn.files)
		}
		listed := setuptest.ReadmeItems(t, entries["README.md"], "## What is in this package")
		if got := slices.Sorted(maps.Keys(listed)); !slices.Equal(got, pn.files) {
			t.Fatalf("%s: the README lists %v, want the package's files %v", pn.network, got, pn.files)
		}
	}
}

// TestTheReadmeIsItsNetworksSteps holds the README to the network's edge step,
// the one the wizard's last step draws: its title and summary, every deploy
// command and then every check, each with its label, command and answer, in
// the data file's order, and the step's note.
func TestTheReadmeIsItsNetworksSteps(t *testing.T) {
	t.Parallel()
	for _, pn := range packageNetworks {
		_, entries := readmePackage(t, pn.network, "", readmeSettings())
		readme := entries["README.md"]
		step := setupsteps.EdgeStep(cdn.Provider(pn.network), readmeDomain)
		want := []string{step.Title, step.Summary, "## Deploy"}
		want = append(want, setuptest.CommandTexts(step.ByHand())...)
		want = append(want, "## Check that it works")
		want = append(want, setuptest.CommandTexts(step.Checks())...)
		if step.Note != "" {
			want = append(want, step.Note)
		}
		setuptest.AssertInOrder(t, pn.network+" README", readme, want)
		if strings.Contains(readme, "<host>") {
			t.Fatalf("%s: a placeholder survived into the README:\n%s", pn.network, readme)
		}
		if len(step.ByHand()) == 0 || len(step.Checks()) == 0 {
			t.Fatalf("%s: the step has no deploy commands or no checks, so this proved nothing", pn.network)
		}
	}
}

// TestEveryCommandIsNumberedInItsSection: the README draws every command from
// one block, and numbers it from 1 in its own section, the deploy steps and the
// checks alike. A publisher works through both in order, and the README refers
// to the checks by number. The README starts with its title, so nothing the
// blocks leave behind comes before it.
func TestEveryCommandIsNumberedInItsSection(t *testing.T) {
	t.Parallel()
	for _, pn := range packageNetworks {
		_, entries := readmePackage(t, pn.network, "", readmeSettings())
		readme := entries["README.md"]
		step := setupsteps.EdgeStep(cdn.Provider(pn.network), readmeDomain)
		if !strings.HasPrefix(readme, "# "+step.Title+"\n") {
			t.Fatalf("%s: the README does not start with its title:\n%s", pn.network, readme)
		}
		if len(step.ByHand()) == 0 || len(step.Checks()) == 0 {
			t.Fatalf("%s: the step has no deploy commands or no checks, so this proved nothing", pn.network)
		}
		for section, commands := range map[string][]setupsteps.Command{
			"## Deploy": step.ByHand(), "## Check that it works": step.Checks(),
		} {
			text := setuptest.ReadmeSection(t, readme, section)
			for i, c := range commands {
				if want := fmt.Sprintf("\n### %d. %s\n", i+1, c.Label); !strings.Contains(text, want) {
					t.Fatalf("%s: %q does not carry %q:\n%s", pn.network, section, want, readme)
				}
			}
		}
		if strings.Contains(readme, "### 0.") {
			t.Fatalf("%s: a command is numbered 0:\n%s", pn.network, readme)
		}
	}
}

// TestEveryCheckSaysWhatToExpect: a check's answer is the result a working
// package gives, so the README labels it "Expected". A deploy step's text is a
// note or an instruction, not an answer, and carries no label.
func TestEveryCheckSaysWhatToExpect(t *testing.T) {
	t.Parallel()
	for _, pn := range packageNetworks {
		_, entries := readmePackage(t, pn.network, "", readmeSettings())
		readme := entries["README.md"]
		step := setupsteps.EdgeStep(cdn.Provider(pn.network), readmeDomain)
		for _, c := range step.Checks() {
			if c.Expect == "" || !strings.Contains(readme, "\n**Expected:** "+c.Expect+"\n") {
				t.Fatalf("%s: %q does not say what to expect:\n%s", pn.network, c.Label, readme)
			}
		}
		for _, c := range step.ByHand() {
			if c.Expect != "" && strings.Contains(readme, "**Expected:** "+c.Expect) {
				t.Fatalf("%s: the deploy step %q is labelled as an answer:\n%s", pn.network, c.Label, readme)
			}
		}
	}
}

// TestTheCheckSectionSaysHowToRunTheChecks: the text in front of the checks
// says how to run them without the console, that the console runs the same
// ones, and that the first check's 403 is the result a working package gives,
// not an error. It also says GPTBot is one AI agent of many and points at
// OpenAI's own list, so a publisher does not read the checks as covering one
// agent only.
func TestTheCheckSectionSaysHowToRunTheChecks(t *testing.T) {
	t.Parallel()
	for _, pn := range packageNetworks {
		_, entries := readmePackage(t, pn.network, "", readmeSettings())
		section := setuptest.ReadmeSection(t, entries["README.md"], "## Check that it works")
		intro, _, _ := strings.Cut(section, "\n### ")
		for _, want := range []string{
			"`your-article-path`", "**Connect your site**", "a 403 is the expected result, not an error",
			"https://developers.openai.com/api/docs/bots",
		} {
			if !strings.Contains(intro, want) {
				t.Fatalf("%s: the text before the checks does not say %q:\n%s", pn.network, want, intro)
			}
		}
	}
}

// TestTheChecksComeInTheOrderTheReadmeNamesThem: the README calls the agent
// check "the first check", and its section on verification refers to checks 1,
// 2 and 3 by what each shows. Those references hold only while the checks come
// in this order: an AI agent at a page, refused with a 403; an ordinary
// browser; an AI agent at the discovery file.
func TestTheChecksComeInTheOrderTheReadmeNamesThem(t *testing.T) {
	t.Parallel()
	const document = "/.well-known/fora.json"
	for _, pn := range packageNetworks {
		checks := setupsteps.EdgeStep(cdn.Provider(pn.network), readmeDomain).Checks()
		if len(checks) < 3 {
			t.Fatalf("%s: %d checks, and the README refers to three: %+v", pn.network, len(checks), checks)
		}
		agentAtPage := strings.Contains(checks[0].Run, `-A "GPTBot`) &&
			!strings.Contains(checks[0].Run, document) && strings.HasPrefix(checks[0].Expect, "403")
		browser := strings.Contains(checks[1].Run, `-A "Mozilla`)
		agentAtDocument := strings.Contains(checks[2].Run, `-A "GPTBot`) && strings.Contains(checks[2].Run, document)
		if !agentAtPage || !browser || !agentAtDocument {
			t.Fatalf("%s: the checks are not in the order the README names them: %+v", pn.network, checks)
		}
	}
}

// TestOnlyTheCloudflareReadmeCreditsCloudflaresBotVerification: the Cloudflare
// README says the package also uses Cloudflare's own verification of AI
// crawlers. That is true because the Cloudflare entry hands the platform's bot
// signal to the app, and the Lambda@Edge entry hands none. The test reads each
// network's entry and holds its README to the entry's wiring: the sentence goes
// when the wiring does, and a network whose entry starts passing a signal has
// to say so. It matches the createApp call itself, so a comment that merely
// names the signal changes nothing.
func TestOnlyTheCloudflareReadmeCreditsCloudflaresBotVerification(t *testing.T) {
	t.Parallel()
	entryFiles := map[string]string{
		"cloudflare": "../../../edge/src/entries/cloudflare.ts",
		"cloudfront": "../../../edge/src/entries/aws-lambda.ts",
	}
	wired := regexp.MustCompile(`createApp\(\{[^}]*botSignal:`)
	for _, pn := range packageNetworks {
		path, ok := entryFiles[pn.network]
		if !ok {
			t.Fatalf("%s: no edge entry is named for this network; name the one its package runs", pn.network)
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		_, files := readmePackage(t, pn.network, "", readmeSettings())
		claims := strings.Contains(files["README.md"], "Cloudflare's own verification")
		if passes := wired.Match(source); claims != passes {
			t.Fatalf("%s: the README credits Cloudflare's verification: %v, but %s passes a bot signal: %v",
				pn.network, claims, path, passes)
		}
	}
}

// TestTheFirstCloudflareStepUnpacksThePackage holds the sentence the Cloudflare
// README puts in front of its deploy commands: a publisher who has already
// unpacked the zip skips the first step. That is right only while the first
// command a person runs is the one that unpacks this package.
func TestTheFirstCloudflareStepUnpacksThePackage(t *testing.T) {
	t.Parallel()
	byHand := setupsteps.EdgeStep(cdn.ProviderCloudflare, readmeDomain).ByHand()
	if len(byHand) == 0 || !strings.HasPrefix(byHand[0].Run, "unzip "+setup.FileCloudflarePackage+" ") {
		t.Fatalf("the first Cloudflare step does not unpack %s: %+v", setup.FileCloudflarePackage, byHand)
	}
	_, entries := readmePackage(t, "cloudflare", "", readmeSettings())
	if !strings.Contains(entries["README.md"], "skip the first step") {
		t.Fatalf("the README no longer tells a publisher who unpacked the zip to skip the first step:\n%s",
			entries["README.md"])
	}
}

// TestTheReadmeSaysWhatVerifiesTheDomain: verification needs the terms, the
// package and the document (ADR-026), and the console verifies a domain only
// when its last step checks the site. That check asks less than "every check
// passes": the package has to refuse an agent and must not refuse a person,
// and agents have to be able to read the document. So the README lists the
// three conditions as bullets, each naming the check that shows it. The
// domain's page offers the package before the terms are set, and Finish setup
// reopens the wizard on the Your terms step while they are not, so the README
// says that too.
func TestTheReadmeSaysWhatVerifiesTheDomain(t *testing.T) {
	t.Parallel()
	for _, pn := range packageNetworks {
		_, entries := readmePackage(t, pn.network, "", readmeSettings())
		verified := setuptest.ReadmeSection(t, entries["README.md"], "## When your domain is verified")
		var bullets []string
		for _, line := range strings.Split(verified, "\n") {
			if strings.HasPrefix(line, "- ") {
				bullets = append(bullets, line)
			}
		}
		conditions := [][]string{
			{"**Your terms**"},
			{"(check 1)", "(check 2)"},
			{"https://" + readmeDomain + "/.well-known/fora.json", "(check 3)"},
		}
		if len(bullets) != len(conditions) {
			t.Fatalf("%s: the section lists %d conditions, want %d:\n%s", pn.network, len(bullets), len(conditions), verified)
		}
		for i, want := range conditions {
			for _, w := range want {
				if !strings.Contains(bullets[i], w) {
					t.Fatalf("%s: condition %d, %q, does not say %q", pn.network, i+1, bullets[i], w)
				}
			}
		}
		for _, want := range []string{"**Finish setup**", "opens the **Your terms** step"} {
			if !strings.Contains(verified, want) {
				t.Fatalf("%s: the README does not say %q:\n%s", pn.network, want, verified)
			}
		}
	}
}

// TestTheReadmeIsWrittenForItsOwnNetwork: a publisher on one network is never
// handed the other network's commands.
func TestTheReadmeIsWrittenForItsOwnNetwork(t *testing.T) {
	t.Parallel()
	for network, foreign := range map[string]string{"cloudflare": "aws lambda", "cloudfront": "wrangler"} {
		_, entries := readmePackage(t, network, "", readmeSettings())
		if strings.Contains(entries["README.md"], foreign) {
			t.Fatalf("%s: the README carries %q:\n%s", network, foreign, entries["README.md"])
		}
	}
}

// TestTheReadmeNamesThePublishersValues: the values the README writes out are
// the ones the files beside it carry, read back from those files, so the
// README cannot name a route or an exchange the package does not use.
func TestTheReadmeNamesThePublishersValues(t *testing.T) {
	t.Parallel()
	_, cf := readmePackage(t, "cloudflare", "", readmeSettings())
	toml := cf["wrangler.toml"]
	for label, pattern := range map[string]string{
		"Worker": `(?m)^name = "([^"]+)"$`, "Route": `pattern = "([^"]+)"`,
		"Zone": `zone_name = "([^"]+)"`, "Exchange": `(?m)^EXCHANGE_URL = "([^"]+)"$`,
	} {
		m := regexp.MustCompile(pattern).FindStringSubmatch(toml)
		if m == nil {
			t.Fatalf("wrangler.toml has no %s to compare with:\n%s", label, toml)
		}
		assertValue(t, "cloudflare", cf["README.md"], label, m[1])
	}
	_, lambda := readmePackage(t, "cloudfront", "", readmeSettings())
	if !strings.Contains(lambda["index.mjs"], `"EXCHANGE_URL": "https://exchange.example"`) {
		t.Fatalf("index.mjs does not carry the exchange the README is compared with:\n%s", lambda["index.mjs"])
	}
	for network, readme := range map[string]string{"cloudflare": cf["README.md"], "cloudfront": lambda["README.md"]} {
		assertValue(t, network, readme, "Domain", readmeDomain)
		assertValue(t, network, readme, "Exchange", "https://exchange.example")
	}
}

// TestAValueThePackageDoesNotCarryIsLeftOut: a package built without an
// exchange setting gets no Exchange line, rather than one naming nothing. The
// list must still be read, with the domain in it, or a missing Exchange line
// would prove nothing.
func TestAValueThePackageDoesNotCarryIsLeftOut(t *testing.T) {
	t.Parallel()
	for _, pn := range packageNetworks {
		_, entries := readmePackage(t, pn.network, "", map[string]string{})
		values := setuptest.ReadmeItems(t, entries["README.md"], "## Your values")
		if exchange, ok := values["Exchange"]; ok {
			t.Fatalf("%s: the README names an exchange the package does not carry, %q:\n%s",
				pn.network, exchange, entries["README.md"])
		}
		if values["Domain"] != readmeDomain {
			t.Fatalf("%s: the README's values do not give the domain, so this read nothing:\n%s",
				pn.network, entries["README.md"])
		}
	}
}

// TestEveryReadmeLinksItsNetworksOwnPages: each README lists its network's own
// pages after the publisher's values and before the deploy commands, so they
// are at hand while deploying. Every link is an absolute https address, none
// is a page of the other network's vendor, and the pages the instructions rely
// on are there: on Cloudflare the dashboard and the reference for the
// wrangler.toml the package holds; on CloudFront the role a Lambda@Edge
// function needs, which the first deploy step asks for.
func TestEveryReadmeLinksItsNetworksOwnPages(t *testing.T) {
	t.Parallel()
	want := map[string]struct {
		foreignHost string
		pages       []string
	}{
		"cloudflare": {"amazon.com", []string{
			"https://dash.cloudflare.com/", "https://developers.cloudflare.com/workers/wrangler/configuration/",
		}},
		"cloudfront": {"cloudflare.com", []string{
			"https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/lambda-edge-permissions.html",
		}},
	}
	for _, pn := range packageNetworks {
		w, ok := want[pn.network]
		if !ok {
			t.Fatalf("%s: no pages are expected for this network; say which", pn.network)
		}
		_, entries := readmePackage(t, pn.network, "", readmeSettings())
		readme := entries["README.md"]
		setuptest.AssertInOrder(t, pn.network+" README", readme,
			[]string{"\n## Your values\n", "\n## Useful links\n", "\n## Deploy\n"})
		links := setuptest.ReadmeItems(t, readme, "## Useful links")
		if len(links) == 0 {
			t.Fatalf("%s: the README links no pages:\n%s", pn.network, readme)
		}
		for name, link := range links {
			u, err := url.Parse(link)
			if err != nil || u.Scheme != "https" || u.Host == "" || strings.ContainsAny(link, " \t") {
				t.Fatalf("%s: %s links %q, which is not an absolute https address", pn.network, name, link)
			}
			if host := u.Hostname(); host == w.foreignHost || strings.HasSuffix(host, "."+w.foreignHost) {
				t.Fatalf("%s: %s links %q, a page of the other network's vendor", pn.network, name, link)
			}
		}
		linked := slices.Collect(maps.Values(links))
		for _, page := range w.pages {
			if !slices.Contains(linked, page) {
				t.Fatalf("%s: the README does not link %s:\n%s", pn.network, page, readme)
			}
		}
	}
}

// TestTheReadmeNeverShowsWhatAPublisherTyped: the licensed paths come from a
// form, and a README rendered as Markdown would show whatever they hold. The
// README writes out none of them; the commands keep their placeholder page.
func TestTheReadmeNeverShowsWhatAPublisherTyped(t *testing.T) {
	t.Parallel()
	hostile := "/paid/\n# Pay us instead\n<img src=x onerror=alert(1)>"
	for _, pn := range packageNetworks {
		_, entries := readmePackage(t, pn.network, "",
			map[string]string{"EXCHANGE_URL": "https://exchange.example", "LICENSED_PATHS_JSON": hostile})
		readme := entries["README.md"]
		for _, leak := range []string{"Pay us instead", "<img", "/paid/"} {
			if strings.Contains(readme, leak) {
				t.Fatalf("%s: the README carries %q from the licensed paths:\n%s", pn.network, leak, readme)
			}
		}
	}
}

// TestTheReadmeSaysWhoToWriteTo: the support address is written in when the
// deployment has one. Without one, the README still sends the publisher back
// to the console rather than ending at nobody.
func TestTheReadmeSaysWhoToWriteTo(t *testing.T) {
	t.Parallel()
	const console = "The console you downloaded this package from checks your site"
	for _, pn := range packageNetworks {
		_, with := readmePackage(t, pn.network, "help@exchange.example", readmeSettings())
		if !strings.Contains(with["README.md"], "Write to us at help@exchange.example") {
			t.Fatalf("%s: the README does not give the support address:\n%s", pn.network, with["README.md"])
		}
		_, without := readmePackage(t, pn.network, "", readmeSettings())
		if strings.Contains(without["README.md"], "Write to us") {
			t.Fatalf("%s: the README asks to write to nobody:\n%s", pn.network, without["README.md"])
		}
		for _, readme := range []string{with["README.md"], without["README.md"]} {
			if !strings.Contains(readme, console) {
				t.Fatalf("%s: the README does not send the publisher back to the console:\n%s", pn.network, readme)
			}
		}
	}
}

// dosEpoch is the earliest time a zip entry can record. An entry written with
// no time, which is how zip.Writer.Create writes one, reads back before it.
var dosEpoch = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

// TestThePackageIsTheSameForTheSameAnswers: the files are built on every
// download, so the same registration has to give the same bytes a week later.
// Two builds in a row cannot show that on their own: a time stamped into the
// entries, from a clock or from a file, is the same in both. So no entry may
// carry a time at all. This says nothing about other Go releases, whose
// compression may write different bytes.
func TestThePackageIsTheSameForTheSameAnswers(t *testing.T) {
	t.Parallel()
	for _, pn := range packageNetworks {
		first, _ := readmePackage(t, pn.network, "help@exchange.example", readmeSettings())
		second, _ := readmePackage(t, pn.network, "help@exchange.example", readmeSettings())
		if !bytes.Equal(first.Body, second.Body) {
			t.Fatalf("%s: two builds of the same site differ", pn.network)
		}
		zr, err := zip.NewReader(bytes.NewReader(first.Body), int64(len(first.Body)))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range zr.File {
			if !f.Modified.Before(dosEpoch) {
				t.Fatalf("%s: %s carries the time %v, so the same site gives other bytes on another day",
					pn.network, f.Name, f.Modified)
			}
		}
	}
}

// TestTheDeployCommandsNameThePackageTheyCameIn: the README's commands unpack
// or upload the very zip the README is in, and the page that offers the zip
// says the instructions travel inside it.
func TestTheDeployCommandsNameThePackageTheyCameIn(t *testing.T) {
	t.Parallel()
	for _, pn := range packageNetworks {
		pkg, entries := readmePackage(t, pn.network, "", readmeSettings())
		if !strings.Contains(setuptest.ReadmeSection(t, entries["README.md"], "## Deploy"), pkg.Name) {
			t.Fatalf("%s: no deploy command names %s:\n%s", pn.network, pkg.Name, entries["README.md"])
		}
		if !strings.Contains(pkg.Description, "README.md") {
			t.Fatalf("%s: the description does not say the instructions are inside: %q", pn.network, pkg.Description)
		}
	}
}

// assertValue fails unless readme's list of values gives label as value.
func assertValue(t *testing.T, network, readme, label, value string) {
	t.Helper()
	if got := setuptest.ReadmeItems(t, readme, "## Your values")[label]; got != value {
		t.Fatalf("%s: the README gives %s as %q, want %q:\n%s", network, label, got, value, readme)
	}
}
