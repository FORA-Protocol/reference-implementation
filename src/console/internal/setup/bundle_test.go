package setup_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/catalogfeed"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
)

// The generators are pure functions over a registration, which is one of the
// categories a unit test is for. What they produce is held to the code that
// will later read it -- the manifest to the Exchange's own reader, the feed
// line to the ingest tool's own check -- rather than to a second opinion about
// what the bytes ought to look like.

const (
	testExchangeDomain  = "exchange.example"
	testContributorHost = "catalog-contributor.exchange.example"
	testPublisherID     = "pub_2f1c"
	testSiteDomain      = "stoa.example"
)

func testDeployment() setup.Deployment {
	return setup.Deployment{
		ExchangeDomain:    testExchangeDomain,
		ExchangeEndpoint:  "https://" + testExchangeDomain,
		ContributorHost:   testContributorHost,
		SupportedProfiles: []string{"fora-news-v1"},
		EdgeReleaseRef:    "v1.0.0",
		ExchangeWBAURL:    "https://" + testExchangeDomain + "/.well-known/http-message-signatures-directory",
	}
}

func testSite() setup.Site {
	return setup.Site{
		Domain:           testSiteDomain,
		PublisherID:      testPublisherID,
		CDN:              "cloudflare",
		ContentPathScope: []string{"/articles/"},
		Terms: termspreset.Controls{
			PricingModel:        "per_unit",
			Rate:                "0.02",
			PermittedFunctions:  []string{"search", "ai-input"},
			AttributionRequired: true,
		},
	}
}

// TestManifestSatisfiesTheReaderThatWillFetchIt is the property that decides
// whether a publisher's setup can ever finish: the document they download has
// to pass the check the Exchange runs when it fetches that same document from
// their site.
//
// It is asserted through the Exchange's own reader rather than by comparing
// bytes, because the reader is what the listing's activation depends on.
func TestManifestSatisfiesTheReaderThatWillFetchIt(t *testing.T) {
	raw, err := setup.Manifest(testDeployment(), testSite())
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	owner, contributors, err := setup.ManifestReadsBack(raw, testExchangeDomain)
	if err != nil {
		t.Fatalf("the generated manifest does not read back: %v", err)
	}
	if owner != testPublisherID {
		t.Fatalf("the manifest attests payee %q, want %q", owner, testPublisherID)
	}
	if len(contributors) != 1 || contributors[0] != testContributorHost {
		t.Fatalf("the manifest authorises %v, want [%s]", contributors, testContributorHost)
	}
}

// TestManifestNamesNoOtherExchange checks the attestation is scoped. The payee
// is read from the entry for one exchange, so a manifest that answered the same
// id for any exchange would let a second deployment claim this publisher's
// revenue by asking.
func TestManifestNamesNoOtherExchange(t *testing.T) {
	raw, err := setup.Manifest(testDeployment(), testSite())
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	owner, _, err := setup.ManifestReadsBack(raw, "other-exchange.example")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if owner != "" {
		t.Fatalf("the manifest attests %q to an exchange it does not list", owner)
	}
}

// stubPages stands in for the publisher's own web server.
type stubPages struct {
	site pagefetch.Site
	err  error
}

func (s stubPages) Read(_ context.Context, _ string, _ string) (pagefetch.Site, error) {
	return s.site, s.err
}

// TestBundleProducesTheFourFilesAPublisherDeploys walks the whole bundle and
// holds the feed line to the same check the ingest tool runs.
func TestBundleProducesTheFourFilesAPublisherDeploys(t *testing.T) {
	pages := stubPages{site: pagefetch.Site{
		Page: pagefetch.Page{
			CanonicalURL: "https://" + testSiteDomain + "/articles/on-rivers",
			Title:        "On rivers",
		},
	}}
	b := setup.NewBuilder(testDeployment(), pages, termspreset.Defaults{Currency: "EUR"})
	bundle, err := b.Build(context.Background(), testSite())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(bundle.Warnings) != 0 {
		t.Fatalf("a readable site produced warnings: %v", bundle.Warnings)
	}
	files := map[string][]byte{}
	for _, f := range bundle.Files {
		files[f.Name] = f.Body
		if f.Description == "" {
			t.Fatalf("file %q is offered with no description of what it is for", f.Name)
		}
	}
	for _, want := range []string{setup.FileManifest, setup.FileEdgeConfig} {
		if len(files[want]) == 0 {
			t.Fatalf("the bundle carries no %s", want)
		}
	}
	// The sample is not one of the files: it is what the catalogue will look
	// like, not something to deploy, and offering it beside three files that
	// are work is what made it read as a fourth.
	if bundle.Sample == nil {
		t.Fatal("the bundle carries no sample of the catalogue")
	}
	if files[setup.FileSampleFeed] != nil {
		t.Fatal("the sample is offered among the files to deploy")
	}
	report, err := catalogfeed.Check(strings.NewReader(string(bundle.Sample.Body)))
	if err != nil {
		t.Fatalf("the sample feed does not parse: %v", err)
	}
	if !report.OK() {
		t.Fatalf("the sample feed would be refused: %+v", report.Findings)
	}
}

