package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch/essearchtest"
)

// The provider needs neither the model's credentials nor a catalog feed, and the
// settings arrive where the search reads them.
func TestElasticsearch_BootsAndSearchesWithItsOwnSettingsAlone(t *testing.T) {
	es := essearchtest.NewServer(t)
	esWorkingEnv(t, es)
	t.Setenv(envESResultLimit, " 25 ")
	t.Setenv(envQueryCatalogFile, "")
	t.Setenv(envQueryModel, "")
	t.Setenv("AWS_REGION", "")
	logs, logger := testutil.NewLogCapture()

	s, err := buildQuerySearcher(context.Background(), logger)
	if err != nil || s == nil {
		t.Fatalf("searcher = %v, err = %v", s, err)
	}
	logs.AssertLoggedOnce(t, "broker.es.ready", "INFO")
	logs.AssertLoggedOnce(t, "broker.es.plain_http", "WARN")
	rec := logs.AssertLoggedOnce(t, "broker.query_discovery.enabled", "INFO")
	for k, want := range map[string]any{
		"provider": providerElasticsearch, "index": "catalog", "template": "id:catalog-tpl",
		"hit_url_path": "_source.url", "timeout": "3s", "auth": "api_key", "filter_keys": "none",
	} {
		if rec[k] != want {
			t.Errorf("enabled record %s = %v, want %v (record %v)", k, rec[k], want, rec)
		}
	}
	if got := logs.Find("broker.es.no_auth"); len(got) != 0 {
		t.Errorf("warned about no authentication under api_key: %v", got)
	}
	assertNoCredential(t, "the log", strings.Join(logs.Lines(), "\n"))

	if _, err := s.Search(context.Background(), "headline", essearch.Filters(nil)); err != nil {
		t.Fatalf("Search: %v", err)
	}
	reqs := es.Requests()
	reqs[len(reqs)-1].AssertBody(t, `{"id":"catalog-tpl","params":{"query":"headline","limit":25}}`)
}

func TestElasticsearch_TemplateFileIsSentAsSource(t *testing.T) {
	es := essearchtest.NewServer(t)
	esWorkingEnv(t, es)
	const source = `{"query":{"match":{"title":"{{query}}"}},"size":"{{limit}}"}`
	path := writeTempFile(t, "template.mustache", source)
	t.Setenv(envESTemplateID, "")
	t.Setenv(envESTemplateFile, path)
	logs, logger := testutil.NewLogCapture()

	s, err := buildQuerySearcher(context.Background(), logger)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rec := logs.AssertLoggedOnce(t, "broker.query_discovery.enabled", "INFO"); rec["template"] != "file:"+path {
		t.Errorf("template = %v, want file:%s", rec["template"], path)
	}
	if _, err := s.Search(context.Background(), "q", essearch.Filters(nil)); err != nil {
		t.Fatalf("Search: %v", err)
	}
	reqs := es.Requests()
	reqs[len(reqs)-1].AssertBody(t, `{"source":`+jsonString(source)+`,"params":{"query":"q","limit":25}}`)
}

// jsonString quotes s as a JSON string.
func jsonString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func TestElasticsearch_CredentialsArriveFromFiles(t *testing.T) {
	es := essearchtest.NewServer(t)
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			"an API key file, trimmed",
			map[string]string{envESKey: "", envESKey + "_FILE": writeTempFile(t, "key", "key-from-file\n")},
			"ApiKey key-from-file",
		},
		{
			"a password file",
			map[string]string{
				envESAuth: "basic", envESKey: "", envESUsername: "reader",
				envESUserKey + "_FILE": writeTempFile(t, "pass", "secret\n"),
			},
			"Basic cmVhZGVyOnNlY3JldA==",
		},
		{"no authentication", map[string]string{envESAuth: "none", envESKey: ""}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			esWorkingEnv(t, es)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger()); err != nil {
				t.Fatalf("build: %v", err)
			}
			reqs := es.Requests()
			if got := reqs[len(reqs)-1].Authorization; got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestElasticsearch_NoAuthenticationWarns(t *testing.T) {
	es := essearchtest.NewServer(t)
	esWorkingEnv(t, es)
	t.Setenv(envESAuth, "none")
	t.Setenv(envESKey, "")
	logs, logger := testutil.NewLogCapture()
	if _, err := buildQuerySearcher(context.Background(), logger); err != nil {
		t.Fatalf("build: %v", err)
	}
	logs.AssertLoggedOnce(t, "broker.es.no_auth", "WARN")
	if rec := logs.AssertLoggedOnce(t, "broker.query_discovery.enabled", "INFO"); rec["auth"] != "none" {
		t.Errorf("auth = %v, want none", rec["auth"])
	}
}

