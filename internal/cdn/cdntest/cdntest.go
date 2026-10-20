// Package cdntest provides a DNS fixture for tests of CDN detection.
//
// It is the seam the detector's Resolver port exists for: a test states what
// DNS says about a name and gets that answer back, with no network and no
// waiting.
package cdntest

import (
	"context"
	"net"
	"strings"
)

// Fixture answers DNS from maps. A name that is not in a map is answered the
// way a real resolver answers an unknown name, so a test that forgets an entry
// sees the same behaviour production would.
type Fixture struct {
	// CNAME maps a host to its delegation target.
	CNAME map[string]string
	// NS maps a zone to its name servers.
	NS map[string][]string
	// Err, when set for a name, is returned instead of an answer. It is how a
	// test stages a resolver failure.
	Err map[string]error
}

// LookupCNAME implements the detector's resolver port.
func (f Fixture) LookupCNAME(_ context.Context, host string) (string, error) {
	key := fold(host)
	if err, ok := f.Err[key]; ok {
		return "", err
	}
	target, ok := f.CNAME[key]
	if !ok {
		return "", notFound(host)
	}
	return target, nil
}

// LookupNS implements the detector's resolver port.
func (f Fixture) LookupNS(_ context.Context, name string) ([]*net.NS, error) {
	key := fold(name)
	if err, ok := f.Err[key]; ok {
		return nil, err
	}
	hosts, ok := f.NS[key]
	if !ok {
		return nil, notFound(name)
	}
	out := make([]*net.NS, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, &net.NS{Host: h})
	}
	return out, nil
}

func fold(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}
