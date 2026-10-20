//go:build integration

package mcp_test

import (
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// Filters reach the Broker exactly as the agent wrote them, with no query and no
// URIs. The values cover each JSON type a profile uses — a string, a boolean
// false, an array — plus a key no profile defines. This service keeps no list of
// keys, so it must not drop the unknown one or the false value: the Broker is
// the one that decides what they mean.
func TestDiscover_ForwardsSearchFiltersUnchanged(t *testing.T) {
	f := newFixture(t)
	a := f.provision(t, "dev-one")
	filters := map[string]any{
		"music.isrc":        "USUAN2300011",
		"music.vocals":      false,
		"music.instruments": []any{"cello", "piano"},
		"x.not_in_profile":  "kept",
	}

	callTool[discoverResult](t, f.connect(t, a.Token), "fora_discover", map[string]any{
		"search_filters": filters,
	})

	sent := f.broker.LastDiscovery()
	if sent == nil {
		t.Fatal("a filters-only discovery never reached the Broker")
	}
	if got := sent.GetSearchFilters().AsMap(); !reflect.DeepEqual(got, filters) {
		t.Errorf("search_filters sent = %v, want %v", got, filters)
	}
	if sent.Query != nil || len(sent.GetUris()) != 0 {
		t.Errorf("request carries query=%v uris=%v, want neither", sent.Query, sent.GetUris())
	}
}

// An empty filter object names nothing to search for, so it counts as absent. A
// call with nothing else is refused before any Broker call.
func TestDiscover_EmptySearchFiltersAreRefused(t *testing.T) {
	f := newFixture(t)
	a := f.provision(t, "dev-one")

	msg := callToolErr(t, f.connect(t, a.Token), "fora_discover", map[string]any{
		"search_filters": map[string]any{},
	})

	if !strings.Contains(msg, "search_filters") {
		t.Errorf("tool error %q, want it to name search_filters as an accepted argument", msg)
	}
	if len(f.broker.Calls()) != 0 {
		t.Error("a discovery with an empty filter object still reached the Broker")
	}
}

// The Broker rejects a malformed filter with an ErrorDetail that names no reason
// and keeps the offending key only in metadata["field"]. This is the Broker's
// real shape: its validation errors are the generic transport class, and the
// message writes the key with a space, not the dotted path. So the field must be
// read from the metadata, or the agent cannot tell which filter to fix.
func TestDiscover_MalformedFilterErrorNamesTheField(t *testing.T) {
	f := newFixture(t)
	const field = "search_filters.music.bpm_range"
	const message = "invalid_argument: search_filters music.bpm_range: must be a string"
	refusal := connect.NewError(connect.CodeInvalidArgument, errStub(message))
	refusal.AddDetail(mustDetail(t, &forav1.ErrorDetail{
		Domain:   "broker",
		Message:  message,
		Metadata: map[string]string{"field": field},
	}))
	f.broker.failWith(refusal)
	a := f.provision(t, "dev-one")

	msg := callToolErr(t, f.connect(t, a.Token), "fora_discover", map[string]any{
		"search_filters": map[string]any{"music.bpm_range": 90},
	})

	if !strings.Contains(msg, field) {
		t.Errorf("tool error %q, want it to name %s", msg, field)
	}
	// The field is added to the rendering, not substituted for it: the reason
	// token an agent branches on and the Broker's own sentence both stay.
	if !strings.Contains(msg, "invalid_argument") {
		t.Errorf("tool error %q, want it to keep the reason token", msg)
	}
	if !strings.Contains(msg, "must be a string") {
		t.Errorf("tool error %q, want it to keep the Broker's message", msg)
	}
}
