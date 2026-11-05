//go:build integration

package testutil

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/testcontainers/testcontainers-go"
	tcelasticsearch "github.com/testcontainers/testcontainers-go/modules/elasticsearch"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/tcstart"
)

// elasticsearchPassword is the bootstrap password of a container that lives for
// one test binary.
const elasticsearchPassword = "integration-throwaway"

// SharedElasticsearch is one single-node cluster brought up once per package,
// with security on as a production cluster has it. The module gives a node of
// this version plain http and no CA. It has no snapshot, so Reset removes what
// the previous test created: the Elasticsearch analogue of SharedRedis FLUSHDB.
//
// Its helpers are fixture lifecycle, standing in for the index owner, whose
// writes are outside the code under test. They speak to the node through one
// small superuser call with none of the Broker client's bounds, so the fixture
// cannot quietly share the code it is used to test.
type SharedElasticsearch struct {
	// Address is the http base URL; Username and Password are the superuser.
	Address  string
	Username string
	Password string

	http    *http.Client
	indices []string
	scripts []string
}

// StartSharedElasticsearch brings up the container and returns a handle plus a
// cleanup for TestMain to defer. ES_JAVA_OPTS takes precedence over the module's
// 2 GiB jvm.options file, so the heap is the 512 MiB set here. A 7.x node turns
// API keys off unless its HTTP interface has TLS, which this one has not, so the
// setting is stated: the API-key tests need it, as a deployment of that version
// does.
func StartSharedElasticsearch(ctx context.Context, logger *slog.Logger) (*SharedElasticsearch, func(), error) {
	c, err := tcstart.Run(ctx, func(ctx context.Context) (*tcelasticsearch.ElasticsearchContainer, error) {
		return tcelasticsearch.Run(ctx, ElasticsearchImage,
			tcelasticsearch.WithPassword(elasticsearchPassword),
			testcontainers.WithEnv(map[string]string{
				"ES_JAVA_OPTS":                         "-Xms512m -Xmx512m",
				"xpack.ml.enabled":                     "false",
				"ingest.geoip.downloader.enabled":      "false",
				"xpack.security.authc.api_key.enabled": "true",
			}),
		)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start elasticsearch: %w", err)
	}
	cleanup := func() {
		if termErr := c.Terminate(context.Background()); termErr != nil {
			logger.Warn("terminate elasticsearch", "err", termErr)
		}
	}
	return &SharedElasticsearch{
		Address:  c.Settings.Address,
		Username: c.Settings.Username,
		Password: c.Settings.Password,
		http:     &http.Client{},
	}, cleanup, nil
}

// call sends one request as the superuser and returns the status and the body.
func (s *SharedElasticsearch) call(
	ctx context.Context, method, path, contentType string, body []byte,
) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.Address+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(s.Username, s.Password)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	return res.StatusCode, raw, err
}

// must is call with a refused answer turned into an error naming what.
func (s *SharedElasticsearch) must(ctx context.Context, what, method, path string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: encode: %w", what, err)
	}
	if body == nil {
		payload = nil
	}
	status, raw, err := s.call(ctx, method, path, "application/json", payload)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s: status %d: %s", what, status, raw)
	}
	return raw, nil
}

// Reset deletes every stored template PutTemplate made and every index
// CreateIndex made, and invalidates every API key MintReadKey made. Call it at
// the start of each test's setup.
func (s *SharedElasticsearch) Reset(ctx context.Context) error {
	for _, id := range s.scripts {
		status, raw, err := s.call(ctx, http.MethodDelete, "/_scripts/"+id, "", nil)
		if err != nil {
			return fmt.Errorf("delete template %s: %w", id, err)
		}
		if status != http.StatusOK && status != http.StatusNotFound {
			return fmt.Errorf("delete template %s: status %d: %s", id, status, raw)
		}
	}
	s.scripts = nil
	if len(s.indices) > 0 {
		path := "/" + strings.Join(s.indices, ",") + "?ignore_unavailable=true"
		if _, err := s.must(ctx, "delete indices", http.MethodDelete, path, nil); err != nil {
			return err
		}
		s.indices = nil
	}
	_, err := s.must(ctx, "invalidate api keys", http.MethodDelete, "/_security/api_key", map[string]any{"owner": true})
	return err
}

