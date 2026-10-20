// Package edgerelease is what the edge release says about how it is installed:
// the Workers runtime settings it was built and tested under, the name and
// project file of a Cloudflare Worker, and the index.mjs that carries a
// Lambda@Edge deployment's settings.
//
// It sits in the edge tree because the edge owns those facts. The files it
// embeds are read as they are by the edge's own tooling: workers-compat.json by
// the test configuration and the local Miniflare server, lambda-entry.mjs.tmpl
// by scripts/lambda-entry.mjs, which builds a deployment's own Lambda package.
// So the console, which assembles a publisher's download, and the edge build,
// which assembles a deployment's, write the same files from the same source.
package edgerelease

import (
	"bytes"
	"crypto/sha256"
	_ "embed" // the release settings and the Lambda entry template
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/publicsuffix"
)

//go:embed workers-compat.json
var workersCompatJSON []byte

//go:embed lambda-entry.mjs.tmpl
var lambdaEntryTemplate string

// WorkersCompat is the Workers runtime behaviour the release is built and
// tested against. It is a release property, not a publisher's choice, which
// is why it is written into a package rather than asked for.
type WorkersCompat struct {
	Date  string   `json:"compatibility_date"`
	Flags []string `json:"compatibility_flags"`
}

// Compat reads the release's Workers runtime settings.
func Compat() (WorkersCompat, error) {
	var c WorkersCompat
	if err := json.Unmarshal(workersCompatJSON, &c); err != nil {
		return WorkersCompat{}, fmt.Errorf("edgerelease: read workers-compat.json: %w", err)
	}
	if c.Date == "" {
		return WorkersCompat{}, fmt.Errorf("edgerelease: workers-compat.json names no compatibility date")
	}
	return c, nil
}

// maxWorkerName is the longest name Cloudflare accepts for a Worker.
const maxWorkerName = 63

// workerNameHashLen is how many hex digits of the domain's digest end the name.
const workerNameHashLen = 8

var notWorkerNameChar = regexp.MustCompile(`[^a-z0-9-]+`)

// WorkerName is the Worker a site's package deploys as.
//
// One Cloudflare account can hold several of a publisher's sites, and a deploy
// replaces the Worker of the same name. So each domain needs its own name, and
// the same domain must always get the same one, or a redeploy would leave the
// old Worker running beside the new one.
//
// A Worker name is lower-case letters, digits and dashes, at most 63
// characters. The readable part is the domain with its dots turned into dashes,
// cut to fit. It ends in a digest of the whole domain, because that mapping is
// not one-to-one: a-b.example and a.b-example read the same, and two long
// domains can share their first 49 characters.
func WorkerName(domain string) string {
	sum := sha256.Sum256([]byte(domain))
	suffix := hex.EncodeToString(sum[:])[:workerNameHashLen]
	const prefix = "fora-"
	room := maxWorkerName - len(prefix) - 1 - len(suffix)

	slug := notWorkerNameChar.ReplaceAllString(strings.ToLower(domain), "-")
	if len(slug) > room {
		slug = slug[:room]
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return prefix + suffix
	}
	return prefix + slug + "-" + suffix
}

// WranglerConfig writes the wrangler.toml of a site's Cloudflare package.
//
// main is the worker file beside it. The route covers the whole hostname on
// purpose. The Worker serves the discovery document itself, so a route over the
// article paths alone would leave /.well-known answered by the origin. Which
// paths the Worker gates is a setting (LICENSED_PATHS_JSON), not the route.
func WranglerConfig(domain, main string, settings map[string]string) ([]byte, error) {
	zone, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return nil, fmt.Errorf("edgerelease: work out the zone for %q: %w", domain, err)
	}
	compat, err := Compat()
	if err != nil {
		return nil, err
	}
	flags, err := json.Marshal(compat.Flags)
	if err != nil {
		return nil, fmt.Errorf("edgerelease: render the compatibility flags: %w", err)
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, `# The project for %s, ready to deploy. Generated for this domain; the
# worker file beside it is the release every publisher installs.
name = %s
main = %s
compatibility_date = %s
compatibility_flags = %s

# The whole hostname, because the Worker serves your discovery document at
# /.well-known/fora.json as well as guarding your articles.
routes = [
  { pattern = %s, zone_name = %s }
]

# Your settings. None of them is a secret: the Worker verifies a delivered
# address against the exchange's public keys, which it fetches for itself.
[vars]
`, domain, tomlString(WorkerName(domain)), tomlString(main), tomlString(compat.Date), flags,
		tomlString(domain+"/*"), tomlString(zone))

	for _, k := range slices.Sorted(maps.Keys(settings)) {
		fmt.Fprintf(&out, "%s = %s\n", k, tomlString(settings[k]))
	}
	return out.Bytes(), nil
}

// tomlString renders a value as a TOML basic string.
//
// JSON's string encoding is TOML's: the same escapes, the same quoting. Using
// it means a publisher's own domain reaches the file as a value and cannot
// become part of the configuration around it. HTML escaping is off because a
// TOML file is not HTML, so a path like /q&a/ reads as the publisher typed it.
func tomlString(v string) string {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return `""`
	}
	return strings.TrimSuffix(out.String(), "\n")
}

var bareToken = regexp.MustCompile(`^[\x21-\x7e]+$`)

// LambdaEntry renders the index.mjs that sits beside the Lambda@Edge release
// bundle: the settings assignment, then a re-export of the handler from bundle.
//
// The re-export is static, and a module's static imports and exports are
// evaluated before its body runs. So the bundle is loaded BEFORE the
// assignment. That is safe only because the bundle reads process.env on its
// first invocation and not at load: getHandler in src/entries/aws-lambda.ts
// parses the environment the first time the handler is called. If the bundle
// ever read its settings at load, this entry would hand it an empty
// environment; tests/node/release-package.test.ts runs testdata/lambda-index.mjs.golden
// against a real release bundle so that change fails there.
//
// scripts/lambda-entry.mjs renders the same template for a deployment's own
// build, and both are held to testdata/lambda-index.mjs.golden, so the two cannot
// drift apart. The settings are JSON with sorted keys, two-space indent and no
// HTML escaping, which is what JSON.stringify writes on the edge side. The
// domain goes into a comment, so it must be one printable token with no line
// break that could end the comment.
//
// The template is filled in one pass, so a value that happens to contain a
// placeholder is written as it is and not filled in again.
func LambdaEntry(domain, bundle string, settings map[string]string) ([]byte, error) {
	if !bareToken.MatchString(domain) {
		return nil, fmt.Errorf("edgerelease: the domain %q is not a bare hostname", domain)
	}
	if settings == nil {
		// No settings is an empty object on both sides, never null.
		settings = map[string]string{}
	}
	var encoded bytes.Buffer
	enc := json.NewEncoder(&encoded)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	// encoding/json writes map keys in sorted order, so the same settings
	// always produce the same file.
	if err := enc.Encode(settings); err != nil {
		return nil, fmt.Errorf("edgerelease: render the package settings: %w", err)
	}
	return []byte(strings.NewReplacer(
		"{{DOMAIN}}", domain,
		"{{BUNDLE}}", bundle,
		"{{SETTINGS}}", strings.TrimSuffix(encoded.String(), "\n"),
	).Replace(lambdaEntryTemplate)), nil
}
