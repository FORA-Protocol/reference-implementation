//go:build e2e

package e2econtrol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The lifetimes control holds its state in memory and touches no datastore, so
// its routes are driven here through a real ServeMux over httptest. The relay
// policy routes write the database and are driven end to end by the e2e suite
// against the running Exchange.

const testTenant = "tenant-seat-test"

var configured = Pair{Offer: 5 * time.Minute, URL: 5 * time.Minute}

func lifetimesServer(t *testing.T) (*Lifetimes, *httptest.Server) {
	t.Helper()
	lifetimes := NewLifetimes(configured)
	mux := http.NewServeMux()
	for _, route := range Routes(Deps{Lifetimes: lifetimes}) {
		mux.Handle(route.Pattern, route.Handler)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return lifetimes, server
}

func call(t *testing.T, server *httptest.Server, method, tenant, body string) (int, lifetimesBody) {
	t.Helper()
	url := server.URL + strings.Replace(LifetimesPath, "{tenant}", tenant, 1)
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got lifetimesBody
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return resp.StatusCode, got
}

func TestPutShortensAndDeleteRestoresTheConfiguredLifetimes(t *testing.T) {
	lifetimes, server := lifetimesServer(t)

	status, got := call(t, server, http.MethodPut, testTenant, `{"offer_ttl_seconds":3,"url_ttl_seconds":4}`)
	if status != http.StatusOK || got.OfferTTLSeconds != 3 || got.URLTTLSeconds != 4 {
		t.Fatalf("PUT answered %d %+v, want 200 with 3s and 4s", status, got)
	}
	if ttl, ok := lifetimes.OfferTTL(testTenant); !ok || ttl != 3*time.Second {
		t.Fatalf("OfferTTL = %v %v, want 3s", ttl, ok)
	}
	if _, ok := lifetimes.URLTTL("another-tenant"); ok {
		t.Fatal("another tenant reads an override it was never given")
	}

	status, got = call(t, server, http.MethodDelete, testTenant, "")
	if status != http.StatusOK || got.OfferTTLSeconds != 300 || got.URLTTLSeconds != 300 {
		t.Fatalf("DELETE answered %d %+v, want 200 with the configured 300s", status, got)
	}
	if _, ok := lifetimes.OfferTTL(testTenant); ok {
		t.Fatal("the override outlived DELETE")
	}
}

func TestPutOfOneLifetimeLeavesTheOtherConfigured(t *testing.T) {
	_, server := lifetimesServer(t)
	status, got := call(t, server, http.MethodPut, testTenant, `{"url_ttl_seconds":2}`)
	if status != http.StatusOK || got.OfferTTLSeconds != 300 || got.URLTTLSeconds != 2 {
		t.Fatalf("PUT answered %d %+v, want 200 with the configured offer lifetime and 2s", status, got)
	}
}

func TestPutThatWouldWeakenOrIsMalformedIsRefusedAndChangesNothing(t *testing.T) {
	cases := map[string]struct{ tenant, body string }{
		"longer offer":   {testTenant, `{"offer_ttl_seconds":3600}`},
		"equal url":      {testTenant, `{"url_ttl_seconds":300}`},
		"negative offer": {testTenant, `{"offer_ttl_seconds":-1}`},
		"unknown field":  {testTenant, `{"offer_ttl_seconds":3,"verify":false}`},
		"not json":       {testTenant, `offer=3`},
		"not a tenant":   {strings.Repeat("t", maxTenantIDBytes+1), `{"offer_ttl_seconds":3}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lifetimes, server := lifetimesServer(t)
			if status, _ := call(t, server, http.MethodPut, tc.tenant, tc.body); status != http.StatusBadRequest {
				t.Fatalf("PUT %s answered %d, want 400", tc.body, status)
			}
			if got := lifetimes.Effective(testTenant); got != configured {
				t.Fatalf("a refused PUT changed the lifetimes to %+v", got)
			}
		})
	}
}
