package catalogid_test

import (
	"errors"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/catalogid"
)

// The expected strings below are written out rather than built with the
// package, so a change to how a page is named fails here instead of moving
// the expectation along with the code.

func TestPageNamesThePageByConcatenation(t *testing.T) {
	cases := []struct {
		name                    string
		page                    catalogid.Page
		key, uri, resourceID    string
		httpURI, httpResourceID string
	}{
		{
			name:           "keyed by uri",
			page:           catalogid.Page{TenantID: "t_1", Domain: "publisher.example", Path: "/news/caf%C3%A9"},
			key:            "publisher.example/news/caf%C3%A9",
			uri:            "https://publisher.example/news/caf%C3%A9",
			resourceID:     "t_1:https://publisher.example/news/caf%C3%A9",
			httpURI:        "http://publisher.example/news/caf%C3%A9",
			httpResourceID: "t_1:http://publisher.example/news/caf%C3%A9",
		},
		{
			name:           "keyed by content id",
			page:           catalogid.Page{TenantID: "t_1", Domain: "publisher.example:8443", Path: "/a", ContentID: "story-1"},
			key:            "publisher.example:8443/a",
			uri:            "https://publisher.example:8443/a",
			resourceID:     "t_1:publisher.example:8443:story-1",
			httpURI:        "http://publisher.example:8443/a",
			httpResourceID: "t_1:publisher.example:8443:story-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.page.Key(); got != tc.key {
				t.Errorf("Key = %q, want %q", got, tc.key)
			}
			if got := tc.page.URI("https"); got != tc.uri {
				t.Errorf("URI(https) = %q, want %q", got, tc.uri)
			}
			if got := tc.page.ResourceID("https"); got != tc.resourceID {
				t.Errorf("ResourceID(https) = %q, want %q", got, tc.resourceID)
			}
			if got := tc.page.URI("http"); got != tc.httpURI {
				t.Errorf("URI(http) = %q, want %q", got, tc.httpURI)
			}
			if got := tc.page.ResourceID("http"); got != tc.httpResourceID {
				t.Errorf("ResourceID(http) = %q, want %q", got, tc.httpResourceID)
			}
		})
	}
}

func TestRowKeyDropsOnlyTheScheme(t *testing.T) {
	cases := []struct {
		uri, key string
		ok       bool
	}{
		{uri: "https://publisher.example/news/caf%C3%A9", key: "publisher.example/news/caf%C3%A9", ok: true},
		{uri: "http://publisher.example/news/caf%C3%A9", key: "publisher.example/news/caf%C3%A9", ok: true},
		{uri: "https://publisher.example:8443/a", key: "publisher.example:8443/a", ok: true},
		// Only the first "://" ends the scheme; a path may carry another.
		{uri: "https://publisher.example/x://y", key: "publisher.example/x://y", ok: true},
		{uri: "publisher.example/a", key: "", ok: false},
	}
	for _, tc := range cases {
		key, ok := catalogid.Row{URI: tc.uri}.Key()
		if key != tc.key || ok != tc.ok {
			t.Errorf("Row{URI: %q}.Key() = %q, %v; want %q, %v", tc.uri, key, ok, tc.key, tc.ok)
		}
	}
}

// TestRowContentIDReadsBackWhatThePushCarried stores each page the way the
// catalog push does and reads the content id back off the stored row, under
// both schemes a deployment uses.
func TestRowContentIDReadsBackWhatThePushCarried(t *testing.T) {
	pages := map[string]catalogid.Page{
		"no content id":         {TenantID: "t_1", Domain: "publisher.example", Path: "/a"},
		"plain content id":      {TenantID: "t_1", Domain: "publisher.example", Path: "/a", ContentID: "story-1"},
		"content id with colon": {TenantID: "t_1", Domain: "publisher.example", Path: "/a", ContentID: "cms:story:1"},
		"content id with a uri": {TenantID: "t_1", Domain: "publisher.example", Path: "/a", ContentID: "https://cms.example/1"},
		"domain with a port":    {TenantID: "t_1", Domain: "publisher.example:8443", Path: "/a", ContentID: "story-1"},
		"seat id with a colon":  {TenantID: "seat:eu", Domain: "publisher.example", Path: "/a", ContentID: "story-1"},
		"seat id with a hyphen": {TenantID: "tenant-staging", Domain: "publisher.example", Path: "/a"},
	}
	for name, page := range pages {
		for _, scheme := range []string{"https", "http"} {
			row := catalogid.Row{TenantID: page.TenantID, URI: page.URI(scheme), ResourceID: page.ResourceID(scheme)}
			got, err := row.ContentID()
			if err != nil || got != page.ContentID {
				t.Errorf("%s over %s: ContentID() = %q, %v; want %q", name, scheme, got, err, page.ContentID)
			}
			if key, ok := row.Key(); !ok || key != page.Key() {
				t.Errorf("%s over %s: Row.Key() = %q, %v; want %q", name, scheme, key, ok, page.Key())
			}
		}
	}
}

func TestRowContentIDRefusesAnIDItDoesNotBuild(t *testing.T) {
	const uri = "https://publisher.example/a"
	cases := map[string]catalogid.Row{
		"legacy id without the site": {TenantID: "t_1", URI: uri, ResourceID: "t_1:story-1"},
		"another seat":               {TenantID: "t_1", URI: uri, ResourceID: "t_2:publisher.example:story-1"},
		"another site":               {TenantID: "t_1", URI: uri, ResourceID: "t_1:other.example:story-1"},
		"empty content id":           {TenantID: "t_1", URI: uri, ResourceID: "t_1:publisher.example:"},
		"uri of another scheme":      {TenantID: "t_1", URI: uri, ResourceID: "t_1:http://publisher.example/a"},
		"uri without a scheme":       {TenantID: "t_1", URI: "publisher.example/a", ResourceID: "t_1:publisher.example:story-1"},
	}
	for name, row := range cases {
		got, err := row.ContentID()
		if !errors.Is(err, catalogid.ErrUnrecognizedResourceID) {
			t.Errorf("%s: ContentID() = %q, %v; want ErrUnrecognizedResourceID", name, got, err)
		}
	}
}
