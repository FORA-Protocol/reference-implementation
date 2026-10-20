//go:build integration

package app_test

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// allResources reads every one of the publisher's resources from the
// Exchange's catalog route, newest first.
func allResources(t *testing.T, f *consoleFixture) []exchangeclient.CatalogResource {
	t.Helper()
	return walkRoute(t, "catalog", func(cursor string) ([]exchangeclient.CatalogResource, string) {
		page, err := f.reads().ListCatalog(t.Context(), exchangeclient.ListCatalogRequest{
			Actor: f.actor(), PublisherID: f.publisherID, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("catalog route: %v", err)
		}
		return page.Resources, page.NextCursor
	})
}

// TestDashboard_CatalogPagesThroughWhatTheCatalogRouteReturns lists three
// resources two at a time. Walking the Next links shows each resource the
// catalog route returns, once, each opening its own page. The terms are not
// on the list: they are on the resource's page.
func TestDashboard_CatalogPagesThroughWhatTheCatalogRouteReturns(t *testing.T) {
	f := startSeller(t, withPageSize(2))
	buyer := f.exchange.Buyer(t)
	for i := range 3 {
		sell(t, buyer, "/articles/"+strconv.Itoa(i))
	}
	resources := allResources(t, f)
	if len(resources) != 3 {
		t.Fatalf("the catalog route holds %d resources, want 3", len(resources))
	}

	var seen string
	var linked []string
	path, pages := dashboard.PathCatalog, 0
	for path != "" {
		body := f.get2(t, path)
		pages++
		mustNotContain(t, "catalog page "+strconv.Itoa(pages), body, "0.05 USD per accesses")
		seen += body
		for _, m := range reResourceLink.FindAllStringSubmatch(body, -1) {
			linked = append(linked, m[1])
		}
		path = nextLink(body)
		if pages > maxWalk {
			t.Fatalf("still walking after %d pages: the Next link does not move on", pages)
		}
	}
	if pages != 2 {
		t.Fatalf("walked %d catalog pages, want 2", pages)
	}
	want := make([]string, 0, len(resources))
	for _, r := range resources {
		mustContain(t, "the catalog", seen, html.EscapeString(r.URI))
		want = append(want, url.PathEscape(r.ResourceID))
	}
	// Every resource once, in the route's order: a page that repeated one or
	// skipped one would still show each of them somewhere.
	if !slices.Equal(linked, want) {
		t.Fatalf("the pages link %v, want the route's %v", linked, want)
	}
}

// TestDashboard_AResourcePageShowsItsTermsAndItsSales opens one resource from
// the catalog. The terms the catalog route returns for it are said in words on
// its own page, and the page offers the transaction list narrowed to it.
func TestDashboard_AResourcePageShowsItsTermsAndItsSales(t *testing.T) {
	f := startSeller(t)
	sell(t, f.exchange.Buyer(t), "/articles/one")
	resources := allResources(t, f)
	if len(resources) != 1 {
		t.Fatalf("the catalog route holds %d resources, want 1", len(resources))
	}
	res := resources[0]

	body := f.get2(t, "/catalog/"+url.PathEscape(res.ResourceID))
	mustContain(t, "the resource page", body,
		html.EscapeString(res.URI),
		html.EscapeString(res.ResourceID),
		"0.05 USD per accesses",
		"resource="+url.QueryEscape(res.ResourceID))
}

// TestDashboard_AnotherPublishersResourceRendersNotFound opens the rival's
// resource. The read is scoped to the person's own publisher, so it is not
// found and nothing of the rival's listing shows.
func TestDashboard_AnotherPublishersResourceRendersNotFound(t *testing.T) {
	f := startSeller(t)
	r := newRival(t, f)
	rivals := allResources(t, r.consoleFixture)
	if len(rivals) == 0 {
		t.Fatal("the rival lists no resource to ask for")
	}

	body := f.status(t, "/catalog/"+url.PathEscape(rivals[0].ResourceID), http.StatusNotFound)
	mustContain(t, "the rival's resource", body, "Not found")
	mustNotContain(t, "the rival's resource", body, "harmonia.example")
}

// TestDashboard_AnEmptyCatalogSaysSo is a publisher whose domain is registered
// but whose pages have not been imported yet.
func TestDashboard_AnEmptyCatalogSaysSo(t *testing.T) {
	f := startSeller(t)
	body := f.get2(t, dashboard.PathCatalog)
	mustContain(t, "an empty catalog", body, "Nothing is listed yet.")
}

// reResourceLink finds the link each catalog row opens its resource page by.
var reResourceLink = regexp.MustCompile(`href="/catalog/([^"?]+)"`)

// TestDashboard_CatalogPreviousWalksBackThroughEveryPage walks the catalog
// forward two resources at a time, then back from the last page by its
// Previous links alone. Each page on the way back lists the resources it
// listed on the way forward, and the first page has no Previous link.
func TestDashboard_CatalogPreviousWalksBackThroughEveryPage(t *testing.T) {
	f := startSeller(t, withPageSize(2))
	buyer := f.exchange.Buyer(t)
	for i := range 5 {
		sell(t, buyer, "/articles/"+strconv.Itoa(i))
	}
	resourcesOn := func(body string) []string {
		matches := reResourceLink.FindAllStringSubmatch(body, -1)
		out := make([]string, 0, len(matches))
		for _, m := range matches {
			out = append(out, m[1])
		}
		return out
	}

	var forward [][]string
	body := f.get2(t, dashboard.PathCatalog)
	for {
		forward = append(forward, resourcesOn(body))
		next := nextLink(body)
		if next == "" || len(forward) > maxWalk {
			break
		}
		body = f.get2(t, next)
	}
	if len(forward) != 3 {
		t.Fatalf("walked forward through %d catalog pages, want 3", len(forward))
	}
	for i := len(forward) - 2; i >= 0; i-- {
		prev := prevLink(body)
		if prev == "" {
			t.Fatalf("catalog page %d has no Previous link", i+2)
		}
		body = f.get2(t, prev)
		if got := resourcesOn(body); !slices.Equal(got, forward[i]) {
			t.Fatalf("back on catalog page %d: %v, want %v", i+1, got, forward[i])
		}
	}
	if prevLink(body) != "" {
		t.Fatal("walking back reached a first catalog page that still has a Previous link")
	}
}
