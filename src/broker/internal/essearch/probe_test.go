package essearch_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch/essearchtest"
)

// The probe renders the template first, then runs it, both with the probe's
// query and a limit of one.
func TestProbe_RendersThenSearchesWithLimitOne(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	if err := newSearcher(t, es.Config()).Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	reqs := es.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if reqs[0].Path != "/_render/template" || reqs[1].Path != "/catalog/_search/template" {
		t.Errorf("paths = %s then %s, want the render then the search", reqs[0].Path, reqs[1].Path)
	}
	const body = `{"id":"fora-catalog","params":{"query":"fora boot probe","limit":1}}`
	reqs[0].AssertBody(t, body)
	reqs[1].AssertBody(t, body)
}

func TestProbe_Classification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		render essearchtest.Reply
		search essearchtest.Reply
		want   error
		names  string
	}{
		{name: "render refused credentials", render: essearchtest.Reply{Status: 401, ErrorType: "security_exception"}, want: essearch.ErrCredentials},
		{name: "render forbidden", render: essearchtest.Reply{Status: 403, ErrorType: "security_exception"}, want: essearch.ErrCredentials},
		{name: "search forbidden", search: essearchtest.Reply{Status: 403, ErrorType: "security_exception"}, want: essearch.ErrCredentials},
		{
			name:   "no stored template",
			render: essearchtest.Reply{Status: 404, ErrorType: "resource_not_found_exception"},
			want:   essearch.ErrMisconfigured,
		},
		{
			name:   "a template that does not compile",
			render: essearchtest.Reply{Status: 400, ErrorType: "script_exception"},
			want:   essearch.ErrMisconfigured,
		},
		{
			name:   "a template that renders as no object",
			render: essearchtest.Reply{RawBody: `{"template_output":"size: 10"}`},
			want:   essearch.ErrMisconfigured,
		},
		{name: "a missing index", search: essearchtest.Reply{Status: 404, ErrorType: "index_not_found_exception"}, want: essearch.ErrMisconfigured},
		{
			name:   "a hit without a URL at the path",
			search: essearchtest.Reply{Hits: []map[string]any{{"_id": "doc-1", "_source": map[string]any{"title": "x"}}}},
			want:   essearch.ErrMisconfigured,
			names:  "_source.url",
		},
		{name: "rate limited", search: essearchtest.Reply{Status: 429}, want: essearch.ErrUnreachable},
		{name: "cluster failing", render: essearchtest.Reply{Status: 503}, want: essearch.ErrUnreachable},
		{name: "a timed-out search", search: essearchtest.Reply{TimedOut: true}, want: essearch.ErrUnreachable},
		{name: "not a search answer", search: essearchtest.Reply{RawBody: "{}"}, want: essearch.ErrUnreachable},
		{name: "not a render answer", render: essearchtest.Reply{RawBody: "{}"}, want: essearch.ErrUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := essearchtest.NewServer(t)
			es.ScriptRender(tc.render)
			es.Script(tc.search)
			err := newSearcher(t, es.Config()).Probe(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.names != "" && !strings.Contains(err.Error(), tc.names) {
				t.Errorf("err = %q, want it to name %s", err, tc.names)
			}
		})
	}
}

func TestProbe_ClosedPortIsUnreachable(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	s := newSearcher(t, es.Config())
	es.Close()
	if err := s.Probe(context.Background()); !errors.Is(err, essearch.ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
}
