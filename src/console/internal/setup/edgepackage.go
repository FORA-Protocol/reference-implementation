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

	// lambdaEntryFile is the module Lambda@Edge loads for the handler string
	// "index.handler". It carries the settings and re-exports the handler from
	// the release bundle, which sits beside it under its release name.
	lambdaEntryFile = "index.mjs"
)

// ErrNoEdgePackages says this deployment ships no edge packages, so a publisher
// cannot be handed one. It is a deployment fault rather than a publisher's, and
// the page says so instead of offering a download that would 404.
var ErrNoEdgePackages = errors.New("setup: this deployment ships no edge packages")

// EdgePackages hands a publisher the package they install, built once per
// release rather than once per publisher.
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
// settings that can be read.
type EdgePackages struct {
	dir string
}

// NewEdgePackages reads the packages from a directory the deployment ships. An
// empty path is a deployment that ships none, which every caller handles.
func NewEdgePackages(dir string) *EdgePackages { return &EdgePackages{dir: dir} }

// Available reports whether a package can be handed over at all.
func (p *EdgePackages) Available() bool { return p != nil && p.dir != "" }

// For builds the package one site's publisher installs on their own CDN.
//
// settings are the same values edge-config.json carries. They travel inside the
// Lambda package because Lambda@Edge has no other way to receive them, and
// alongside the Cloudflare worker because Cloudflare does.
//
// The domain is written into a comment in both packages, so only a domain
// already in the form sitedomain.Normalize produces is accepted. That form has
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
// and a wrangler.toml that already carries this publisher's settings, their
// route and the Workers runtime settings the release was tested against, all
// written by edgerelease.WranglerConfig.
//
// The worker file is the same for every publisher -- its settings arrive as
// variables the platform hands it -- so what makes the package theirs is the
// manifest beside it. Handing over the worker alone would leave them setting
// six variables by hand, which is the work this is supposed to remove.
func (p *EdgePackages) cloudflare(s Site, settings map[string]string) (File, error) {
	worker, err := p.read(releaseCloudflare)
	if err != nil {
		return File{}, err
	}
	// The Worker sits on a route in front of the publisher's own zone, so what
	// it lets through goes on to that zone's origin. It refuses to start
	// without an origin mode, and every request would then fail.
	vars := maps.Clone(settings)
	vars["SAME_ZONE_ORIGIN"] = "true"
	toml, err := edgerelease.WranglerConfig(s.Domain, workerEntry, vars)
	if err != nil {
		return File{}, err
	}
	body, err := zipOf(map[string][]byte{
		workerEntry:     worker,
		"wrangler.toml": toml,
	})
	if err != nil {
		return File{}, err
	}
	return File{
		Name: FileCloudflarePackage, ContentType: "application/zip", Body: body,
		Description: "The Worker and its wrangler.toml, with your settings, your " +
			"route and nothing left to fill in. Unpack it and deploy.",
	}, nil
}

// lambda assembles the zip: the release bundle under its release name, byte
// for byte, and an entry module beside it that carries the publisher's
// settings. Lambda loads index.mjs and calls the handler it re-exports;
// edgerelease.LambdaEntry explains why the settings reach it.
func (p *EdgePackages) lambda(s Site, settings map[string]string) (File, error) {
	bundle, err := p.read(releaseLambda)
	if err != nil {
		return File{}, err
	}
	entry, err := edgerelease.LambdaEntry(s.Domain, releaseLambda, settings)
	if err != nil {
		return File{}, err
	}
	body, err := zipOf(map[string][]byte{
		lambdaEntryFile: entry,
		releaseLambda:   bundle,
	})
	if err != nil {
		return File{}, err
	}
	return File{
		Name: FileLambdaPackage, ContentType: "application/zip", Body: body,
		Description: "The function, with your settings beside it. Lambda@Edge takes " +
			"no environment variables, so they travel in the package.",
	}, nil
}

// zipOf packs the named files, in a stable order so the same answers produce
// the same package.
func zipOf(files map[string][]byte) ([]byte, error) {
	names := slices.Sorted(maps.Keys(files))
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		entry, err := zw.Create(name)
		if err != nil {
			return nil, fmt.Errorf("setup: start %s in the package: %w", name, err)
		}
		if _, err := entry.Write(files[name]); err != nil {
			return nil, fmt.Errorf("setup: write %s into the package: %w", name, err)
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
