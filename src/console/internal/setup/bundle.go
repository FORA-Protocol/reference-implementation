package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/catalogfeed"
	wkserver "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/publicerr"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

// File names, which are also the download paths a page links to.
const (
	FileManifest   = "fora.json"
	FileSampleFeed = "sample-feed.jsonl"
	FileEdgeConfig = "edge-config.json"
)

// File is one downloadable artifact.
type File struct {
	Name        string
	ContentType string
	Body        []byte
	// Description is one sentence for the page, saying what the file is for.
	Description string
}

// Bundle is what one site's setup produces: the files a publisher deploys, and
// separately the sample of what their catalogue will look like.
//
// The two are kept apart because they ask different things of the reader. Files
// are work: serve this document, build the package with these settings, follow
// these steps. Sample is not work at all -- the catalogue is imported for the
// publisher, and the sample exists so they can see what will be listed and at
// what price before any of it is. Presented together, the sample reads as a
// fifth thing to deploy.
//
// Warnings are not errors. The sample needs a page from the publisher's own
// site, and a site that is not reachable yet -- being built, behind a staging
// password -- still gets every file it deploys.
type Bundle struct {
	Files []File
	// Sample is nil when the publisher's site could not be read, which
	// Warnings then explains.
	Sample *File
	// SamplePage is the page of the publisher's site the sample was built
	// from, so a preview of the same terms can show the same page without
	// reading the site again. Nil exactly when Sample is.
	SamplePage *pagefetch.Page
	Warnings   []string
}

// PageReader reads one page of a publisher's site, so the sample feed shows
// their own content rather than an invented example. It is the narrow port the
// builder depends on, satisfied by the shared guarded fetcher.
type PageReader interface {
	Read(ctx context.Context, domain, articleURL string) (pagefetch.Site, error)
}

// Builder produces a site's bundle.
type Builder struct {
	deployment Deployment
	pages      PageReader
	// defaults is where the denomination the sample feed is priced in comes
	// from: the Exchange, which stores the terms in it.
	defaults termspreset.Source
	// packages hands over the edge package a publisher installs. Nil on a
	// deployment that ships none, which the page reports rather than offering
	// a download that cannot be served.
	packages *EdgePackages
}

// NewBuilder assembles the builder. pages may be nil on a deployment that does
// not read publisher sites; the sample feed is then reported as unavailable
// rather than silently omitted.
func NewBuilder(d Deployment, pages PageReader, defaults termspreset.Source) *Builder {
	return &Builder{
		deployment: d, pages: pages, defaults: defaults,
		packages: NewEdgePackages(d.EdgePackageDir),
	}
}

// Build produces every file for one site.
//
// It is called on each download rather than stored, which is why the console
// needs no artifact storage, no background jobs and no per-publisher build: the
// registration is the source, and the same registration produces the same
// files a week later.
func (b *Builder) Build(ctx context.Context, s Site) (Bundle, error) {
	manifest, err := Manifest(b.deployment, s)
	if err != nil {
		return Bundle{}, err
	}
	pretty, err := prettyJSON(manifest)
	if err != nil {
		return Bundle{}, err
	}
	edge, err := b.edgeConfig(s)
	if err != nil {
		return Bundle{}, err
	}
	out := Bundle{Files: []File{{
		Name: FileManifest, ContentType: "application/json", Body: pretty,
		Description: "Serve this at " + s.Domain + "/.well-known/fora.json.",
	}}}
	pkg, err := b.edgePackage(s)
	if err != nil {
		return Bundle{}, err
	}
	// The package carries the settings, so handing the settings over as well
	// would be the same values twice and one of them to act on. A deployment
	// that ships no package is the only one where this file is the publisher's
	// only way to get them, and there it is what the written procedure works
	// from.
	if pkg != nil {
		out.Files = append(out.Files, *pkg)
	} else {
		out.Files = append(out.Files, File{
			Name: FileEdgeConfig, ContentType: "application/json", Body: edge,
			Description: "The settings your edge package is built or deployed with.",
		})
	}
	feed, page, warnings := b.sampleFeed(ctx, s)
	out.Warnings = warnings
	if feed != nil {
		out.SamplePage = page
		out.Sample = &File{
			Name: FileSampleFeed, ContentType: "application/jsonl", Body: feed,
			Description: "One entry of your catalogue, as it will be listed: a page of your " +
				"own site, under the licensing you chose.",
		}
	}
	return out, nil
}

// edgePackage builds the package this site's publisher installs, or nothing
// when the deployment ships none.
//
// A deployment without the packages is not an error a publisher caused, and it
// is not a reason to refuse them their discovery document: they get the
// configuration file and the written procedure, which is where every publisher
// was before the packages existed.
func (b *Builder) edgePackage(s Site) (*File, error) {
	if !b.packages.Available() {
		return nil, nil
	}
	settings, err := b.edgeSettings(s)
	if err != nil {
		return nil, err
	}
	pkg, err := b.packages.For(s, settings)
	if errors.Is(err, ErrNoEdgePackages) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pkg, nil
}

// edgeConfigBody is what the edge packages read. Every runtime takes the same
// values; how they arrive differs -- Cloudflare binds them as variables, Fastly
// as a configuration store, and Lambda@Edge, which takes no environment, gets
// them from the index.mjs the package places beside the unchanged bundle.
type edgeConfigBody struct {
	ExchangeURL     string          `json:"EXCHANGE_URL"`
	ExchangeWBAURL  string          `json:"EXCHANGE_WBA_URL"`
	Provider        string          `json:"PROVIDER"`
	ExchangesJSON   string          `json:"EXCHANGES_JSON"`
	ContributorJSON string          `json:"CATALOG_CONTRIBUTORS_JSON"`
	EnforceBinding  string          `json:"FORA_ENFORCE_BINDING"`
	LicensedPaths   []string        `json:"licensed_paths"`
	Notes           edgeConfigNotes `json:"notes"`
}

