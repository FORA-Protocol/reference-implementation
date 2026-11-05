package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch/essearchtest"
)

// esRequired are the settings the provider cannot start without, in the order a
// refusal names them.
var esRequired = []string{envESURL, envESIndex, envESTemplateEither, envESHitURLPath, envESResultLimit, envESAuth}

// esEnv selects the provider and clears every setting, so the developer's own
// environment cannot decide a test.
func esEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envQueryProvider, providerElasticsearch)
	for _, name := range []string{
		envESURL, envESAllowHTTP, envESIndex, envESTemplateID, envESTemplateFile, envESHitURLPath,
		envESResultLimit, envESTimeout, envESAuth, envESKey, envESKey + "_FILE", envESUsername,
		envESUserKey, envESUserKey + "_FILE", envESCAFile, envESFilterSchema, envESFilterSchema + "_FILE",
		"ALLOW_INSECURE",
	} {
		t.Setenv(name, "")
	}
}

// esWorkingEnv sets everything a boot against the stand-in needs.
func esWorkingEnv(t *testing.T, es *essearchtest.Server) {
	t.Helper()
	esEnv(t)
	t.Setenv(envESURL, es.URL())
	t.Setenv(envESAllowHTTP, "true")
	t.Setenv(envESIndex, "catalog")
	t.Setenv(envESTemplateID, "catalog-tpl")
	t.Setenv(envESHitURLPath, "_source.url")
	t.Setenv(envESResultLimit, "25")
	t.Setenv(envESAuth, "api_key")
	t.Setenv(envESKey, essearchtest.Key)
}

// esTestUserKey is the password the tests give basic authentication, named like
// envESUserKey for the same reason. It is distinctive, so finding it in a
// refusal or a log line can only be a leak.
const esTestUserKey = "stand-in-user-key"

// assertNoCredential fails when text carries the stand-in's API key or the
// tests' password; where names the text in the failure.
func assertNoCredential(t *testing.T, where, text string) {
	t.Helper()
	for _, credential := range []string{essearchtest.Key, esTestUserKey} {
		if strings.Contains(text, credential) {
			t.Errorf("%s carries a credential: %s", where, text)
		}
	}
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestElasticsearch_NothingSetNamesEveryRequiredSetting(t *testing.T) {
	esEnv(t)
	s, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger())
	assertBootRefusal(t, s, err, esRequired...)
}

func TestElasticsearch_EachOmissionNamesThatSettingAlone(t *testing.T) {
	es := essearchtest.NewServer(t)
	for _, name := range esRequired {
		t.Run(name, func(t *testing.T) {
			esWorkingEnv(t, es)
			unset := name
			if name == envESTemplateEither {
				unset = envESTemplateID // the working environment names a stored template
			}
			t.Setenv(unset, "")
			s, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger())
			assertBootRefusal(t, s, err, "requires "+name)
			for _, other := range esRequired {
				if other != name && strings.Contains(err.Error(), other) {
					t.Errorf("error %q also names %s, which is set", err, other)
				}
			}
		})
	}
	if n := len(es.Requests()); n != 0 {
		t.Errorf("%d requests reached the cluster behind a refused configuration, want 0", n)
	}
}

