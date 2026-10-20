package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/bedrock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/resolve"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

// Query discovery settings. With the switch off (the default) a free-text
// query goes to the search provider, as it always has. With it on, the Broker
// resolves the query against the catalog feed through the model, and the
// catalog file and model are required.
const (
	envQueryDiscovery   = "BROKER_QUERY_DISCOVERY"
	envQueryCatalogFile = "BROKER_QUERY_CATALOG_FILE"
	envQueryModel       = "BROKER_QUERY_MODEL"
	// envQueryURIScheme is the scheme of the catalog URIs, https by default. It
	// must match the scheme the Exchange uses for the same catalog.
	envQueryURIScheme = "BROKER_QUERY_URI_SCHEME"
)

type queryConfig struct {
	enabled     bool
	catalogFile string
	model       string
	uriScheme   string
}

// queryConfigFromEnv reads the settings. An unknown switch value, or the
// switch on with a setting missing, is an error naming what is wrong.
func queryConfigFromEnv() (queryConfig, error) {
	cfg := queryConfig{
		catalogFile: runhttp.EnvTrimmed(envQueryCatalogFile),
		model:       runhttp.EnvTrimmed(envQueryModel),
		uriScheme:   runhttp.EnvOr(envQueryURIScheme, "https"),
	}
	switch v := runhttp.EnvTrimmed(envQueryDiscovery); v {
	case "", "off":
		return cfg, nil
	case "on":
		cfg.enabled = true
	default:
		return cfg, fmt.Errorf("%s=%q: want on or off", envQueryDiscovery, v)
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
		return cfg, fmt.Errorf("%s=on requires %s", envQueryDiscovery, strings.Join(missing, " and "))
	}
	return cfg, nil
}

// buildQuerySearcher returns the catalog search, or nil when it is not
// enabled. Any configuration problem is an error, so the Broker refuses to
// start rather than serve queries it cannot answer.
func buildQuerySearcher(ctx context.Context, logger *slog.Logger) (resolve.QuerySearcher, error) {
	cfg, err := queryConfigFromEnv()
	if err != nil {
		return nil, err
	}
	if !cfg.enabled {
		logger.Info("broker.query_discovery.disabled")
		return nil, nil
	}
	index, err := tagsearch.LoadFile(cfg.catalogFile, cfg.uriScheme)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", envQueryCatalogFile, err)
	}
	extractor, err := bedrock.NewFromDefaultConfig(ctx, cfg.model)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%s=on", envQueryDiscovery), err)
	}
	logger.Info("broker.query_discovery.enabled",
		"catalog_file", cfg.catalogFile, "tracks", index.Len(), "model", cfg.model)
	return tagsearch.NewSearcher(index, extractor), nil
}
