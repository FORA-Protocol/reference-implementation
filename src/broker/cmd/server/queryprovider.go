package main

import (
	"context"
	"fmt"
	"log/slog"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
)

// envQueryProvider selects the Broker's catalog search. A deployment runs one
// provider, and a failure in it never falls back to another.
const envQueryProvider = "BROKER_QUERY_PROVIDER"

const (
	// providerNone is the default: no catalog search, so a free-text query goes
	// to the search provider, as it always has.
	providerNone = "none"
	// providerLLM resolves a query through the model and the in-memory tag index.
	providerLLM = "llm"
	// providerElasticsearch resolves a query through an index someone else owns.
	providerElasticsearch = "elasticsearch"
)

// buildQuerySearcher returns the selected catalog search, or nil for
// providerNone. Any configuration problem is an error, so the Broker refuses to
// start rather than serve queries it cannot answer.
func buildQuerySearcher(ctx context.Context, logger *slog.Logger) (catalogsearch.Searcher, error) {
	switch v := runhttp.EnvTrimmed(envQueryProvider); v {
	case "", providerNone:
		logger.Info("broker.query_discovery.disabled")
		return nil, nil
	case providerLLM:
		return buildLLMSearcher(ctx, logger)
	case providerElasticsearch:
		return buildElasticsearchSearcher(ctx, logger)
	default:
		return nil, fmt.Errorf("%s=%q: want %s, %s or %s",
			envQueryProvider, v, providerNone, providerLLM, providerElasticsearch)
	}
}
