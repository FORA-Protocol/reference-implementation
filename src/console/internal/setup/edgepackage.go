package setup

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/sitedomain"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/edge/edgerelease"
)

// The file names a publisher downloads, and the names of the release packages
// they are made from.
const (
	FileCloudflarePackage = "fora-cloudflare.zip"
	FileLambdaPackage     = "fora-lambda-edge.zip"

	releaseCloudflare = "cloudflare-worker.mjs"
	releaseLambda     = "lambda-edge.mjs"

	// workerEntry is the file wrangler.toml points at inside the package.
	workerEntry = "fora-worker.mjs"

	// wranglerFile is the project file wrangler reads in the folder it is run
	// in.
	wranglerFile = "wrangler.toml"

	// lambdaEntryFile is the module Lambda@Edge loads for the handler string
	// "index.handler". It carries the settings and re-exports the handler from
	// the release bundle, which sits beside it under its release name.
	lambdaEntryFile = "index.mjs"
)

// readmeNote ends each package's description, so the person who downloads it
// knows the instructions travel inside.
const readmeNote = "The " + readmeFile + " inside has the deploy commands and the checks."

// cloudflareLinks are the pages the Cloudflare README lists before its deploy
// commands: the dashboard the Worker is seen in, and Cloudflare's reference for
// the project file, the route, the proxied DNS records the step asks for and
// the Node.js that wrangler needs.
//
// They are written into the README only. The wizard and the public preview
// show the shared step text as plain text, so a link there would be read as
// Markdown source; a setupsteps test keeps that text free of it.
var cloudflareLinks = []readmeItem{
	{Key: "Cloudflare dashboard", Text: "https://dash.cloudflare.com/"},
	{Key: "Wrangler configuration", Text: "https://developers.cloudflare.com/workers/wrangler/configuration/"},
	{Key: "Workers routes", Text: "https://developers.cloudflare.com/workers/configuration/routing/routes/"},
	{Key: "Proxied DNS records", Text: "https://developers.cloudflare.com/dns/proxy-status/"},
	{Key: "Node.js download", Text: "https://nodejs.org/en/download"},
}

// cloudFrontGuide is the CloudFront Developer Guide, where AWS documents
// Lambda@Edge.
const cloudFrontGuide = "https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/"

// lambdaLinks are the pages the CloudFront README lists, as cloudflareLinks
// are for Cloudflare: the AWS CLI its commands run in, AWS's pages on the role
// a Lambda@Edge function needs and on attaching it to a cache behaviour, and
// the two consoles the distribution and the function are seen in.
var lambdaLinks = []readmeItem{
	{Key: "AWS CLI installation", Text: "https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"},
	{Key: "Lambda@Edge role and permissions", Text: cloudFrontGuide + "lambda-edge-permissions.html"},
	{Key: "Adding a Lambda@Edge trigger", Text: cloudFrontGuide + "lambda-edge-add-triggers-console.html"},
	{Key: "CloudFront console", Text: "https://console.aws.amazon.com/cloudfront/v4/home"},
	{Key: "Lambda console in us-east-1", Text: "https://us-east-1.console.aws.amazon.com/lambda/home?region=us-east-1"},
}

// ErrNoEdgePackages says this deployment ships no edge packages, so a publisher
// cannot be handed one. It is a deployment fault rather than a publisher's, and
// the page says so instead of offering a download that would 404.
var ErrNoEdgePackages = errors.New("setup: this deployment ships no edge packages")

// EdgePackages hands a publisher the package they install, built once per
// release rather than once per publisher, with a README that says how to
// install it and how to check that it works.
//
// Nothing about a publisher is compiled into either package. The Cloudflare
// worker reads its settings from the bindings the platform gives it. The
// Lambda@Edge bundle reads process.env, which Lambda@Edge never populates --
// so the package adds a small entry module that assigns the settings and
// re-exports the handler from the unchanged release bundle beside it.
//
// That is what makes one build enough. Baking the settings through the bundler
// instead would mean a build per publisher: a Node toolchain inside a Go
// service, a bundler running on values somebody typed, and an artifact per
// publisher that nothing can identify afterwards. Here every publisher installs
// the same reviewed bytes, and what differs is a few lines of their own
// settings that can be read, and the README written for their domain.
type EdgePackages struct {
	dir string
	// supportEmail is who each package's README tells a publisher to write
	// to. Empty leaves that line out.
	supportEmail string
}