// CreateIndex makes an index with the given field mappings, a JSON object of
// properties. One shard and no replicas make scores deterministic.
func (s *SharedElasticsearch) CreateIndex(ctx context.Context, name, properties string) error {
	body := map[string]any{
		"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0},
		"mappings": map[string]any{"properties": json.RawMessage(properties)},
	}
	if _, err := s.must(ctx, "create index "+name, http.MethodPut, "/"+name, body); err != nil {
		return err
	}
	s.indices = append(s.indices, name)
	return nil
}

// BulkIndex writes docs and makes them searchable before it returns.
func (s *SharedElasticsearch) BulkIndex(ctx context.Context, index string, docs []map[string]any) error {
	var body bytes.Buffer
	action := fmt.Sprintf("{\"index\":{\"_index\":%q}}\n", index)
	for _, doc := range docs {
		line, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("encode document: %w", err)
		}
		body.WriteString(action)
		body.Write(line)
		body.WriteByte('\n')
	}
	status, raw, err := s.call(ctx, http.MethodPost, "/_bulk?refresh=true", "application/x-ndjson", body.Bytes())
	if err != nil {
		return fmt.Errorf("bulk index: %w", err)
	}
	var outcome struct {
		Errors bool `json:"errors"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &outcome) != nil || outcome.Errors {
		return fmt.Errorf("bulk index: status %d: %s", status, raw)
	}
	return nil
}

// CreateAlias points alias at index.
func (s *SharedElasticsearch) CreateAlias(ctx context.Context, index, alias string) error {
	_, err := s.must(ctx, "create alias "+alias, http.MethodPut, "/"+index+"/_alias/"+alias, nil)
	return err
}

// PutTemplate stores a search template under id, as the index owner would.
func (s *SharedElasticsearch) PutTemplate(ctx context.Context, id, source string) error {
	body := map[string]any{"script": map[string]any{"lang": "mustache", "source": source}}
	if _, err := s.must(ctx, "store template "+id, http.MethodPut, "/_scripts/"+id, body); err != nil {
		return err
	}
	s.scripts = append(s.scripts, id)
	return nil
}

// MintReadKey creates an API key that may only read index, and returns what an
// index owner hands the Broker's operator: base64 of id:api_key. Every version
// answers with the id and the key; only later ones also send the encoded form.
func (s *SharedElasticsearch) MintReadKey(ctx context.Context, name, index string) (string, error) {
	body := map[string]any{
		"name": name,
		"role_descriptors": map[string]any{"reader": map[string]any{
			"indices": []any{map[string]any{"names": []string{index}, "privileges": []string{"read"}}},
		}},
	}
	raw, err := s.must(ctx, "mint api key", http.MethodPost, "/_security/api_key", body)
	if err != nil {
		return "", err
	}
	var minted struct {
		ID     string `json:"id"`
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(raw, &minted); err != nil {
		return "", fmt.Errorf("decode api key: %w", err)
	}
	return base64.StdEncoding.EncodeToString([]byte(minted.ID + ":" + minted.APIKey)), nil
}

// HeapMaxBytes reports the node's maximum heap.
func (s *SharedElasticsearch) HeapMaxBytes(ctx context.Context) (int64, error) {
	raw, err := s.must(ctx, "node info", http.MethodGet, "/_nodes/jvm", nil)
	if err != nil {
		return 0, err
	}
	var info struct {
		Nodes map[string]struct {
			JVM struct {
				Mem struct {
					HeapMax int64 `json:"heap_max_in_bytes"`
				} `json:"mem"`
			} `json:"jvm"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return 0, fmt.Errorf("decode node info: %w", err)
	}
	for _, node := range info.Nodes {
		return node.JVM.Mem.HeapMax, nil // a single-node cluster
	}
	return 0, fmt.Errorf("node info lists no node: %s", raw)
}
