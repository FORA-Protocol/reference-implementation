package essearch_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch/essearchtest"
)

const (
	urlA = "https://publisher.example/a"
	urlB = "https://publisher.example/b"
	urlC = "https://publisher.example/c"
)

// none is the parsed value of a request without filters.
var none = essearch.Filters(nil)

func newSearcher(t *testing.T, cfg essearch.Config) *essearch.Searcher {
	t.Helper()
	s, err := essearch.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// The request is the whole contract with the cluster: which index, which
// template, which parameters, which credentials.
func TestSearch_RequestShape(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	s := newSearcher(t, es.Config())

	ctx := reqctx.WithRequestID(context.Background(), "req-1")
	if _, err := s.Search(ctx, "central bank", none); err != nil {
		t.Fatalf("Search: %v", err)
	}

	reqs := es.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Method != http.MethodPost || got.Path != "/catalog/_search/template" {
		t.Errorf("request = %s %s", got.Method, got.Path)
	}
	if got.OpaqueID != "req-1" {
		t.Errorf("X-Opaque-Id = %q, want the request id", got.OpaqueID)
	}
	if want := "ApiKey " + essearchtest.Key; got.Authorization != want {
		t.Errorf("Authorization = %q, want %q", got.Authorization, want)
	}
	got.AssertBody(t, `{"id":"fora-catalog","params":{"query":"central bank","limit":10}}`)
}

// The template sees exactly the parameters the docs promise: query only when
// there is query text, filters only when some were accepted, nested by dot, and
// always the limit.
func TestSearch_ParamsContract(t *testing.T) {
	t.Parallel()
	schema := loadSchema(t, `{"type":"object","additionalProperties":false,"properties":{
		"news.language":{"type":"string"},"news.date_published_after":{"type":"string"},"topic":{"type":"string"}}}`)
	cases := []struct {
		name    string
		source  bool
		query   string
		filters map[string]any
		want    string
	}{
		{"no query is no query member", false, "", nil, `{"id":"fora-catalog","params":{"limit":10}}`},
		{
			"filters nest by dot, a dotless key stays flat", false, "",
			map[string]any{"news.language": "en", "news.date_published_after": "2026-01-02T00:00:00Z", "topic": "x"},
			`{"id":"fora-catalog","params":{"limit":10,"filters":{
				"news":{"language":"en","date_published_after":"2026-01-02T00:00:00Z"},"topic":"x"}}}`,
		},
		{
			"a query and filters", false, "central bank",
			map[string]any{"news.language": "el"},
			`{"id":"fora-catalog","params":{"query":"central bank","limit":10,"filters":{"news":{"language":"el"}}}}`,
		},
		{"empty filters are no filters member", false, "q", map[string]any{}, `{"id":"fora-catalog","params":{"query":"q","limit":10}}`},
		{"a template file is sent as source", true, "q", nil, `{"source":"{\"size\":\"{{limit}}\"}","params":{"query":"q","limit":10}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := essearchtest.NewServer(t)
			cfg := es.Config()
			cfg.Filters = schema
			if tc.source {
				cfg.TemplateID, cfg.TemplateSource = "", `{"size":"{{limit}}"}`
			}
			s := newSearcher(t, cfg)
			f, err := s.Parse(tc.filters)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if _, err := s.Search(context.Background(), tc.query, f); err != nil {
				t.Fatalf("Search: %v", err)
			}
			es.Requests()[0].AssertBody(t, tc.want)
		})
	}
}

func TestSearch_WithoutRequestIDSendsNoOpaqueID(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	if _, err := newSearcher(t, es.Config()).Search(context.Background(), "q", none); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := es.Requests()[0].OpaqueID; got != "" {
		t.Errorf("X-Opaque-Id = %q, want none", got)
	}
}

func TestSearch_BasicAuthentication(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	cfg := es.Config()
	cfg.Auth, cfg.APIKey, cfg.Username, cfg.Password = essearch.AuthBasic, "", "reader", "secret"
	if _, err := newSearcher(t, cfg).Search(context.Background(), "q", none); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got, want := es.Requests()[0].Authorization, "Basic cmVhZGVyOnNlY3JldA=="; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
}

func TestSearch_NoAuthSendsNoHeader(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	cfg := es.Config()
	cfg.Auth, cfg.APIKey = essearch.AuthNone, ""
	if _, err := newSearcher(t, cfg).Search(context.Background(), "q", none); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := es.Requests()[0].Authorization; got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}

// The hit path walks objects, an array standing for its first element, and the
// value must be an absolute URL. A hit that breaks the rule fails the search
// and the error names the path.
func TestSearch_HitPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		path string
		hit  map[string]any
		ok   bool
	}{
		{"_source.url", "_source.url", map[string]any{"_source": map[string]any{"url": urlA}}, true},
		{"a projected field is an array", "fields.url", map[string]any{"fields": map[string]any{"url": []any{urlA, urlB}}}, true},
		{"a nested object", "_source.links.canonical", map[string]any{
			"_source": map[string]any{"links": map[string]any{"canonical": urlA}},
		}, true},
		{"an array of objects on the way", "_source.links.canonical", map[string]any{
			"_source": map[string]any{"links": []any{map[string]any{"canonical": urlA}}},
		}, true},
		{"a missing path", "_source.url", map[string]any{"_source": map[string]any{"title": "x"}}, false},
		{"no source", "_source.url", map[string]any{}, false},
		{"a number", "_source.url", map[string]any{"_source": map[string]any{"url": 42}}, false},
		{"an empty array", "fields.url", map[string]any{"fields": map[string]any{"url": []any{}}}, false},
		{"null", "_source.url", map[string]any{"_source": map[string]any{"url": nil}}, false},
		{"a bare path", "_source.url", map[string]any{"_source": map[string]any{"url": "/articles/a"}}, false},
		{"no scheme", "_source.url", map[string]any{"_source": map[string]any{"url": "publisher.example/a"}}, false},
		{"not a URL", "_source.url", map[string]any{"_source": map[string]any{"url": "https://publisher.example/%zz"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := essearchtest.NewServer(t)
			hit := map[string]any{"_id": "doc-1"}
			for k, v := range tc.hit {
				hit[k] = v
			}
			es.Script(essearchtest.Reply{Hits: []map[string]any{hit}})
			cfg := es.Config()
			cfg.HitURLPath = tc.path
			got, err := newSearcher(t, cfg).Search(context.Background(), "q", none)
			if tc.ok {
				if err != nil || !slices.Equal(got, []string{urlA}) {
					t.Fatalf("got %v, %v; want [%s]", got, err, urlA)
				}
				return
			}
			if !errors.Is(err, essearch.ErrUnavailable) || !errors.Is(err, essearch.ErrMalformedHit) {
				t.Fatalf("err = %v, want ErrUnavailable wrapping ErrMalformedHit", err)
			}
			if !strings.Contains(err.Error(), tc.path) || !strings.Contains(err.Error(), "doc-1") {
				t.Errorf("err = %q, want it to name the hit and %s", err, tc.path)
			}
			if got != nil {
				t.Errorf("uris = %v, want none", got)
			}
		})
	}
}

func TestSearch_Results(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		limit int
		urls  []string
		want  []string
	}{
		{"hit order is kept", 10, []string{urlC, urlA, urlB}, []string{urlC, urlA, urlB}},
		{"duplicates keep their first position", 10, []string{urlB, urlA, urlB, urlA}, []string{urlB, urlA}},
		{"no hits is a nil result", 10, nil, nil},
		{"the limit holds if the cluster sends more", 2, []string{urlA, urlB, urlC}, []string{urlA, urlB}},
		{"duplicates do not count towards the limit", 2, []string{urlA, urlA, urlB, urlC}, []string{urlA, urlB}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := essearchtest.NewServer(t)
			es.Script(essearchtest.Reply{URLs: tc.urls})
			cfg := es.Config()
			cfg.Limit = tc.limit
			got, err := newSearcher(t, cfg).Search(context.Background(), "q", none)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("uris = %v, want %v", got, tc.want)
			}
		})
	}
}

// A hit past the limit is not read, so it cannot fail the search.
func TestSearch_HitPastTheLimitIsNotRead(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	es.Script(essearchtest.Reply{URLs: []string{urlA}, Hits: []map[string]any{{"_id": "bad", "_source": map[string]any{"url": 42}}}})
	cfg := es.Config()
	cfg.Limit = 1
	got, err := newSearcher(t, cfg).Search(context.Background(), "q", none)
	if err != nil || !slices.Equal(got, []string{urlA}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

// Filters another vocabulary parsed never reach the cluster.
func TestSearch_ForeignFiltersAreRefused(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	for name, f := range map[string]catalogsearch.Filters{"another vocabulary's value": foreign{}, "no value": nil} {
		if _, err := newSearcher(t, es.Config()).Search(context.Background(), "q", f); !errors.Is(err, catalogsearch.ErrForeignFilters) {
			t.Errorf("%s: err = %v, want catalogsearch.ErrForeignFilters", name, err)
		}
	}
	if n := len(es.Requests()); n != 0 {
		t.Errorf("requests = %d, want none", n)
	}
}

// foreign is a filter value no essearch vocabulary produced.
type foreign struct{}

func (foreign) Empty() bool { return true }

func TestSearch_TimeoutBoundsTheCall(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	es.Script(essearchtest.Reply{Delay: 5 * time.Second})
	cfg := es.Config()
	cfg.Timeout = 50 * time.Millisecond

	_, err := newSearcher(t, cfg).Search(context.Background(), "q", none)

	if !errors.Is(err, essearch.ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want ErrUnavailable wrapping the deadline", err)
	}
}

func TestSearch_CallerCancellationEndsTheCall(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	es.Script(essearchtest.Reply{Delay: 5 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	_, err := newSearcher(t, es.Config()).Search(ctx, "q", none)

	if !errors.Is(err, essearch.ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want ErrUnavailable wrapping the cancellation", err)
	}
}

// One endpoint means a retry hits the same node, and it would stretch the search
// past its timeout.
func TestSearch_DoesNotRetry(t *testing.T) {
	t.Parallel()
	es := essearchtest.NewServer(t)
	es.Script(essearchtest.Reply{Status: 503})
	if _, err := newSearcher(t, es.Config()).Search(context.Background(), "q", none); err == nil {
		t.Fatal("Search succeeded against a failing cluster")
	}
	if got := len(es.Requests()); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestSearch_FailureTextCarriesNoCredential(t *testing.T) {
	t.Parallel()
	const marker = "not-a-real-one"
	es := essearchtest.NewServer(t)
	es.Script(essearchtest.Reply{Status: 401, ErrorType: "security_exception", ErrorReason: "unable to authenticate"})
	cfg := es.Config()
	cfg.Auth, cfg.APIKey, cfg.Username, cfg.Password = essearch.AuthBasic, "", "reader", marker
	s := newSearcher(t, cfg)

	_, refused := s.Search(context.Background(), "q", none)
	es.Close()
	_, unreachable := s.Search(context.Background(), "q", none)

	for _, err := range []error{refused, unreachable} {
		if err == nil {
			t.Fatal("Search succeeded")
		}
		if strings.Contains(err.Error(), marker) {
			t.Errorf("error carries the password: %v", err)
		}
	}
}

// Every way a search can fail wraps ErrUnavailable, and the error identifies the
// cause without quoting a credential. A redirect is an answer, never followed.
func TestSearch_FailuresAreUnavailable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		reply essearchtest.Reply
		want  string
	}{
		{
			"bad credentials",
			essearchtest.Reply{Status: 401, ErrorType: "security_exception", ErrorReason: "unable to authenticate"},
			"status 401 security_exception",
		},
		{
			"no access to the index",
			essearchtest.Reply{Status: 403, ErrorType: "security_exception", ErrorReason: "action is unauthorized"},
			"status 403 security_exception",
		},
		{
			"missing index",
			essearchtest.Reply{Status: 404, ErrorType: "index_not_found_exception", ErrorReason: "no such index [catalog]"},
			"status 404 index_not_found_exception",
		},
		{
			"a template the cluster cannot render reports its root cause",
			essearchtest.Reply{Status: 400, RawBody: `{"error":{
				"root_cause":[{"type":"script_exception","reason":"Failed to compile inline script"}],
				"type":"search_phase_execution_exception","reason":"all shards failed"}}`},
			"status 400 script_exception: Failed to compile inline script",
		},
		{"cluster failing", essearchtest.Reply{Status: 503, ErrorType: "unavailable_shards_exception"}, "status 503"},
		{"a proxy's error page", essearchtest.Reply{Status: 502, RawBody: "<html>bad gateway</html>"}, "status 502"},
		{"a redirect", essearchtest.Reply{RedirectTo: "https://elsewhere.example/catalog/_search/template"}, "status 302"},
		{"a timed-out search", essearchtest.Reply{URLs: []string{urlA}, TimedOut: true}, "timed out"},
		{"a failed shard", essearchtest.Reply{URLs: []string{urlA}, FailedShards: 1}, "shards"},
		{"not JSON", essearchtest.Reply{RawBody: "<html>hello</html>"}, "decode"},
		{"truncated JSON", essearchtest.Reply{RawBody: `{"took":1,"hits":{"hits":[`}, "decode"},
		{"an empty object", essearchtest.Reply{RawBody: `{}`}, "not a search result"},
		{"no hit list", essearchtest.Reply{RawBody: `{"_shards":{"failed":0},"hits":{}}`}, "not a search result"},
		{"no shard report", essearchtest.Reply{RawBody: `{"hits":{"hits":[]}}`}, "not a search result"},
		{"a body over the read cap", essearchtest.Reply{RawBody: `{"pad":"` + strings.Repeat("x", 1<<20) + `"}`}, "over 1 MiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := essearchtest.NewServer(t)
			es.Script(tc.reply)
			uris, err := newSearcher(t, es.Config()).Search(context.Background(), "q", none)
			if !errors.Is(err, essearch.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to identify %q", err, tc.want)
			}
			if uris != nil {
				t.Errorf("uris = %v, want none alongside the failure", uris)
			}
			if n := len(es.Requests()); n != 1 {
				t.Errorf("requests = %d, want exactly 1", n)
			}
		})
	}
}
