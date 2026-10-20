package main

import (
	"encoding/json"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const discoverPath = "/fora.v1.ExchangeService/DiscoverResources"

func sampleQuery() *forav1.ResourceQuery {
	return &forav1.ResourceQuery{
		Exchange:          "exchange.example:8081",
		SupportedProfiles: []string{"fora-comp-v1"},
		Requester:         &forav1.Requester{Domain: "agent.agents.example"},
	}
}

// The record renders a request message in the FORA wire form whichever codec
// carried it, so a reader parses one shape.
func TestDecodeRendersTheRequestMessageInSnakeCase(t *testing.T) {
	binary, err := proto.Marshal(sampleQuery())
	if err != nil {
		t.Fatal(err)
	}
	asJSON, err := protojson.Marshal(sampleQuery())
	if err != nil {
		t.Fatal(err)
	}
	for name, sent := range map[string]struct {
		contentType string
		body        []byte
	}{
		"proto": {"application/proto", binary},
		"json":  {"application/json; charset=utf-8", asJSON},
	} {
		messageType, message, problem := decode(discoverPath, sent.contentType, "", sent.body)
		if messageType != "fora.v1.ResourceQuery" || problem != "" {
			t.Fatalf("%s: decoded as %q, problem %q", name, messageType, problem)
		}
		var fields map[string]any
		if err := json.Unmarshal(message, &fields); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok := fields["supported_profiles"]; !ok {
			t.Fatalf("%s: no snake_case supported_profiles in %s", name, message)
		}
	}
}

// A body that is not a FORA request is recorded as bytes only, and one that
// should be but does not decode says why.
func TestDecodeLeavesOtherRequestsUndecoded(t *testing.T) {
	cases := []struct {
		name, path, contentType, encoding string
		body                              []byte
		wantType, wantProblem             bool
	}{
		{"not an rpc", "/.well-known/fora.json", "", "", nil, false, false},
		{"unknown method", "/fora.v1.ExchangeService/Nothing", "application/proto", "", nil, false, false},
		{"compressed", discoverPath, "application/proto", "gzip", []byte{1}, true, true},
		{"garbage", discoverPath, "application/proto", "", []byte{0xff, 0xff}, true, true},
		{"other media type", discoverPath, "text/plain", "", []byte("x"), true, true},
	}
	for _, c := range cases {
		messageType, message, problem := decode(c.path, c.contentType, c.encoding, c.body)
		if (messageType != "") != c.wantType || (problem != "") != c.wantProblem || message != nil {
			t.Errorf("%s: type %q, problem %q, message %s", c.name, messageType, problem, message)
		}
	}
}
