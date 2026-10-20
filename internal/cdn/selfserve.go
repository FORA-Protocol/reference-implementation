package cdn

import (
	"slices"
	"strings"
)

// SelfServe lists the networks a publisher can set up by themselves, in the
// order the console offers them. It is the one copy of that answer: the
// console reads it to decide whether its wizard goes on, and the Exchange reads
// it to decide which integrations registration admits, so a network cannot be
// offered by one and refused by the other.
//
// Every network on it verifies the delivery address inside an edge function
// the publisher deploys, which is why all of them register with Ed25519.
// CloudFront's own signed-address check is a separate mechanism -- RSA against
// a trusted key group, with no function -- and self-service does not offer it.
//
// Fastly is recognised by the detector and is deliberately NOT on this list.
// There is no Fastly package a publisher could install: the edge build script
// has presets for the Cloudflare and AWS runtimes only, the repository ships no
// Fastly project to build from, and the one entry point that reads a Fastly
// configuration store belongs to the end-to-end harness, which its own manifest
// marks as not for deployment. A site behind Fastly goes to the support path,
// the same as a network nothing recognises.
//
// It returns a fresh slice on every call, so a caller that sorts or appends to
// it cannot change the answer for anybody else.
func SelfServe() []Provider {
	return []Provider{ProviderCloudflare, ProviderCloudFront}
}

// Supported reports whether a publisher can set up a site on this network by
// themselves. The comparison is on the stored token, trimmed and lower-cased,
// because that is the form both a detection and a registration request carry.
func Supported(name string) bool {
	return slices.Contains(SelfServe(), Provider(strings.ToLower(strings.TrimSpace(name))))
}
