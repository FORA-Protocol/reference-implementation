package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/searchfilters"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
)

// Settings of the elasticsearch catalog search provider. The result limit has no
// default on purpose: how many resources one search returns is the deployment's
// product decision, and Elasticsearch's own default of 10 must not become that
// number by accident. The template, the hit path and the authentication mode
// have none because each is the deployment's statement about its own index.
const (
	envESURL            = "BROKER_ES_URL"
	envESAllowHTTP      = "BROKER_ES_ALLOW_HTTP"
	envESIndex          = "BROKER_ES_INDEX"
	envESTemplateID     = "BROKER_ES_TEMPLATE_ID"
	envESTemplateFile   = "BROKER_ES_TEMPLATE_FILE"
	envESHitURLPath     = "BROKER_ES_HIT_URL_PATH"
	envESResultLimit    = "BROKER_ES_RESULT_LIMIT"
	envESTimeout        = "BROKER_ES_TIMEOUT"
	envESAuth           = "BROKER_ES_AUTH"
	envESFilterSchema   = "BROKER_ES_FILTERS_SCHEMA"
	envESCAFile         = "BROKER_ES_CA_FILE"
	envESUsername       = "BROKER_ES_USERNAME"
	envESTemplateEither = envESTemplateID + " or " + envESTemplateFile
	// These hold variable names, not credentials. The constants avoid the words
	// the hardcoded-credential lint reads as a secret.
	envESKey     = "BROKER_ES_API_KEY"
	envESUserKey = "BROKER_ES_PASSWORD"
)

// defaultESTimeout bounds one call when the deployment sets none.
const defaultESTimeout = 3 * time.Second

// maxTemplateFileBytes bounds the template file, which is sent with every search.
const maxTemplateFileBytes = 64 << 10

// esFieldSetting names the settings behind each Config field, so a refusal names
// what the operator can change.
var esFieldSetting = map[essearch.Field]string{
	essearch.FieldEndpoint:   envESURL + " (" + envESAllowHTTP + " for plain http)",
	essearch.FieldIndex:      envESIndex,
	essearch.FieldTemplate:   envESTemplateEither,
	essearch.FieldHitURLPath: envESHitURLPath,
	essearch.FieldLimit:      envESResultLimit,
	essearch.FieldTimeout:    envESTimeout,
	essearch.FieldAuth:       envESAuth + " and its credentials",
	essearch.FieldCACert:     envESCAFile,
}

// esFaults collects what is wrong with the settings, so one refusal lists them
// all and a deployment is fixed in one pass.
type esFaults struct {
	missing []string
	invalid []string
	seen    map[essearch.Field]bool
}

// miss records a required setting that is not set.
func (f *esFaults) miss(field essearch.Field, setting string) {
	f.seen[field] = true
	f.missing = append(f.missing, setting)
}

func (f *esFaults) invalidate(field essearch.Field, reason string) {
	f.fail(field, esFieldSetting[field]+": "+reason)
}

// fail records a fault whose text already names the setting.
func (f *esFaults) fail(field essearch.Field, text string) {
	f.seen[field] = true
	f.note(text)
}

// note records a fault that belongs to no Config field.
func (f *esFaults) note(text string) {
	f.invalid = append(f.invalid, text)
}

// err renders the faults, or nil when there are none. The text comes from
// essearch, searchfilters, runhttp or this file, and none of them quotes a
// credential.
func (f *esFaults) err() error {
	var parts []string
	if len(f.missing) > 0 {
		parts = append(parts, "requires "+strings.Join(f.missing, ", "))
	}
	parts = append(parts, f.invalid...)
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("%s=%s: %s", envQueryProvider, providerElasticsearch, strings.Join(parts, "; "))
}

// esSettings is the Config plus how the template was given, for the log.
type esSettings struct {
	cfg      essearch.Config
	template string
}

