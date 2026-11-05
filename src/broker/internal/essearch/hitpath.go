package essearch

import (
	"fmt"
	"net/url"
)

// urlAt returns the URL a hit carries at path, the dotted names of
// Config.HitURLPath split apart. An array at any step stands for its first
// element, because a projected field (fields.url) always comes back as one.
// The Broker takes the host from the URL to route the request, so a value
// without a scheme and a host can never be a catalog URI.
func urlAt(hit map[string]any, path []string, dotted string) (string, error) {
	var node any = hit
	for _, name := range path {
		obj, ok := first(node).(map[string]any)
		if !ok {
			return "", malformedHit(hit, dotted)
		}
		node = obj[name]
	}
	value, ok := first(node).(string)
	if !ok {
		return "", malformedHit(hit, dotted)
	}
	if u, err := url.Parse(value); err != nil || u.Scheme == "" || u.Host == "" {
		return "", malformedHit(hit, dotted)
	}
	return value, nil
}

// first is an array's first element, nil for an empty array, and any other
// value unchanged.
func first(v any) any {
	if list, ok := v.([]any); ok {
		if len(list) == 0 {
			return nil
		}
		return list[0]
	}
	return v
}

// malformedHit names the hit by its _id and the path that held no usable URL.
// The value is not quoted: it is index content, and an error is logged.
func malformedHit(hit map[string]any, dotted string) error {
	id, _ := hit["_id"].(string)
	return fmt.Errorf("%w: hit %q has none at %s", ErrMalformedHit, id, dotted)
}