// NewEdgePackages reads the packages from the directory the deployment ships,
// and takes the address their READMEs give for help from the same deployment.
// An empty directory is a deployment that ships none, which every caller
// handles.
func NewEdgePackages(d Deployment) *EdgePackages {
	return &EdgePackages{dir: d.EdgePackageDir, supportEmail: d.SupportEmail}
}

// Available reports whether a package can be handed over at all.
func (p *EdgePackages) Available() bool { return p != nil && p.dir != "" }

// For builds the package one site's publisher installs on their own CDN.
//
// settings are the same values edge-config.json carries. They travel inside the
// Lambda package because Lambda@Edge has no other way to receive them, and
// alongside the Cloudflare worker because Cloudflare does.
//
// The domain is written into a comment in both packages and into their
// README, so only a domain already in the form sitedomain.Normalize produces
// is accepted. That form has
// no line breaks and nothing outside printable ASCII, so the domain cannot end
// the comment it is written in. Every registered site already has that form,
// so anything else is a caller fault.
func (p *EdgePackages) For(s Site, settings map[string]string) (File, error) {
	if !p.Available() {
		return File{}, ErrNoEdgePackages
	}
	if normalized, err := sitedomain.Normalize(s.Domain); err != nil || normalized != s.Domain {
		return File{}, fmt.Errorf("setup: no package for %q, which is not a normalized domain", s.Domain)
	}
	switch cdn.Provider(s.CDN) {
	case cdn.ProviderCloudflare:
		return p.cloudflare(s, settings)
	case cdn.ProviderCloudFront:
		return p.lambda(s, settings)
	default:
		return File{}, ErrNoEdgePackages
	}
}

// cloudflare assembles a ready wrangler project: the worker as it was built,
// a wrangler.toml that already carries this publisher's settings, their route
// and the Workers runtime settings the release was tested against, all
// written by edgerelease.WranglerConfig, and the README that says how to
// deploy and check it.
//
// The worker file is the same for every publisher -- its settings arrive as
// variables the platform hands it -- so what makes the package theirs is the
// manifest beside it. Handing over the worker alone would leave them setting
// its variables and writing a route by hand, which is the work this is
// supposed to remove.
func (p *EdgePackages) cloudflare(s Site, settings map[string]string) (File, error) {
	worker, err := p.read(releaseCloudflare)
	if err != nil {
		return File{}, err
	}
	// The Worker sits on a route in front of the publisher's own zone, so what
	// it lets through goes on to that zone's origin. It refuses to start
	// without an origin mode, and every request would then fail.
	vars := maps.Clone(settings)
	vars[settingSameZoneOrigin] = "true"
	toml, err := edgerelease.WranglerConfig(s.Domain, workerEntry, vars)
	if err != nil {
		return File{}, err
	}
	route, err := edgerelease.WorkerRoute(s.Domain)
	if err != nil {
		return File{}, err
	}
	body, err := p.pack(readmeInput{
		network: cdn.ProviderCloudflare, domain: s.Domain,
		beforeDeploy: "If you have already unpacked the zip, skip the first step and run the rest " +
			"in the folder that holds this file.",
		botVerification: "It also uses Cloudflare's own verification of AI crawlers when Cloudflare provides it.",
		values: []readmeItem{
			{"Domain", s.Domain},
			{"Exchange", settings[settingExchangeURL]},
			{"Worker", edgerelease.WorkerName(s.Domain)},
			{"Route", route.Pattern},
			{"Zone", route.Zone},
		},
		links: cloudflareLinks,
	}, []packageEntry{
		{name: workerEntry, body: worker, does: "The Worker. Every publisher installs this same file; " +
			"what makes the package yours is " + wranglerFile + "."},
		{name: wranglerFile, body: toml, does: "The project npx wrangler deploy reads: the Worker's name, " +
			"your route and your settings. None of them is a secret."},
		{name: readmeFile, does: "This file."},
	})
	if err != nil {
		return File{}, err
	}
	return File{
		Name: FileCloudflarePackage, ContentType: "application/zip", Body: body,
		Description: "The Worker and its wrangler.toml, with your settings and your route. " +
			"Nothing to fill in. " + readmeNote,
	}, nil
}