// esConfigFromEnv reads the settings. Every setting that is missing or invalid
// is named in the one error it returns.
func esConfigFromEnv() (esSettings, error) {
	faults := &esFaults{seen: map[essearch.Field]bool{}}
	cfg := essearch.Config{
		Endpoint:   runhttp.EnvTrimmed(envESURL),
		Index:      runhttp.EnvTrimmed(envESIndex),
		HitURLPath: runhttp.EnvTrimmed(envESHitURLPath),
		AllowHTTP:  runhttp.EnvOptIn(envESAllowHTTP),
	}
	requireESSetting(faults, cfg.Endpoint, essearch.FieldEndpoint, envESURL)
	requireESSetting(faults, cfg.Index, essearch.FieldIndex, envESIndex)
	var template string
	cfg.TemplateID, cfg.TemplateSource, template = esTemplate(faults)
	requireESSetting(faults, cfg.HitURLPath, essearch.FieldHitURLPath, envESHitURLPath)
	cfg.Limit = esResultLimit(faults)
	cfg.Timeout = esTimeout(faults)
	esAuth(faults, &cfg)
	cfg.CACertPEM = esCACert(faults)
	cfg.Filters = esFilterSchema(faults)
	// Validate holds the rules. It reports a field already faulted a second time,
	// usually as "is required", so only the fields read cleanly above count.
	for _, p := range cfg.Validate() {
		if !faults.seen[p.Field] {
			faults.invalidate(p.Field, p.Reason)
		}
	}
	return esSettings{cfg: cfg, template: template}, faults.err()
}

func requireESSetting(faults *esFaults, value string, field essearch.Field, setting string) {
	if value == "" {
		faults.miss(field, setting)
	}
}

// esTemplate reads the stored template's id, or the template file, which must
// be set alone. The returned label says which, for the log: id:<id> or
// file:<path>.
func esTemplate(faults *esFaults) (id, source, label string) {
	id = runhttp.EnvTrimmed(envESTemplateID)
	path := runhttp.EnvTrimmed(envESTemplateFile)
	switch {
	case id == "" && path == "":
		faults.miss(essearch.FieldTemplate, envESTemplateEither)
		return "", "", ""
	case id != "" && path != "":
		faults.fail(essearch.FieldTemplate, fmt.Sprintf("set one of %s and %s, not both", envESTemplateID, envESTemplateFile))
		return id, "", ""
	case id != "":
		return id, "", "id:" + id
	}
	raw, err := runhttp.EnvFile(envESTemplateFile)
	switch {
	case err != nil:
		faults.fail(essearch.FieldTemplate, err.Error())
	case len(bytes.TrimSpace(raw)) == 0:
		faults.fail(essearch.FieldTemplate, envESTemplateFile+": the file is empty")
	case len(raw) > maxTemplateFileBytes:
		faults.fail(essearch.FieldTemplate,
			fmt.Sprintf("%s: the file is over %d KiB", envESTemplateFile, maxTemplateFileBytes>>10))
	}
	return "", string(raw), "file:" + path
}

func esResultLimit(faults *esFaults) int {
	raw := runhttp.EnvTrimmed(envESResultLimit)
	if raw == "" {
		faults.miss(essearch.FieldLimit, envESResultLimit)
		return 0
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		faults.invalidate(essearch.FieldLimit, fmt.Sprintf("%q is not a whole number", raw))
	}
	return limit
}