// TestBundleCarriesNoKeyMaterial pins the promise the console makes to every
// publisher: nothing secret is handed out. The edge package verifies against
// keys it fetches, so a key in these files would be both unnecessary and a
// secret sitting in a browser's downloads folder.
func TestBundleCarriesNoKeyMaterial(t *testing.T) {
	b := setup.NewBuilder(testDeployment(), stubPages{err: errors.New("unreachable")},
		termspreset.Defaults{Currency: "EUR"})
	bundle, err := b.Build(context.Background(), testSite())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, f := range bundle.Files {
		body := string(f.Body)
		for _, marker := range []string{"PRIVATE KEY", "BEGIN EC", "BEGIN RSA", "\"d\":"} {
			if strings.Contains(body, marker) {
				t.Fatalf("%s carries what looks like key material (%q)", f.Name, marker)
			}
		}
	}
}

// TestBundleSurvivesAnUnreachableSite covers the state most publishers are
// actually in when they first register: the site is up but we cannot read it,
// or it is not built yet. The two files that unblock the rest of the setup must
// still be produced, and the person must be told why the third is missing.
func TestBundleSurvivesAnUnreachableSite(t *testing.T) {
	b := setup.NewBuilder(testDeployment(), stubPages{err: errors.New("unreachable")},
		termspreset.Defaults{Currency: "EUR"})
	bundle, err := b.Build(context.Background(), testSite())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(bundle.Warnings) == 0 {
		t.Fatal("an unreadable site produced no warning")
	}
	if bundle.Sample != nil {
		t.Fatal("a sample was produced from a site that could not be read")
	}
	if len(bundle.Files) != 2 {
		t.Fatalf("an unreadable site produced %d files, want the two it deploys", len(bundle.Files))
	}
}

// TestBundleRefusesLicensingItCannotBuild is the negative path on the licensing
// arrangement. Terms that produce nothing must not quietly become free content.
func TestBundleRefusesLicensingItCannotBuild(t *testing.T) {
	site := testSite()
	site.Terms.PermittedFunctions = []string{"telepathy"}
	b := setup.NewBuilder(testDeployment(), stubPages{site: pagefetch.Site{
		Page: pagefetch.Page{CanonicalURL: "https://" + testSiteDomain + "/articles/x"},
	}}, termspreset.Defaults{Currency: "EUR"})
	bundle, err := b.Build(context.Background(), site)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if bundle.Sample != nil {
		t.Fatal("licensing that cannot be built still produced a sample")
	}
	if len(bundle.Warnings) == 0 {
		t.Fatal("licensing that cannot be built produced no warning")
	}
}

// TestEdgeConfigNamesThisSiteAndThisExchange checks the settings a publisher
// builds their package with actually describe their own site. A config that
// named the wrong payee would deploy cleanly and send another publisher's
// content through this one's edge.
func TestEdgeConfigNamesThisSiteAndThisExchange(t *testing.T) {
	b := setup.NewBuilder(testDeployment(), nil, termspreset.Defaults{Currency: "EUR"})
	bundle, err := b.Build(context.Background(), testSite())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var raw []byte
	for _, f := range bundle.Files {
		if f.Name == setup.FileEdgeConfig {
			raw = f.Body
		}
	}
	var cfg struct {
		Provider      string `json:"PROVIDER"`
		ExchangesJSON string `json:"EXCHANGES_JSON"`
		WBAURL        string `json:"EXCHANGE_WBA_URL"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("the edge settings do not parse: %v", err)
	}
	if cfg.Provider != testSiteDomain {
		t.Fatalf("edge settings name provider %q, want %q", cfg.Provider, testSiteDomain)
	}
	if !strings.Contains(cfg.ExchangesJSON, testPublisherID) {
		t.Fatalf("edge settings do not name the payee: %s", cfg.ExchangesJSON)
	}
	if cfg.WBAURL == "" {
		t.Fatal("edge settings name no address for the verification keys")
	}
}
