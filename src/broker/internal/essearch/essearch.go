package essearch

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
)

var (
	// ErrUnavailable wraps every Search failure: a connection failure, a timeout,
	// a refused request, a partial result or an unreadable answer.
	ErrUnavailable = errors.New("catalog search unavailable")
	// ErrMalformedHit is wrapped inside ErrUnavailable when a hit has no usable
	// URL. The index contract is broken, so the whole search fails rather than
	// answering with what remains.
	ErrMalformedHit = errors.New("search hit carries no usable URL")
)

// Searcher resolves a query and the request's filters to catalog URIs through
// the deployment's search template. It is also the filter vocabulary.
type Searcher struct {
	cfg       Config
	http      *http.Client
	path      []string
	searchURL string
	renderURL string
}

var _ catalogsearch.Searcher = (*Searcher)(nil)

// New validates cfg and builds the client. It dials nothing: Probe is the call
// that reaches the cluster.
func New(cfg Config) (*Searcher, error) {
	if problems := cfg.Validate(); len(problems) > 0 {
		errs := make([]error, len(problems))
		for i, p := range problems {
			errs[i] = p
		}
		return nil, fmt.Errorf("essearch: %w", errors.Join(errs...))
	}
	searchURL, err := url.JoinPath(cfg.Endpoint, cfg.Index, "_search", "template")
	if err != nil {
		return nil, fmt.Errorf("essearch: search URL: %w", err)
	}
	renderURL, err := url.JoinPath(cfg.Endpoint, "_render", "template")
	if err != nil {
		return nil, fmt.Errorf("essearch: render URL: %w", err)
	}
	return &Searcher{
		cfg:       cfg,
		http:      newHTTPClient(cfg),
		path:      strings.Split(cfg.HitURLPath, "."),
		searchURL: searchURL,
		renderURL: renderURL,
	}, nil
}

// Parse admits the request's search_filters against the deployment's schema: a
// key the schema does not declare is unsupported, a value that breaks its key's
// rule is malformed, and with no schema every key is unsupported. The refusal
// names the key and the rule, never the value. The accepted object is what
// Search hands the template.
func (s *Searcher) Parse(raw map[string]any) (catalogsearch.Filters, error) {
	if v := s.cfg.Filters.Check(raw); v != nil {
		return nil, &catalogsearch.FilterError{Key: v.Key, Unsupported: v.Unsupported, Reason: v.Constraint}
	}
	return Filters(maps.Clone(raw)), nil
}

// Search runs the template with the query and the filters Parse accepted, and
// returns the URL of each hit, best first, at most Config.Limit and without
// duplicates. An empty result means nothing matched. Filters another
// vocabulary parsed are catalogsearch.ErrForeignFilters, before anything is
// sent. Any failure of the search wraps ErrUnavailable: a result that may be
// incomplete is not an answer.
func (s *Searcher) Search(ctx context.Context, query string, f catalogsearch.Filters) ([]string, error) {
	filters, ok := f.(Filters)
	if !ok {
		return nil, catalogsearch.ErrForeignFilters
	}
	result, err := s.search(ctx, s.request(query, filters))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	uris, err := s.uris(result)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	// This line logs neither the query text nor a filter value: both are the
	// agent's. A failed search is logged by the caller with the cluster's own
	// reason, which can quote either.
	reqctx.FromContext(ctx).InfoContext(ctx, "broker.es.search",
		"index", s.cfg.Index, "hits", len(uris), "took_ms", result.Took)
	return uris, nil
}

// search sends one _search/template call and returns the parsed answer.
func (s *Searcher) search(ctx context.Context, body templateRequest) (*searchResponse, error) {
	raw, err := s.post(ctx, s.searchURL, body)
	if err != nil {
		return nil, err
	}
	return decodeResponse(raw)
}