// The schema decides what Parse admits, and the enabled record lists its keys.
func TestElasticsearch_FiltersSchemaFromFileNarrowsParse(t *testing.T) {
	es := essearchtest.NewServer(t)
	esWorkingEnv(t, es)
	t.Setenv(envESFilterSchema+"_FILE", writeTempFile(t, "filters.json", `{"type":"object","additionalProperties":false,
		"properties":{"news.language":{"type":"string","pattern":"^[a-z]{2}$"},"news.topic":{"type":"string"}}}`))
	logs, logger := testutil.NewLogCapture()

	s, err := buildQuerySearcher(context.Background(), logger)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rec := logs.AssertLoggedOnce(t, "broker.query_discovery.enabled", "INFO"); rec["filter_keys"] != "news.language,news.topic" {
		t.Errorf("filter_keys = %v", rec["filter_keys"])
	}
	if f, err := s.Parse(map[string]any{"news.language": "en"}); err != nil || f.Empty() {
		t.Errorf("declared key: %v, %v; want accepted", f, err)
	}
	_, err = s.Parse(map[string]any{"music.genre": "x"})
	var ferr *catalogsearch.FilterError
	if !errors.As(err, &ferr) || !ferr.Unsupported || ferr.Key != "music.genre" {
		t.Errorf("undeclared key: err = %v, want music.genre unsupported", err)
	}
}

// Refused credentials are the operator's own setting and never heal by waiting,
// so they refuse to start, naming the setting of the mode and not its value:
// neither the refusal nor a log line carries the key or the password.
func TestElasticsearch_RefusedCredentialsRefuseToStart(t *testing.T) {
	modes := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"an API key", nil, envESKey},
		{"a password", map[string]string{
			envESAuth: "basic", envESKey: "", envESUsername: "reader", envESUserKey: esTestUserKey,
		}, envESUserKey},
		{"no credentials", map[string]string{envESAuth: "none", envESKey: ""}, envESAuth},
	}
	for _, mode := range modes {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			es := essearchtest.NewServer(t)
			es.ScriptRender(essearchtest.Reply{Status: status, ErrorType: "security_exception", ErrorReason: "refused"})
			esWorkingEnv(t, es)
			for k, v := range mode.env {
				t.Setenv(k, v)
			}

			logs, logger := testutil.NewLogCapture()
			s, err := buildQuerySearcher(context.Background(), logger)

			assertBootRefusal(t, s, err, mode.want, "security_exception")
			assertNoCredential(t, mode.name+": the refusal", err.Error())
			assertNoCredential(t, mode.name+": the log", strings.Join(logs.Lines(), "\n"))
		}
	}
}

// Everything else can be the index owner's side, so the Broker starts: an outage
// of the index must not take URI requests and purchases down with it.
func TestElasticsearch_OtherProbeFaultsWarnAndStart(t *testing.T) {
	cases := []struct {
		name   string
		render essearchtest.Reply
		search essearchtest.Reply
		want   string
		names  string
	}{
		{name: "no stored template", render: essearchtest.Reply{Status: http.StatusNotFound, ErrorType: "resource_not_found_exception"}, want: "broker.es.misconfigured"},
		{name: "a template that does not compile", render: essearchtest.Reply{Status: http.StatusBadRequest, ErrorType: "script_exception"}, want: "broker.es.misconfigured"},
		{name: "a missing index", search: essearchtest.Reply{Status: http.StatusNotFound, ErrorType: "index_not_found_exception"}, want: "broker.es.misconfigured"},
		{
			name:   "a hit without a URL at the path",
			search: essearchtest.Reply{Hits: []map[string]any{{"_id": "doc-1", "_source": map[string]any{"title": "x"}}}},
			want:   "broker.es.misconfigured",
			names:  "_source.url",
		},
		{name: "a failing cluster", search: essearchtest.Reply{Status: http.StatusServiceUnavailable}, want: "broker.es.unreachable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			es := essearchtest.NewServer(t)
			es.ScriptRender(tc.render)
			es.Script(tc.search)
			esWorkingEnv(t, es)
			rec := assertStartedWithWarning(t, tc.want)
			if err, _ := rec["err"].(string); tc.names != "" && !strings.Contains(err, tc.names) {
				t.Errorf("warning err = %q, want it to name %s", err, tc.names)
			}
		})
	}
}

func TestElasticsearch_ClosedPortWarnsAndStarts(t *testing.T) {
	es := essearchtest.NewServer(t)
	esWorkingEnv(t, es)
	es.Close()
	assertStartedWithWarning(t, "broker.es.unreachable")
}

func assertStartedWithWarning(t *testing.T, event string) map[string]any {
	t.Helper()
	logs, logger := testutil.NewLogCapture()
	s, err := buildQuerySearcher(context.Background(), logger)
	if err != nil || s == nil {
		t.Fatalf("searcher = %v, err = %v; want a Broker that starts", s, err)
	}
	rec := logs.AssertLoggedOnce(t, event, "WARN")
	if got := logs.Find("broker.es.ready"); len(got) != 0 {
		t.Errorf("logged ready after a failed probe: %v", got)
	}
	return rec
}