// lambda assembles the zip: the release bundle under its release name, byte
// for byte, an entry module beside it that carries the publisher's settings,
// and the README. Lambda loads index.mjs and calls the handler it re-exports;
// edgerelease.LambdaEntry explains why the settings reach it. The zip is
// uploaded as the function's code, README included, and Lambda ignores what
// it does not load.
func (p *EdgePackages) lambda(s Site, settings map[string]string) (File, error) {
	bundle, err := p.read(releaseLambda)
	if err != nil {
		return File{}, err
	}
	entry, err := edgerelease.LambdaEntry(s.Domain, releaseLambda, settings)
	if err != nil {
		return File{}, err
	}
	body, err := p.pack(readmeInput{
		network: cdn.ProviderCloudFront, domain: s.Domain,
		beforeDeploy: "Run these commands in the folder that holds " + FileLambdaPackage + ", and " +
			"upload that zip as it is. A zip made again from its unpacked files can fail when the " +
			"function runs.",
		values: []readmeItem{{"Domain", s.Domain}, {"Exchange", settings[settingExchangeURL]}},
		links:  lambdaLinks,
	}, []packageEntry{
		{name: lambdaEntryFile, body: entry, does: "The module Lambda@Edge loads for the handler " +
			"index.handler. It sets your settings, because Lambda@Edge gives a function no " +
			"environment variables."},
		{name: releaseLambda, body: bundle, does: "The function itself, the same file for every publisher."},
		{name: readmeFile, does: "This file. It is uploaded with the function, which ignores it."},
	})
	if err != nil {
		return File{}, err
	}
	return File{
		Name: FileLambdaPackage, ContentType: "application/zip", Body: body,
		Description: "The function, with your settings beside it. Lambda@Edge takes " +
			"no environment variables, so they travel in the package. " + readmeNote,
	}, nil
}

// packageEntry is one file of a package, and the line its README says about it.
// One list of them is what both the zip and the README's list of files are
// made from, so a file cannot be packed without being described.
type packageEntry struct {
	name string
	does string
	body []byte
}

// pack writes the README into files, whose entry for it has no body yet, and
// packs them all.
func (p *EdgePackages) pack(in readmeInput, files []packageEntry) ([]byte, error) {
	in.supportEmail, in.files = p.supportEmail, files
	readme, err := renderReadme(in)
	if err != nil {
		return nil, err
	}
	packed := slices.Clone(files)
	for i := range packed {
		if packed[i].name == readmeFile {
			packed[i].body = readme
		}
	}
	return zipOf(packed)
}

// zipOf packs the files, sorted by name so the same answers produce the same
// package.
func zipOf(files []packageEntry) ([]byte, error) {
	sorted := slices.SortedFunc(slices.Values(files), func(a, b packageEntry) int {
		return strings.Compare(a.name, b.name)
	})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range sorted {
		entry, err := zw.Create(f.name)
		if err != nil {
			return nil, fmt.Errorf("setup: start %s in the package: %w", f.name, err)
		}
		if _, err := entry.Write(f.body); err != nil {
			return nil, fmt.Errorf("setup: write %s into the package: %w", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("setup: finish the package: %w", err)
	}
	return buf.Bytes(), nil
}

// read loads one release package, or says which one is missing.
//
// Both parts of the path are ours: the directory is operator configuration and
// the name is one of this file's own two constants. Nothing a publisher typed
// reaches it -- their CDN chooses between the two names, and a CDN we ship no
// package for never gets here.
func (p *EdgePackages) read(name string) ([]byte, error) {
	body, err := fs.ReadFile(os.DirFS(p.dir), name)
	if err != nil {
		return nil, fmt.Errorf("setup: read the %s package: %w", name, err)
	}
	return body, nil
}
