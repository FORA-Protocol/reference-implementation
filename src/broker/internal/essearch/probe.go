package essearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

var (
	// ErrCredentials is the cluster refusing the configured credentials or their
	// access to the index. It does not heal by waiting.
	ErrCredentials = errors.New("elasticsearch refused the credentials")
	// ErrUnreachable is a cluster that did not answer usefully: a connection
	// failure, a timeout or a server error. It can heal without the Broker
	// changing.
	ErrUnreachable = errors.New("elasticsearch unreachable")
	// ErrMisconfigured is a cluster that answered, but cannot serve the configured
	// search: the stored template is missing, the template does not render, the
	// index is missing, or a hit carries no URL at the configured path.
	ErrMisconfigured = errors.New("elasticsearch template, index or hit path unusable")
)

// probeQuery is the query text of the boot probe. It exercises the template's
// query branch; it is not expected to match anything.
const probeQuery = "fora boot probe"

// Probe checks the configuration against the cluster in two calls, under the
// same parameters a search sends, with a limit of one. The first renders the
// template without searching, so a missing stored template or a template that
// does not render as a JSON object is caught on its own. The second runs it,
// and when it returns a hit, the hit must carry a URL at the configured path.
// Both need only the read privilege on the index.
//
// The error wraps ErrCredentials, ErrUnreachable or ErrMisconfigured.
func (s *Searcher) Probe(ctx context.Context) error {
	body := s.request(probeQuery, nil)
	body.Params.Limit = 1
	if err := s.render(ctx, body); err != nil {
		return classify("render the template", err)
	}
	result, err := s.search(ctx, body)
	if err != nil {
		return classify("search", err)
	}
	if len(result.Hits.Hits) > 0 {
		if _, err := urlAt(result.Hits.Hits[0], s.path, s.cfg.HitURLPath); err != nil {
			return fmt.Errorf("%w: %w", ErrMisconfigured, err)
		}
	}
	return nil
}

// render sends one _render/template call. An answer without template_output is
// not Elasticsearch's; an output that is not a JSON object is a template that
// cannot be a search body.
func (s *Searcher) render(ctx context.Context, body templateRequest) error {
	raw, err := s.post(ctx, s.renderURL, body)
	if err != nil {
		return err
	}
	var rendered struct {
		Output json.RawMessage `json:"template_output"`
	}
	if err := json.Unmarshal(raw, &rendered); err != nil {
		return fmt.Errorf("decode render answer: %w", err)
	}
	if rendered.Output == nil {
		return errors.New("answer is not a render result")
	}
	var output map[string]any
	if err := json.Unmarshal(rendered.Output, &output); err != nil || output == nil {
		return fmt.Errorf("%w: the template does not render as a JSON object", ErrMisconfigured)
	}
	return nil
}

// classify sorts a failed probe call. Refused credentials are the operator's
// own setting. A 400 is a template the cluster cannot render or parse, and a
// 404 a stored template or an index that does not exist: both are the
// configuration, on either side. Anything else can heal by itself.
func classify(step string, err error) error {
	if errors.Is(err, ErrMisconfigured) {
		return fmt.Errorf("%s: %w", step, err)
	}
	var refused *statusError
	if !errors.As(err, &refused) {
		return fmt.Errorf("%w: %s: %w", ErrUnreachable, step, err)
	}
	switch refused.status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s: %w", ErrCredentials, step, err)
	case http.StatusBadRequest, http.StatusNotFound:
		return fmt.Errorf("%w: %s: %w", ErrMisconfigured, step, err)
	default:
		return fmt.Errorf("%w: %s: %w", ErrUnreachable, step, err)
	}
}
