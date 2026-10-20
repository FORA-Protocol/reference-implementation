// The address rendering is pure logic whose branches are the point: each case
// is an address whose secret a careless rendering repeats into an error, a
// stored failure reason or a log line.
package guardedfetch_test

import (
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/guardedfetch"
)

func TestRedacted(t *testing.T) {
	t.Parallel()
	const unreadable = "(an address that is not a well-formed http or https URL)"
	for _, tc := range []struct{ name, address, want string }{
		{"a user and a password", "https://user:secret@publisher.example/sitemap.xml", "https://xxxxx@publisher.example/sitemap.xml"},
		{"a token in the user-name slot", "https://secret@publisher.example/sitemap.xml", "https://xxxxx@publisher.example/sitemap.xml"},
		{"an encoded colon in the user name", "https://user%3Asecret@publisher.example/x", "https://xxxxx@publisher.example/x"},
		{"no credentials", "http://publisher.example:8080/news/caf%C3%A9?page=2", "http://publisher.example:8080/news/caf%C3%A9?page=2"},
		{"an opaque http address", "https:user:secret@publisher.example/x", unreadable},
		{"another scheme", "ftp://user:secret@publisher.example/x", unreadable},
		{"an address that does not parse", "http://user:secret@publisher.example/%zz", unreadable},
		{"no host", "https:///secret/x", unreadable},
		{"not a URL at all", "secret", unreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := guardedfetch.Redacted(tc.address); got != tc.want {
				t.Errorf("Redacted(%q) = %q, want %q", tc.address, got, tc.want)
			}
		})
	}
}

// A nil URL has nothing to show, and is rendered as unreadable rather than
// taking the caller down.
func TestRedactedURLOfNil(t *testing.T) {
	t.Parallel()
	if got := guardedfetch.RedactedURL(nil); got != "(an address that is not a well-formed http or https URL)" {
		t.Errorf("RedactedURL(nil) = %q", got)
	}
}
