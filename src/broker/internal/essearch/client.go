package essearch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
)

// maxResponseBytes bounds what one answer can make the Broker read. A search
// returns at most 256 hits projected to their URLs, so a larger body is not a
// search answer.
const maxResponseBytes = 1 << 20

// errAnswerTooLarge is an answer past maxResponseBytes, refused by name rather
// than truncated into a decode error.
var errAnswerTooLarge = errors.New("answer over 1 MiB")

// newHTTPClient builds the one client every call uses. It verifies certificates
// against CACertPEM alone when one is set, or the system roots otherwise, and
// it follows no redirect: a 3xx is an answer to report, not a hop to take with
// the credentials attached. Nothing is retried.
func newHTTPClient(cfg Config) *http.Client {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CACertPEM != nil {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(cfg.CACertPEM) // Validate has refused a bundle with no certificate
		tlsConfig.RootCAs = pool
	}
	transport := &http.Transport{}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}
	transport.TLSClientConfig = tlsConfig
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// post sends one JSON body under the configured timeout and returns the 2xx
// answer, read up to maxResponseBytes. A non-2xx answer, a 3xx included, is a
// *statusError.
func (s *Searcher) post(ctx context.Context, url string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	s.authenticate(req)
	if id := reqctx.RequestID(ctx); id != "" {
		req.Header.Set("X-Opaque-Id", id)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", s.cfg.Index, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read answer: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return nil, errAnswerTooLarge
	}
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return nil, newStatusError(res.StatusCode, raw)
	}
	return raw, nil
}

// authenticate sets the credentials of the configured mode, and none for
// AuthNone.
func (s *Searcher) authenticate(req *http.Request) {
	switch s.cfg.Auth {
	case AuthAPIKey:
		req.Header.Set("Authorization", "ApiKey "+s.cfg.APIKey)
	case AuthBasic:
		req.SetBasicAuth(s.cfg.Username, s.cfg.Password)
	case AuthNone:
	}
}
