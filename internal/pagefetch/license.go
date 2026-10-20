package pagefetch

import (
	"context"
	"errors"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/publicerr"
)

// LicenseDocument fetches the site's licensing document, if it publishes one.
//
// The conventional address is tried first, then the pointer robots.txt may
// carry for a document kept elsewhere. A pointer is only followed when it names
// the same site: a licensing document hosted on another host would be another
// party's statement about this publisher's content.
func (f *Fetcher) LicenseDocument(ctx context.Context, domain string, site *Site) ([]byte, string, error) {
	base := f.limits.scheme() + "://" + domain
	if body, address, err := f.licenseAt(ctx, base+"/rsl.txt"); err != nil {
		return nil, "", err
	} else if body != nil {
		return body, address, nil
	}
	if !site.RobotsFetched {
		if robots, ok := f.robots(ctx, base); ok {
			site.Robots = robots
			site.RobotsFetched = true
		}
	}
	for _, pointer := range site.Robots.Licenses {
		target, warning, ok := f.readable(pointer, domain, "a licensing document")
		if !ok {
			site.Warnings = append(site.Warnings, warning)
			continue
		}
		if body, address, err := f.licenseAt(ctx, target); err != nil {
			return nil, "", err
		} else if body != nil {
			return body, address, nil
		}
	}
	return nil, "", nil
}

// licenseAt fetches one candidate address. A document that is not there is not
// an error — most sites publish none — but a document that is there and cannot
// be read is, because silently ignoring an oversized licensing document would
// show the publisher terms they did not choose.
func (f *Fetcher) licenseAt(ctx context.Context, address string) ([]byte, string, error) {
	result, err := f.getWithinSite(ctx, address, f.limits.docBytes(), "application/xml")
	if err != nil {
		if publicerr.KindOf(err) == publicerr.KindUpstream && isNotReachable(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	if result.Status != http.StatusOK {
		return nil, "", nil
	}
	return result.Body, result.URL, nil
}

// isNotReachable distinguishes "this site publishes no such document" from "the
// document is there and something went wrong reading it". The first is the
// ordinary case and is not worth a failure; the second is.
func isNotReachable(err error) bool {
	return !errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrRedirectRefused)
}
