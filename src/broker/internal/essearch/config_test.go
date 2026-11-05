package essearch_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
)

func validConfig() essearch.Config {
	return essearch.Config{
		Endpoint:   "https://es.example:9243",
		Index:      "catalog",
		TemplateID: "fora-catalog",
		HitURLPath: "_source.url",
		Limit:      10,
		Timeout:    3 * time.Second,
		Auth:       essearch.AuthAPIKey,
		APIKey:     "a2V5",
	}
}

func TestValidate_RefusesEachBadField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*essearch.Config)
		field  essearch.Field
	}{
		{"no endpoint", func(c *essearch.Config) { c.Endpoint = "" }, essearch.FieldEndpoint},
		{"unparsable endpoint", func(c *essearch.Config) { c.Endpoint = "https://exa mple" }, essearch.FieldEndpoint},
		{"no host", func(c *essearch.Config) { c.Endpoint = "es.example:9243" }, essearch.FieldEndpoint},
		{"http without the opt-in", func(c *essearch.Config) { c.Endpoint = "http://es.example" }, essearch.FieldEndpoint},
		{"other scheme", func(c *essearch.Config) { c.Endpoint = "ftp://es.example" }, essearch.FieldEndpoint},
		{"userinfo", func(c *essearch.Config) { c.Endpoint = "https://u:p@es.example" }, essearch.FieldEndpoint},
		{"query string", func(c *essearch.Config) { c.Endpoint = "https://es.example?a=b" }, essearch.FieldEndpoint},
		{"fragment", func(c *essearch.Config) { c.Endpoint = "https://es.example#a" }, essearch.FieldEndpoint},
		{"no index", func(c *essearch.Config) { c.Index = "" }, essearch.FieldIndex},
		{"pattern index", func(c *essearch.Config) { c.Index = "catalog-*" }, essearch.FieldIndex},
		{"index list", func(c *essearch.Config) { c.Index = "a,b" }, essearch.FieldIndex},
		{"all indices", func(c *essearch.Config) { c.Index = "_all" }, essearch.FieldIndex},
		{"index with a path", func(c *essearch.Config) { c.Index = "a/b" }, essearch.FieldIndex},
		{"remote index", func(c *essearch.Config) { c.Index = "remote:catalog" }, essearch.FieldIndex},
		{"dot index", func(c *essearch.Config) { c.Index = ".." }, essearch.FieldIndex},
		{"no template", func(c *essearch.Config) { c.TemplateID = "" }, essearch.FieldTemplate},
		{"both templates", func(c *essearch.Config) { c.TemplateSource = `{"query":{"match_all":{}}}` }, essearch.FieldTemplate},
		{"template id with a slash", func(c *essearch.Config) { c.TemplateID = "a/b" }, essearch.FieldTemplate},
		{"template id with whitespace", func(c *essearch.Config) { c.TemplateID = "fora catalog" }, essearch.FieldTemplate},
		{"source with a triple mustache", func(c *essearch.Config) {
			c.TemplateID, c.TemplateSource = "", `{"query":{"match":{"title":"{{{query}}}"}}}`
		}, essearch.FieldTemplate},
		{"source with an ampersand tag", func(c *essearch.Config) {
			c.TemplateID, c.TemplateSource = "", `{"query":{"match":{"title":"{{&query}}"}}}`
		}, essearch.FieldTemplate},
		{"source changing the delimiters", func(c *essearch.Config) {
			c.TemplateID, c.TemplateSource = "", `{{=<% %>=}}{"query":{"match":{"title":"<%&query%>"}}}`
		}, essearch.FieldTemplate},
		{"source joining values", func(c *essearch.Config) {
			c.TemplateID, c.TemplateSource = "", `{"query":{"match":{"title":"{{#join}}filters.news.tags{{/join}}"}}}`
		}, essearch.FieldTemplate},
		{"no hit path", func(c *essearch.Config) { c.HitURLPath = "" }, essearch.FieldHitURLPath},
		{"hit path with an empty name", func(c *essearch.Config) { c.HitURLPath = "_source..url" }, essearch.FieldHitURLPath},
		{"hit path starting with a dot", func(c *essearch.Config) { c.HitURLPath = ".url" }, essearch.FieldHitURLPath},
		{"limit zero", func(c *essearch.Config) { c.Limit = 0 }, essearch.FieldLimit},
		{"limit above the protocol cap", func(c *essearch.Config) { c.Limit = 257 }, essearch.FieldLimit},
		{"zero timeout", func(c *essearch.Config) { c.Timeout = 0 }, essearch.FieldTimeout},
		{"negative timeout", func(c *essearch.Config) { c.Timeout = -time.Second }, essearch.FieldTimeout},
		{"no auth mode", func(c *essearch.Config) { c.Auth, c.APIKey = "", "" }, essearch.FieldAuth},
		{"unknown auth mode", func(c *essearch.Config) { c.Auth = "token" }, essearch.FieldAuth},
		{"api_key without a key", func(c *essearch.Config) { c.APIKey = "" }, essearch.FieldAuth},
		{"api_key with a username", func(c *essearch.Config) { c.Username = "reader" }, essearch.FieldAuth},
		{"basic without a password", func(c *essearch.Config) {
			c.Auth, c.APIKey, c.Username = essearch.AuthBasic, "", "reader"
		}, essearch.FieldAuth},
		{"basic with a key", func(c *essearch.Config) {
			c.Auth, c.Username, c.Password = essearch.AuthBasic, "reader", "not-a-real-one"
		}, essearch.FieldAuth},
		{"none with a key", func(c *essearch.Config) { c.Auth = essearch.AuthNone }, essearch.FieldAuth},
		{"CA is not PEM", func(c *essearch.Config) { c.CACertPEM = []byte("not a certificate") }, essearch.FieldCACert},
		{"CA file is empty", func(c *essearch.Config) { c.CACertPEM = []byte{} }, essearch.FieldCACert},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			tc.mutate(&cfg)
			problems := cfg.Validate()
			if len(problems) != 1 || problems[0].Field != tc.field {
				t.Fatalf("problems = %v, want one on %s", problems, tc.field)
			}
			if s, err := essearch.New(cfg); err == nil || s != nil {
				t.Fatalf("New = %v, %v; want a refusal", s, err)
			}
		})
	}
}

func TestValidate_AcceptsEveryAllowedShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*essearch.Config)
	}{
		{"https", func(*essearch.Config) {}},
		{"http with the opt-in", func(c *essearch.Config) { c.Endpoint, c.AllowHTTP = "http://es.example", true }},
		{"endpoint behind a path", func(c *essearch.Config) { c.Endpoint = "https://proxy.example/es" }},
		{"alias with capitals", func(c *essearch.Config) { c.Index = "Catalog.v2" }},
		{"template text", func(c *essearch.Config) {
			c.TemplateID, c.TemplateSource = "", `{"query":{"match":{"title":"{{query}}"}}}`
		}},
		{"template text with the guide's sections and an escaped array", func(c *essearch.Config) {
			c.TemplateID, c.TemplateSource = "", `{"query":{"bool":{"must":[{{#query}}`+
				`{"match":{"title":"{{query}}"}}{{/query}}{{^query}}{"match_all":{}}{{/query}}],`+
				`"filter":[{{#filters.news.tags}}{"terms":{"tags":{{#toJson}}filters.news.tags{{/toJson}}}},`+
				`{{/filters.news.tags}}{"match_all":{}}]}}}`
		}},
		{"a projected URL field", func(c *essearch.Config) { c.HitURLPath = "fields.url" }},
		{"limit at the protocol cap", func(c *essearch.Config) { c.Limit = 256 }},
		{"no authentication", func(c *essearch.Config) { c.Auth, c.APIKey = essearch.AuthNone, "" }},
		{"basic auth", func(c *essearch.Config) {
			c.Auth, c.APIKey, c.Username, c.Password = essearch.AuthBasic, "", "reader", "not-a-real-one"
		}},
		{"private CA", func(c *essearch.Config) { c.CACertPEM = selfSignedPEM(t) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			tc.mutate(&cfg)
			if problems := cfg.Validate(); len(problems) != 0 {
				t.Fatalf("problems = %v", problems)
			}
			if _, err := essearch.New(cfg); err != nil {
				t.Fatalf("New: %v", err)
			}
		})
	}
}

// A refusal lists every fault at once, so a deployment is fixed in one pass.
func TestNew_ListsEveryProblem(t *testing.T) {
	t.Parallel()
	_, err := essearch.New(essearch.Config{})
	if err == nil {
		t.Fatal("New accepted an empty Config")
	}
	for _, f := range []essearch.Field{
		essearch.FieldEndpoint, essearch.FieldIndex, essearch.FieldTemplate, essearch.FieldHitURLPath,
		essearch.FieldLimit, essearch.FieldTimeout, essearch.FieldAuth,
	} {
		if !strings.Contains(err.Error(), string(f)) {
			t.Errorf("error %q does not name %s", err, f)
		}
	}
}

// url.Parse quotes its input in its error, and a mistyped endpoint can carry a
// password, so the reason must not repeat the parse error.
func TestValidate_EndpointProblemDoesNotEchoTheValue(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Endpoint = "https://reader:s3cret-value@es example:9243"
	problems := cfg.Validate()
	if len(problems) != 1 {
		t.Fatalf("problems = %v", problems)
	}
	if strings.Contains(problems[0].Error(), "s3cret-value") {
		t.Fatalf("problem repeats the password: %v", problems[0])
	}
}

// selfSignedPEM mints a throwaway certificate, so no key material is committed.
func selfSignedPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