// esTimeout parses strictly: unlike runhttp.EnvDuration it does not read a typo
// as the default.
func esTimeout(faults *esFaults) time.Duration {
	raw := runhttp.EnvTrimmed(envESTimeout)
	if raw == "" {
		return defaultESTimeout
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil {
		faults.invalidate(essearch.FieldTimeout, fmt.Sprintf("%q is not a duration", raw))
	}
	return timeout
}

// esAuth reads the mode and every credential setting. Which credentials go with
// which mode is essearch's rule, applied by Validate; a credential set under
// another mode is refused there. The key and the password are read inline or
// from their _FILE companion, and runhttp.EnvOrFile's error names the variable
// and the path, never the content. A file that cannot be read is a fault of its
// own, kept apart from the mode, so a missing or unknown mode is named in the
// same refusal.
func esAuth(faults *esFaults, cfg *essearch.Config) {
	cfg.Auth = essearch.AuthMode(runhttp.EnvTrimmed(envESAuth))
	cfg.Username = runhttp.EnvTrimmed(envESUsername)
	var err error
	if cfg.APIKey, err = runhttp.EnvOrFile(envESKey); err != nil {
		faults.note(err.Error())
	}
	if cfg.Password, err = runhttp.EnvOrFile(envESUserKey); err != nil {
		faults.note(err.Error())
	}
	if cfg.Auth == "" {
		faults.miss(essearch.FieldAuth, envESAuth)
	}
}

// esCACert reads the CA bundle, or returns nil for the system roots.
func esCACert(faults *esFaults) []byte {
	pem, err := runhttp.EnvFile(envESCAFile)
	if err != nil {
		faults.fail(essearch.FieldCACert, err.Error())
	}
	return pem
}

// esFilterSchema reads and loads the filter schema, inline or from its _FILE
// companion. Unset is no schema: the search then applies no filter.
func esFilterSchema(faults *esFaults) *searchfilters.Schema {
	raw, err := runhttp.EnvOrFile(envESFilterSchema)
	if err != nil {
		faults.note(err.Error())
		return nil
	}
	schema, err := searchfilters.Load([]byte(raw))
	if err != nil {
		faults.note(envESFilterSchema + ": " + err.Error())
	}
	return schema
}

// buildElasticsearchSearcher returns the search over the configured index. It
// needs neither the model's credentials nor a catalog file. The boot probe
// refuses to start only on refused credentials, the operator's own setting.
// Anything else can be the index owner's side, so the Broker starts and queries
// answer TEMPORARILY_UNAVAILABLE until it heals: an outage of the index must not
// take down URI requests and purchases with it.
func buildElasticsearchSearcher(ctx context.Context, logger *slog.Logger) (catalogsearch.Searcher, error) {
	settings, err := esConfigFromEnv()
	if err != nil {
		return nil, err
	}
	cfg := settings.cfg
	searcher, err := essearch.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("%s=%s: %w", envQueryProvider, providerElasticsearch, err)
	}
	logEnabled(logger, settings)
	if err := probeElasticsearch(ctx, searcher, cfg.Auth, logger); err != nil {
		return nil, err
	}
	return searcher, nil
}

// logEnabled records the configuration the Broker runs with, and warns about the
// two choices that leave the cluster connection unprotected. The endpoint
// carries no credential (Validate refuses userinfo), and only the mode of the
// credentials is logged.
func logEnabled(logger *slog.Logger, settings esSettings) {
	cfg := settings.cfg
	filterKeys := "none"
	if keys := cfg.Filters.Keys(); len(keys) > 0 {
		filterKeys = strings.Join(keys, ",")
	}
	logger.Info("broker.query_discovery.enabled", "provider", providerElasticsearch,
		"endpoint", cfg.Endpoint, "index", cfg.Index, "template", settings.template,
		"hit_url_path", cfg.HitURLPath, "limit", cfg.Limit, "timeout", cfg.Timeout.String(),
		"auth", string(cfg.Auth), "filter_keys", filterKeys)
	if strings.HasPrefix(cfg.Endpoint, "http://") {
		logger.Warn("broker.es.plain_http", "endpoint", cfg.Endpoint)
	}
	if cfg.Auth == essearch.AuthNone {
		logger.Warn("broker.es.no_auth", "endpoint", cfg.Endpoint)
	}
}

// esCredentialSetting names what to change when the cluster refuses the
// credentials of a mode.
var esCredentialSetting = map[essearch.AuthMode]string{
	essearch.AuthAPIKey: envESKey,
	essearch.AuthBasic:  envESUsername + " and " + envESUserKey,
	essearch.AuthNone:   envESAuth + " (the cluster asks for credentials)",
}

// probeElasticsearch runs the boot probe and returns an error only for the
// outcome that refuses to start.
func probeElasticsearch(
	ctx context.Context, searcher *essearch.Searcher, auth essearch.AuthMode, logger *slog.Logger,
) error {
	err := searcher.Probe(ctx)
	switch {
	case err == nil:
		logger.Info("broker.es.ready")
	case errors.Is(err, essearch.ErrCredentials):
		return fmt.Errorf("%s: %w", esCredentialSetting[auth], err)
	case errors.Is(err, essearch.ErrMisconfigured):
		logger.Warn("broker.es.misconfigured", "err", err)
	default:
		logger.Warn("broker.es.unreachable", "err", err)
	}
	return nil
}
