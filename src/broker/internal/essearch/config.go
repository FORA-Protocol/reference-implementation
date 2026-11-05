package essearch

import (
	"crypto/x509"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/searchfilters"
)

// maxResultLimit is the most URIs one search may return, because the URIs travel
// on in ResourceQuery.uris, which the protocol caps at 256.
const maxResultLimit = 256

// AuthMode is how the Broker authenticates to the cluster. It is always stated:
// a cluster with security off is a deployment's choice to write down, not a
// default to fall into.
type AuthMode string

// The authentication modes.
const (
	// AuthNone sends no credentials.
	AuthNone AuthMode = "none"
	// AuthAPIKey sends an API key, the base64 of id:api_key.
	AuthAPIKey AuthMode = "api_key"
	// AuthBasic sends a username and password.
	AuthBasic AuthMode = "basic"
)

// Config is one deployment's search settings. Validate reports what is wrong
// with it.
type Config struct {
	// Endpoint is the cluster's base URL: https, or http when AllowHTTP is set.
	// It carries no credentials, query or fragment.
	Endpoint string
	// Index is one index or alias, never a pattern or a list: a pattern would
	// widen what the Broker reads beyond the catalog agreed with the index owner.
	Index string
	// TemplateID names a search template stored in the cluster; TemplateSource
	// is the template's Mustache text, sent with every search. Exactly one is
	// set. A source must not use a tag in refusedTags, each of which would let
	// the agent's text become query syntax.
	TemplateID     string
	TemplateSource string
	// HitURLPath is where a hit carries its URL, as dotted names: _source.url,
	// or fields.url for a projected or computed field. An array at any step
	// gives its first element.
	HitURLPath string
	// Limit is the most URIs one search returns, between 1 and 256. It is also
	// the template's limit parameter.
	Limit int
	// Timeout bounds one call to the cluster: it is the deadline on the Broker's
	// own request.
	Timeout time.Duration
	// Auth is the authentication mode. APIKey goes with AuthAPIKey, Username and
	// Password with AuthBasic, and no mode takes the other mode's credentials.
	Auth     AuthMode
	APIKey   string
	Username string
	Password string
	// CACertPEM, when set, is the only trust root for this connection. Nil means
	// the system roots.
	CACertPEM []byte
	// AllowHTTP admits an http Endpoint: the deployment's dedicated plain-http
	// opt-in, which relaxes nothing else.
	AllowHTTP bool
	// Filters is the deployment's filter vocabulary. Nil means the search applies
	// no filter, and every search_filters key is refused.
	Filters *searchfilters.Schema
}

// Field names one Config field in a Problem, so the caller can name the setting
// behind it.
type Field string

// The Config fields a Problem can name.
const (
	FieldEndpoint   Field = "endpoint"
	FieldIndex      Field = "index"
	FieldTemplate   Field = "template"
	FieldHitURLPath Field = "hit_url_path"
	FieldLimit      Field = "limit"
	FieldTimeout    Field = "timeout"
	FieldAuth       Field = "auth"
	FieldCACert     Field = "ca_cert"
)

// Problem is one invalid Config field. Its text never carries a credential.
type Problem struct {
	Field  Field
	Reason string
}

func (p Problem) Error() string {
	return string(p.Field) + ": " + p.Reason
}

// Validate returns every problem with c, in the order of the fields. No problems
// means New accepts c.
func (c Config) Validate() []Problem {
	checks := []Problem{
		{FieldEndpoint, endpointProblem(c.Endpoint, c.AllowHTTP)},
		{FieldIndex, indexProblem(c.Index)},
		{FieldTemplate, templateProblem(c.TemplateID, c.TemplateSource)},
		{FieldHitURLPath, hitPathProblem(c.HitURLPath)},
		{FieldLimit, limitProblem(c.Limit)},
		{FieldTimeout, timeoutProblem(c.Timeout)},
		{FieldAuth, authProblem(c)},
		{FieldCACert, caProblem(c.CACertPEM)},
	}
	return slices.DeleteFunc(checks, func(p Problem) bool { return p.Reason == "" })
}