func TestElasticsearch_InvalidSettingsRefuseToStart(t *testing.T) {
	es := essearchtest.NewServer(t)
	notPEM := writeTempFile(t, "ca.pem", "not a certificate")
	absent := filepath.Join(t.TempDir(), "absent")
	key := map[string]string{envESKey: ""}
	cases := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"both template settings", map[string]string{envESTemplateFile: writeTempFile(t, "t", "{}")}, []string{envESTemplateID, envESTemplateFile}},
		{"an unreadable template file", map[string]string{envESTemplateID: "", envESTemplateFile: absent}, []string{envESTemplateFile, "absent"}},
		{"an empty template file", map[string]string{envESTemplateID: "", envESTemplateFile: writeTempFile(t, "t", " \n")}, []string{envESTemplateFile, "empty"}},
		{"a template file over 64 KiB", map[string]string{
			envESTemplateID: "", envESTemplateFile: writeTempFile(t, "t", strings.Repeat("x", 64<<10+1)),
		}, []string{envESTemplateFile, "64 KiB"}},
		{"a template file inserting unescaped", map[string]string{
			envESTemplateID: "", envESTemplateFile: writeTempFile(t, "t", `{"query":{"match":{"title":"{{{query}}}"}}}`),
		}, []string{envESTemplateEither, "{{{"}},
		{"a template file changing the delimiters", map[string]string{
			envESTemplateID: "", envESTemplateFile: writeTempFile(t, "t", `{{=<% %>=}}{"query":{"match":{"title":"<%&query%>"}}}`),
		}, []string{envESTemplateEither, "{{="}},
		{"a template file joining values", map[string]string{
			envESTemplateID: "", envESTemplateFile: writeTempFile(t, "t", `{"title":"{{#join}}filters.news.tags{{/join}}"}`),
		}, []string{envESTemplateEither, "{{#join"}},
		{"a template id with a slash", map[string]string{envESTemplateID: "a/b"}, []string{envESTemplateEither}},
		{"http without the opt-in", map[string]string{envESAllowHTTP: ""}, []string{envESURL, envESAllowHTTP}},
		{"a typo in the opt-in", map[string]string{envESAllowHTTP: "yes"}, []string{envESURL, envESAllowHTTP}},
		{"the stack-wide opt-in is not this one", map[string]string{envESAllowHTTP: "", "ALLOW_INSECURE": "true"}, []string{envESAllowHTTP}},
		{"userinfo in the URL", map[string]string{envESURL: "https://reader:" + esTestUserKey + "@es.example"}, []string{envESURL}},
		{"a query in the URL", map[string]string{envESURL: "https://es.example?x=1"}, []string{envESURL}},
		{"a pattern for the index", map[string]string{envESIndex: "catalog-*"}, []string{envESIndex}},
		{"an empty name in the hit path", map[string]string{envESHitURLPath: "_source..url"}, []string{envESHitURLPath}},
		{"limit zero", map[string]string{envESResultLimit: "0"}, []string{envESResultLimit}},
		{"limit above the cap", map[string]string{envESResultLimit: "257"}, []string{envESResultLimit}},
		{"limit not a number", map[string]string{envESResultLimit: "ten"}, []string{envESResultLimit, `"ten"`}},
		{"timeout not a duration", map[string]string{envESTimeout: "fast"}, []string{envESTimeout, `"fast"`}},
		{"negative timeout", map[string]string{envESTimeout: "-1s"}, []string{envESTimeout}},
		{"zero timeout", map[string]string{envESTimeout: "0"}, []string{envESTimeout}},
		{"an unknown auth mode", map[string]string{envESAuth: "token"}, []string{envESAuth, `"token"`}},
		{"api_key without a key", key, []string{envESAuth, "api_key needs an API key"}},
		{"basic without a password", map[string]string{envESAuth: "basic", envESKey: "", envESUsername: "reader"}, []string{envESAuth}},
		{"a key under none", map[string]string{envESAuth: "none"}, []string{envESAuth, "none takes no credentials"}},
		{"a username under api_key", map[string]string{envESUsername: "reader"}, []string{envESAuth}},
		{"an unreadable key file", map[string]string{envESKey: "", envESKey + "_FILE": absent}, []string{envESKey + "_FILE", "absent"}},
		{"an unreadable password file", map[string]string{
			envESAuth: "basic", envESKey: "", envESUsername: "reader", envESUserKey + "_FILE": absent,
		}, []string{envESUserKey + "_FILE"}},
		{"no auth mode beside an unreadable key file", map[string]string{
			envESAuth: "", envESKey: "", envESKey + "_FILE": absent,
		}, []string{"requires " + envESAuth, envESKey + "_FILE"}},
		{"an unknown auth mode beside an unreadable key file", map[string]string{
			envESAuth: "token", envESKey: "", envESKey + "_FILE": absent,
		}, []string{envESAuth, `"token"`, envESKey + "_FILE"}},
		{"an unknown auth mode beside an unreadable password file", map[string]string{
			envESAuth: "token", envESKey: "", envESUserKey + "_FILE": absent,
		}, []string{envESAuth, `"token"`, envESUserKey + "_FILE"}},
		{"an unreadable CA file", map[string]string{envESCAFile: absent}, []string{envESCAFile, "absent"}},
		{"a CA file that is not PEM", map[string]string{envESCAFile: notPEM}, []string{envESCAFile}},
		{"an unreadable schema file", map[string]string{envESFilterSchema + "_FILE": absent}, []string{envESFilterSchema + "_FILE", "absent"}},
		{"a schema that lets any key through", map[string]string{
			envESFilterSchema: `{"type":"object","properties":{"news.language":{"type":"string"}}}`,
		}, []string{envESFilterSchema, "additionalProperties"}},
		{"several faults at once", map[string]string{
			envESIndex: "", envESResultLimit: "ten", envESTimeout: "fast",
		}, []string{envESIndex, envESResultLimit, envESTimeout}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			esWorkingEnv(t, es)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			s, err := buildQuerySearcher(context.Background(), testutil.DiscardLogger())
			assertBootRefusal(t, s, err, tc.want...)
			assertNoCredential(t, "the refusal", err.Error())
		})
	}
	if n := len(es.Requests()); n != 0 {
		t.Errorf("%d requests reached the cluster behind a refused configuration, want 0", n)
	}
}

func TestESConfigFromEnv_ReadsTheCAFileAndTheTimeout(t *testing.T) {
	es := essearchtest.NewServer(t)
	esWorkingEnv(t, es)
	t.Setenv(envESTimeout, "750ms")
	const pem = "-----BEGIN CERTIFICATE-----\n"
	path := writeTempFile(t, "ca.pem", pem)
	t.Setenv(envESCAFile, path)

	settings, err := esConfigFromEnv()

	if string(settings.cfg.CACertPEM) != pem {
		t.Errorf("CACertPEM = %q", settings.cfg.CACertPEM)
	}
	if settings.cfg.Timeout != 750*time.Millisecond {
		t.Errorf("Timeout = %v", settings.cfg.Timeout)
	}
	if err == nil || !strings.Contains(err.Error(), envESCAFile) {
		t.Errorf("err = %v, want the header alone refused as no certificate", err)
	}
}
