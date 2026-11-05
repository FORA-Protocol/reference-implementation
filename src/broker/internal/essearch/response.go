package essearch

import (
	"encoding/json"
	"errors"
	"fmt"
)

// searchResponse is the part of a _search/template answer the Broker reads.
// Shards and Hits are pointers because a 2xx body lacking them is not a search
// answer, and must not read as a search that matched nothing.
type searchResponse struct {
	Took     int64      `json:"took"`
	TimedOut bool       `json:"timed_out"`
	Shards   *shardInfo `json:"_shards"`
	Hits     *hitsBlock `json:"hits"`
}

type shardInfo struct {
	Failed int `json:"failed"`
}

type hitsBlock struct {
	Hits []map[string]any `json:"hits"`
}

// decodeResponse parses a 2xx body and refuses one that is not a complete
// answer.
func decodeResponse(raw []byte) (*searchResponse, error) {
	var r searchResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("decode answer: %w", err)
	}
	switch {
	case r.Shards == nil || r.Hits == nil || r.Hits.Hits == nil:
		return nil, errors.New("answer is not a search result")
	case r.TimedOut:
		return nil, errors.New("search timed out")
	case r.Shards.Failed > 0:
		return nil, fmt.Errorf("search failed on %d shards", r.Shards.Failed)
	}
	return &r, nil
}

// uris reads the URL of every hit, keeping the hit order and the first
// occurrence of a URL. The limit holds even if the cluster returned more hits
// than it was asked for, and a hit past it is not read. No hits is a nil
// result.
func (s *Searcher) uris(r *searchResponse) ([]string, error) {
	var out []string
	seen := make(map[string]bool, len(r.Hits.Hits))
	for _, h := range r.Hits.Hits {
		if len(out) == s.cfg.Limit {
			break
		}
		u, err := urlAt(h, s.path, s.cfg.HitURLPath)
		if err != nil {
			return nil, err
		}
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out, nil
}

// statusError is a non-2xx answer: the status, and the type and reason
// Elasticsearch reports when the body carries them. Neither echoes a credential,
// but the reason is the cluster's own text and can quote the request's query text
// or a filter value, for example a date or a number it could not parse.
type statusError struct {
	status int
	cause  esCause
}

func (e *statusError) Error() string {
	return fmt.Sprintf("status %d %s: %s", e.status, e.cause.Type, e.cause.Reason)
}

// esCause is one cause in an Elasticsearch error body.
type esCause struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// newStatusError reads the error body. It prefers the first root cause, because
// a shard-level fault reports only "all shards failed" at the top level. A body
// that is not Elasticsearch's error shape, such as a proxy's page, leaves only
// the status.
func newStatusError(status int, raw []byte) *statusError {
	var body struct {
		Error struct {
			esCause
			RootCause []esCause `json:"root_cause"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return &statusError{status: status}
	}
	if len(body.Error.RootCause) > 0 {
		return &statusError{status: status, cause: body.Error.RootCause[0]}
	}
	return &statusError{status: status, cause: body.Error.esCause}
}