// endpointProblem keeps the parse error out of its reason: url.Parse quotes its
// input, and a mistyped URL can be one carrying a password.
func endpointProblem(raw string, allowHTTP bool) string {
	u, err := url.Parse(raw)
	switch {
	case raw == "":
		return "is required"
	case err != nil:
		return "is not a URL"
	case u.User != nil:
		return "must not carry credentials"
	case u.Host == "":
		return "needs a scheme and a host"
	case u.RawQuery != "" || u.Fragment != "":
		return "must not carry a query or a fragment"
	case u.Scheme == "https", u.Scheme == "http" && allowHTTP:
		return ""
	case u.Scheme == "http":
		return "plain http needs the plain-http opt-in"
	default:
		return fmt.Sprintf("scheme %q: want https", u.Scheme)
	}
}

// indexProblem refuses the characters Elasticsearch does not allow in a name,
// which also makes the name select one target. The request path is built from
// this value, so the separators matter as much as the patterns.
func indexProblem(name string) string {
	switch {
	case name == "":
		return "is required"
	case name == "." || name == "..":
		return "is not an index name"
	case strings.ContainsAny(name, "\\/*?\"<>|,#: \t\r\n"):
		return "must name one index or alias, not a pattern or a list"
	case strings.ContainsAny(name[:1], "_-+"):
		return "must not start with _, - or +"
	default:
		return ""
	}
}

// refusedTags are the Mustache tags a template source may not use, with what
// each does and what to write instead. Every value the Broker hands a template
// except the limit is the agent's, so each of them would let the agent's text
// become query syntax. Checked on Elasticsearch 7.15.0: {{{ }}} and {{& }}
// insert a value unescaped; {{= }} changes the delimiters, under which the same
// forms can be written with the new ones; and the join lambda writes the values
// it joins unescaped. Matching the prefix {{#join also refuses a section named
// like it, which could only test a parameter the Broker never sends. On that
// version a space after the opening braces made none of these a tag.
var refusedTags = []struct{ tag, does, instead string }{
	{"{{{", "inserts a value unescaped", "use {{name}}"},
	{"{{&", "inserts a value unescaped", "use {{name}}"},
	{
		"{{=", "changes the delimiters, under which a value can be inserted unescaped",
		"keep the default delimiters and use {{name}}",
	},
	{"{{#join", "writes the values it joins unescaped", "use {{#toJson}} for an array"},
}

func templateProblem(id, source string) string {
	switch {
	case id == "" && source == "":
		return "is required: a stored template id, or the template's text"
	case id != "" && source != "":
		return "set one: a stored template id, or the template's text"
	case strings.ContainsAny(id, "/ \t\r\n"):
		return "the stored template id must not contain a slash or whitespace"
	}
	for _, r := range refusedTags {
		if strings.Contains(source, r.tag) {
			return fmt.Sprintf("the template uses %s, which %s; %s", r.tag, r.does, r.instead)
		}
	}
	return ""
}

func hitPathProblem(path string) string {
	switch {
	case path == "":
		return "is required"
	case slices.Contains(strings.Split(path, "."), ""):
		return "has an empty name between dots"
	default:
		return ""
	}
}

func limitProblem(limit int) string {
	if limit < 1 || limit > maxResultLimit {
		return fmt.Sprintf("%d: want 1 to %d", limit, maxResultLimit)
	}
	return ""
}

func timeoutProblem(timeout time.Duration) string {
	if timeout <= 0 {
		return "must be positive"
	}
	return ""
}

// authProblem checks that the mode is stated and has exactly its own
// credentials. A credential the mode does not use is refused rather than
// ignored: it means the deployment believes it authenticates in a way it does
// not.
func authProblem(c Config) string {
	hasKey, hasUser, hasPassword := c.APIKey != "", c.Username != "", c.Password != ""
	switch c.Auth {
	case "":
		return "is required: none, api_key or basic"
	case AuthNone:
		if hasKey || hasUser || hasPassword {
			return "none takes no credentials"
		}
	case AuthAPIKey:
		switch {
		case !hasKey:
			return "api_key needs an API key"
		case hasUser || hasPassword:
			return "api_key takes no username or password"
		}
	case AuthBasic:
		switch {
		case !hasUser || !hasPassword:
			return "basic needs a username and a password"
		case hasKey:
			return "basic takes no API key"
		}
	default:
		return fmt.Sprintf("%q: want none, api_key or basic", string(c.Auth))
	}
	return ""
}

func caProblem(pem []byte) string {
	if pem != nil && !x509.NewCertPool().AppendCertsFromPEM(pem) {
		return "holds no PEM certificate"
	}
	return ""
}
