//go:build e2e

package e2econtrol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// flagResource is the request handling every boolean control shares: decoding
// the body, mapping a missing subject to 404 and a failed store to 500, and
// answering with the value read back. That logic touches no datastore, so it is
// driven here through a real ServeMux, over a store held in a map. The controls
// built on it write the database and are driven end to end by the e2e suite.

const flagPath = "/e2e/v1/things/{thing}/flag"

// flagStore is the map the resource writes and reads; failing makes every call
// fail as a store outage would.
type flagStore struct {
	values  map[string]bool
	writes  int
	failing bool
}

var errStoreDown = errors.New("store down")

func (s *flagStore) resource() flagResource {
	return flagResource{
		pathKey: "thing",
		subject: "thing_id",
		member:  "on",
		event:   "test.flag",
		write: func(_ context.Context, key string, value bool) error {
			if s.failing {
				return errStoreDown
			}
			if _, ok := s.values[key]; !ok {
				return errNoSubject
			}
			s.writes++
			s.values[key] = value
			return nil
		},
		read: func(_ context.Context, key string) (bool, error) {
			if s.failing {
				return false, errStoreDown
			}
			value, ok := s.values[key]
			if !ok {
				return false, errNoSubject
			}
			return value, nil
		},
	}
}

func flagServer(t *testing.T, f flagResource) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range f.routes(flagPath) {
		mux.Handle(route.Pattern, route.Handler)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func callFlag(t *testing.T, server *httptest.Server, method, key, body string) (int, map[string]any) {
	t.Helper()
	url := server.URL + strings.Replace(flagPath, "{thing}", key, 1)
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return resp.StatusCode, got
}

func TestAFlagIsWrittenAndAnsweredAsReadBack(t *testing.T) {
	store := &flagStore{values: map[string]bool{"a": false}}
	server := flagServer(t, store.resource())

	status, got := callFlag(t, server, http.MethodPut, "a", `{"on":true}`)
	if status != http.StatusOK || got["on"] != true || got["thing_id"] != "a" {
		t.Fatalf("PUT = %d %v, want 200 with the stored value", status, got)
	}
	if !store.values["a"] {
		t.Fatal("the PUT did not reach the store")
	}
	status, got = callFlag(t, server, http.MethodGet, "a", "")
	if status != http.StatusOK || got["on"] != true {
		t.Fatalf("GET = %d %v, want 200 with the stored value", status, got)
	}
}

func TestAFlagForNoSubjectIsNotFound(t *testing.T) {
	store := &flagStore{values: map[string]bool{"a": false}}
	server := flagServer(t, store.resource())

	if status, _ := callFlag(t, server, http.MethodPut, "missing", `{"on":true}`); status != http.StatusNotFound {
		t.Errorf("PUT for no subject = %d, want 404", status)
	}
	if status, _ := callFlag(t, server, http.MethodGet, "missing", ""); status != http.StatusNotFound {
		t.Errorf("GET for no subject = %d, want 404", status)
	}
	if store.writes != 0 {
		t.Errorf("a refused PUT wrote %d time(s)", store.writes)
	}
}

func TestAFlagBodyThatIsNotExactlyTheBooleanIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"empty":          `{}`,
		"null":           `{"on":null}`,
		"string":         `{"on":"yes"}`,
		"unknown member": `{"off":true}`,
		"extra member":   `{"on":true,"thing_id":"b"}`,
		"not an object":  `[true]`,
		"no body":        ``,
	} {
		t.Run(name, func(t *testing.T) {
			store := &flagStore{values: map[string]bool{"a": false}}
			server := flagServer(t, store.resource())
			if status, _ := callFlag(t, server, http.MethodPut, "a", body); status != http.StatusBadRequest {
				t.Fatalf("PUT %q = %d, want 400", body, status)
			}
			if store.writes != 0 || store.values["a"] {
				t.Fatalf("a refused PUT changed the store: %+v", store)
			}
		})
	}
}

// The lengths are literals, not the constant, so the bound itself is pinned: the
// longest tenant id the admin plane accepts is served, and one byte more is
// refused before the store is touched.
func TestAPathIDIsServedUpToTheLongestTenantIDTheAdminPlaneAccepts(t *testing.T) {
	for length, want := range map[int]int{255: http.StatusOK, 256: http.StatusBadRequest} {
		t.Run(strconv.Itoa(length), func(t *testing.T) {
			key := strings.Repeat("a", length)
			store := &flagStore{values: map[string]bool{key: false}}
			server := flagServer(t, store.resource())
			if status, _ := callFlag(t, server, http.MethodPut, key, `{"on":true}`); status != want {
				t.Fatalf("PUT with a %d-byte id = %d, want %d", length, status, want)
			}
			if want != http.StatusOK && store.writes != 0 {
				t.Fatalf("a refused PUT wrote %d time(s)", store.writes)
			}
		})
	}
}

func TestAStoreFailureIsAnInternalError(t *testing.T) {
	store := &flagStore{values: map[string]bool{"a": false}, failing: true}
	server := flagServer(t, store.resource())

	if status, _ := callFlag(t, server, http.MethodPut, "a", `{"on":true}`); status != http.StatusInternalServerError {
		t.Errorf("PUT on a failing store = %d, want 500", status)
	}
	if status, _ := callFlag(t, server, http.MethodGet, "a", ""); status != http.StatusInternalServerError {
		t.Errorf("GET on a failing store = %d, want 500", status)
	}
}
