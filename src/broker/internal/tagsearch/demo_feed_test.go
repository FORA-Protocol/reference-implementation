package tagsearch_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

// demoScript is the model answer the e2e fake model gives for each agreed
// request. Entries without text (an error status or a prose answer) and the
// slow entry test the model failure paths, which never reach the matcher.
const demoScript = "../../../../tests/e2e/harness/fixtures/bedrock_script.json"

// demoResults is the ordered result the demo catalog must give for every
// agreed request whose answer reaches the matcher.
var demoResults = map[string][]string{
	"calm background music, no vocals, for my personal YouTube travel vlog": {"almost-bliss", "evening"},
	"For my personal YouTube travel vlog, find calm music without vocals":   {"almost-bliss", "evening"},
	"I need calm music for a travel vlog, with no singing":                  {"almost-bliss", "evening"},
	"music with no vocals": {
		"adeste-fideles-shorter", "almost-bliss", "americana", "ancient-rite", "brain-dance",
		"evening", "hiding-your-reality", "lord-of-the-rangs", "mesmerizing-galaxy-loop",
		"strength-of-the-titans",
	},
	"music at 100-120 BPM": nil,
	"epic soundtrack for a documentary trailer": {
		"americana", "hiding-your-reality", "strength-of-the-titans",
	},
	"fast electronic track for a podcast intro":        {"brain-dance", "mesmerizing-galaxy-loop"},
	"calm music at 100-120 BPM, lasting 60-90 seconds": {"adeste-fideles-shorter", "evening"},
	"cheerful ukulele music":                           nil,
	"news article about interest rates":                nil,
}

// The demo music feed loads, and each agreed request, parsed the way the
// Broker parses the model's answer, returns exactly the tracks the catalog
// was chosen to give, in order. None of the ten tracks has vocals.
func TestDemoFeedAgreedRequests(t *testing.T) {
	ix, err := tagsearch.LoadFile("../../../../deploy/fixtures/demo/music.jsonl", "https")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	raw, err := os.ReadFile(demoScript)
	if err != nil {
		t.Fatal(err)
	}
	var script map[string]struct {
		Text  json.RawMessage `json:"text"`
		Sleep float64         `json:"sleep_seconds"`
	}
	if err := json.Unmarshal(raw, &script); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for query, answer := range script {
		if answer.Text == nil || answer.Sleep > 0 {
			continue
		}
		seen++
		t.Run(query, func(t *testing.T) {
			want, ok := demoResults[query]
			if !ok {
				t.Fatalf("no expected demo result for this agreed request")
			}
			e, err := tagsearch.ParseExtraction(string(answer.Text))
			if err != nil {
				t.Fatalf("ParseExtraction: %v", err)
			}
			if got := ix.Match(e); !slices.Equal(got, demoURIs(want)) {
				t.Errorf("Match = %v, want %v", got, demoURIs(want))
			}
		})
	}
	if seen != len(demoResults) {
		t.Errorf("script has %d matcher requests, expectations cover %d", seen, len(demoResults))
	}
}

func demoURIs(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "https://music.demo.fora-protocol.org/tracks/"+n+".mp3")
	}
	return out
}
