// Package setup builds the files a registered publisher downloads and deploys:
// the discovery document their site serves, a sample of the catalogue feed
// their content produces, the configuration their CDN package needs, and the
// instructions for deploying it.
//
// Everything is generated on request from what the Exchange already holds. The
// console stores no artifacts, builds nothing in the background, and compiles
// nothing per publisher: a person who signs back in a week later gets the same
// files because the registration behind them is the same.
//
// The builders are the shared ones the deployment tooling and the public
// preview page use, so what a publisher downloads here is what they would
// actually serve rather than a second rendering of it.
package setup

import (
	"encoding/json"
	"fmt"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	wkserver "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

// Deployment is what the operator's side contributes to every generated file:
// which Exchange, which contributor, and which release of the edge packages.
type Deployment struct {
	// ExchangeDomain is the seat's hostname, what the manifest names in its
	// exchanges[] entry. The Exchange matches its own domain against this
	// value exactly, so it is the same string the Exchange publishes.
	ExchangeDomain string
	// ExchangeEndpoint is the origin an agent calls.
	ExchangeEndpoint string
	// ContributorHost is the operator's catalog contributor: the party that
	// submits this publisher's catalogue. The manifest authorizes it, and the
	// Exchange refuses to activate a listing whose manifest does not.
	ContributorHost string
	// SupportedProfiles are the extension profiles the Exchange advertises.
	SupportedProfiles []string
	// EdgeReleaseRef names the version of the edge packages the instructions
	// tell a publisher to build from.
	EdgeReleaseRef string
	// EdgePackageDir is where this deployment keeps the edge packages built
	// for its release. Empty ships none, and the publisher gets the written
	// procedure instead of a download.
	EdgePackageDir string
	// ExchangeWBAURL is where the edge package fetches the Exchange's public
	// verification keys. The package holds no secret; this address is how it
	// learns what to check a delivered address against.
	ExchangeWBAURL string
}

// Site is one registered site, as the Exchange reports it.
type Site struct {
	Domain           string
	PublisherID      string
	CDN              string
	ContentPathScope []string
	// Terms is the licensing the publisher agreed. The sample feed is priced
	// from it, and the deployment's currency and unit price it.
	Terms termspreset.Controls
	// SitemapURL is where this site's resources are listed. It travels into the
	// instructions; automatic catalogue import is what reads it.
	SitemapURL string
}

// Manifest builds the discovery document this site must serve at
// /.well-known/fora.json.
//
// Three values in it are what the Exchange checks before it activates the
// listing: the seat's domain, the publisher id it attests for that seat, and
// the operator's catalogue contributor. They come from the registration and
// from the deployment, never from a form, so a publisher cannot serve a
// document that names the wrong payee by typing one.
//
// The document is built by the shared builder, which validates it against the
// protocol's own rules and its JSON schema. A document that would not validate
// is a failure here rather than a file a publisher publishes and then cannot
// get verified.
func Manifest(d Deployment, s Site) ([]byte, error) {
	raw, err := wkserver.BuildPublisher(wkserver.Publisher{
		Domain:            s.Domain,
		ExchangeDomain:    d.ExchangeDomain,
		ExchangeEndpoint:  d.ExchangeEndpoint,
		ResourceOwnerID:   s.PublisherID,
		ContributorHost:   d.ContributorHost,
		SupportedProfiles: d.SupportedProfiles,
	})
	if err != nil {
		return nil, fmt.Errorf("setup: build the discovery document: %w", err)
	}
	return raw, nil
}

// ManifestReadsBack parses a built manifest the way the Exchange's own reader
// does, and reports the two values the domain check compares.
//
// It exists so a test can hold the generator to the reader rather than to a
// second opinion about what the bytes should look like: the document a
// publisher downloads has to satisfy the code that will later fetch it, and
// only that code can say whether it does.
func ManifestReadsBack(raw []byte, exchangeDomain string) (ownerID string, contributors []string, err error) {
	m, err := forawellknown.ParseManifest(raw, forawellknown.RolePublisher)
	if err != nil {
		return "", nil, err
	}
	ownerID, _ = forawellknown.ResourceOwnerID(m, exchangeDomain)
	for _, c := range m.GetCatalogContributors() {
		contributors = append(contributors, c.GetDomain())
	}
	return ownerID, contributors, nil
}

// prettyJSON re-indents a compact document for a file a person will open and
// read. The bytes stay equivalent; only the whitespace differs, and a publisher
// pasting this into their site's static files gets something they can check by
// eye against the instructions.
func prettyJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("setup: re-read the generated document: %w", err)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("setup: format the generated document: %w", err)
	}
	return append(out, '\n'), nil
}