// edgeConfigNotes carry what a person needs to know to use the file and cannot
// read off the values.
type edgeConfigNotes struct {
	Release string `json:"edge_release"`
	Keys    string `json:"keys"`
}

// edgeConfig renders the settings this site's edge package needs.
//
// No key material is in it, and that is the design rather than an omission: the
// package verifies a delivered address against the Exchange's public keys,
// which it fetches from the address named here. Nothing secret ever reaches a
// publisher, and a key rotation needs no redeploy on their side.
func (b *Builder) edgeConfig(s Site) ([]byte, error) {
	exchanges, err := json.Marshal([]map[string]any{{
		"domain":             b.deployment.ExchangeDomain,
		"endpoint":           b.deployment.ExchangeEndpoint,
		"relationship":       "PROVIDER_RELATIONSHIP_DIRECT",
		"ext":                map[string]string{"resource_owner_id": s.PublisherID},
		"supported_profiles": b.deployment.SupportedProfiles,
	}})
	if err != nil {
		return nil, fmt.Errorf("setup: render the exchange settings: %w", err)
	}
	contributors, err := json.Marshal([]map[string]string{{
		"domain": b.deployment.ContributorHost, "relationship": wkserver.OperatorRelationship,
	}})
	if err != nil {
		return nil, fmt.Errorf("setup: render the contributor settings: %w", err)
	}
	body := edgeConfigBody{
		ExchangeURL:     b.deployment.ExchangeEndpoint,
		ExchangeWBAURL:  b.deployment.ExchangeWBAURL,
		Provider:        s.Domain,
		ExchangesJSON:   string(exchanges),
		ContributorJSON: string(contributors),
		EnforceBinding:  "true",
		LicensedPaths:   s.ContentPathScope,
		Notes: edgeConfigNotes{
			Release: b.deployment.EdgeReleaseRef,
			Keys: "The package holds no secret. It fetches the Exchange's public " +
				"verification keys from EXCHANGE_WBA_URL, so a key rotation needs " +
				"no change here.",
		},
	}
	out, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("setup: render the edge settings: %w", err)
	}
	return append(out, '\n'), nil
}

// edgeSettings is the same values in the shape the runtimes actually take:
// flat, all strings, the names the worker's own schema reads. The
// configuration file shows them to a person; this is what goes into the
// package. Both come from edgeConfig, so the file a publisher reads and the
// settings their function runs on cannot say different things.
func (b *Builder) edgeSettings(s Site) (map[string]string, error) {
	raw, err := b.edgeConfig(s)
	if err != nil {
		return nil, err
	}
	var body edgeConfigBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("setup: read back the edge settings: %w", err)
	}
	settings := map[string]string{
		"EXCHANGE_URL":              body.ExchangeURL,
		"EXCHANGE_WBA_URL":          body.ExchangeWBAURL,
		"PROVIDER":                  body.Provider,
		"EXCHANGES_JSON":            body.ExchangesJSON,
		"CATALOG_CONTRIBUTORS_JSON": body.ContributorJSON,
		"FORA_ENFORCE_BINDING":      body.EnforceBinding,
	}
	// The paths the publisher licenses are the only ones the edge sends AI bots
	// to negotiate for; the rest of the site passes as it did before. The route
	// still covers the whole hostname, because the discovery documents are
	// served there. No scope is the whole site, which is the edge's own default,
	// so the key is written only when there is a scope to carry.
	if len(body.LicensedPaths) > 0 {
		scope, err := json.Marshal(body.LicensedPaths)
		if err != nil {
			return nil, fmt.Errorf("setup: render the licensed paths: %w", err)
		}
		settings["LICENSED_PATHS_JSON"] = string(scope)
	}
	return settings, nil
}

// sampleFeed builds one line of the publisher's catalogue from a page of their
// own site and the licensing preset they chose.
//
// It returns warnings rather than failing the whole bundle, because the other
// three files do not depend on the site being reachable and a publisher who
// cannot be read yet still needs them.
func (b *Builder) sampleFeed(ctx context.Context, s Site) ([]byte, *pagefetch.Page, []string) {
	if b.pages == nil {
		return nil, nil, []string{"a sample feed is not available on this deployment"}
	}
	denomination, err := b.defaults.TermsDefaults(ctx)
	if err != nil {
		return nil, nil, []string{"a sample feed is not available right now; the other files do not depend on it"}
	}
	terms, err := termspreset.FromControls(s.Terms, denomination)
	if err != nil {
		return nil, nil, []string{licensingRefusal(err)}
	}
	site, err := b.pages.Read(ctx, s.Domain, "")
	if err != nil {
		return nil, nil, []string{
			"we could not read a page of " + s.Domain + " to build a sample feed. " +
				"The other files do not depend on it.",
		}
	}
	line, warnings, err := catalogfeed.SampleForPage(
		s.Domain, site.Page.CanonicalURL, site.Page.Title, terms)
	if err != nil {
		// The feed builder's error names its own fields and stages; the
		// publisher is told which file is missing, not how it failed.
		return nil, nil, []string{"a sample feed line could not be built from the licensing you chose"}
	}
	return line, &site.Page, warnings
}

// licensingRefusal is the warning for licensing that cannot be rendered. It
// carries the shared package's public sentence, which names the control to
// change, and never the error's own text, which is for the log.
func licensingRefusal(err error) string {
	const lead = "the licensing you chose could not be rendered"
	if msg, ok := publicerr.Message(err); ok {
		return lead + ": " + msg
	}
	return lead
}
