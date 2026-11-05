package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/bedrock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

// Settings of the llm catalog search provider: the Broker resolves a query
// against the catalog feed through the model, and the catalog file and model are
// required.
const (
	envQueryCatalogFile = "BROKER_QUERY_CATALOG_FILE"
	envQueryModel       = "BROKER_QUERY_MODEL"
	// envQueryURIScheme is the scheme of the catalog URIs, https by default. It
	// must match the scheme the Exchange uses for the same catalog.
	envQueryURIScheme = "BROKER_QUERY_URI_SCHEME"
)

type queryConfig struct {
	catalogFile string
	model       string
	uriScheme   string
}

// queryConfigFromEnv reads the settings. A setting that is missing or invalid is
// an error naming what is wrong.
func queryConfigFromEnv() (queryConfig, error) {
	cfg := queryConfig{
		catalogFile: runhttp.EnvTrimmed(envQueryCatalogFile),
		model:       runhttp.EnvTrimmed(envQueryModel),
		uriScheme:   runhttp.EnvOr(envQueryURIScheme, "https"),
	}
	if cfg.uriScheme != "https" && cfg.uriScheme != "http" {
		return cfg, fmt.Errorf("%s=%q: want https or http", envQueryURIScheme, cfg.uriScheme)
	}
	var missing []string
	if cfg.catalogFile == "" {
		missing = append(missing, envQueryCatalogFile)
	}
	if cfg.model == "" {
		missing = append(missing, envQueryModel)
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("%s=%s requires %s", envQueryProvider, providerLLM, strings.Join(missing, " and "))
	}
	return cfg, nil
}

// buildLLMSearcher returns the tag search over the catalog feed.
func buildLLMSearcher(ctx context.Context, logger *slog.Logger) (catalogsearch.Searcher, error) {
	cfg, err := queryConfigFromEnv()
	if err != nil {
		return nil, err
	}
	index, err := tagsearch.LoadFile(cfg.catalogFile, cfg.uriScheme)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", envQueryCatalogFile, err)
	}
	extractor, err := bedrock.NewFromDefaultConfig(ctx, cfg.model)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%s=%s", envQueryProvider, providerLLM), err)
	}
	logger.Info("broker.query_discovery.enabled", "provider", providerLLM,
		"catalog_file", cfg.catalogFile, "tracks", index.Len(), "model", cfg.model)
	return tagsearch.NewSearcher(index, extractor), nil
}
