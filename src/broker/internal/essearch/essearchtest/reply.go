package essearchtest

import (
	"encoding/json"
	"net/http"
	"time"
)

// Reply scripts the answer to every request of one kind until the next Script
// or ScriptRender call. The zero value is a success: a search with no hits, or
// the default rendered template.
type Reply struct {
	// URLs are the hits, in order, each carrying its URL at _source.url.
	URLs []string
	// Hits are whole hit objects appended after URLs, for a hit whose URL sits
	// elsewhere or that the index contract does not allow.
	Hits []map[string]any
	// Status replaces the 200 answer; ErrorType and ErrorReason fill the
	// Elasticsearch error body sent with it.
	Status      int
	ErrorType   string
	ErrorReason string
	// TimedOut and FailedShards mark a search answer as incomplete.
	TimedOut     bool
	FailedShards int
	// RawBody replaces the JSON body altogether.
	RawBody string
	// Delay holds the answer back, until the caller gives up if it is sooner.
	Delay time.Duration
	// RedirectTo answers 302 with this Location.
	RedirectTo string
}

// defaultRender is what _render/template answers when nothing is scripted.
const defaultRender = `{"template_output":{"query":{"match_all":{}}}}`

// searchBody renders the scripted answer in the shape of a _search response, or
// of an error response when the reply carries a status.
func (r Reply) searchBody() string {
	if body, ok := r.override(); ok {
		return body
	}
	hits := make([]map[string]any, 0, len(r.URLs)+len(r.Hits))
	for _, u := range r.URLs {
		hits = append(hits, map[string]any{"_id": u, "_source": map[string]any{"url": u}})
	}
	hits = append(hits, r.Hits...)
	return mustJSON(map[string]any{
		"took":      1,
		"timed_out": r.TimedOut,
		"_shards":   map[string]any{"total": 1, "successful": 1 - r.FailedShards, "failed": r.FailedShards},
		"hits":      map[string]any{"hits": hits},
	})
}

// renderBody renders the scripted answer to _render/template.
func (r Reply) renderBody() string {
	if body, ok := r.override(); ok {
		return body
	}
	return defaultRender
}

// override is the raw body, or the error body for a scripted status.
func (r Reply) override() (string, bool) {
	switch {
	case r.RawBody != "":
		return r.RawBody, true
	case r.Status >= http.StatusMultipleChoices:
		return errorBody(r.Status, r.ErrorType, r.ErrorReason), true
	default:
		return "", false
	}
}

// errorBody is Elasticsearch's error shape.
func errorBody(status int, errorType, reason string) string {
	return mustJSON(map[string]any{
		"error":  map[string]any{"type": errorType, "reason": reason},
		"status": status,
	})
}

func mustJSON(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err) // the values above always marshal
	}
	return string(out)
}
