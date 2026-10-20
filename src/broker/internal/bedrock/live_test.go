package bedrock_test

import (
	"context"
	"os"
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/bedrock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

// liveModelEnv names the model for the on-demand check against real Bedrock.
// Unset, the test skips. It uses the developer's standard AWS configuration
// (for example AWS_PROFILE and AWS_REGION), and model access must be enabled
// for that account and region:
//
//	FORA_BEDROCK_LIVE_MODEL=amazon.nova-lite-v1:0 AWS_PROFILE=<profile> AWS_REGION=us-east-1 \
//	  go test ./src/broker/internal/bedrock/ -run TestLiveExtraction -v
const liveModelEnv = "FORA_BEDROCK_LIVE_MODEL"

// liveVocabulary is the demo feed vocabulary, the one the deployed Broker
// sends to the model.
func liveVocabulary(t *testing.T) []string {
	t.Helper()
	ix, err := tagsearch.LoadFile("../../../../deploy/fixtures/demo/music.jsonl", "https")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	return ix.Vocabulary()
}

type liveCase struct {
	query            string
	include, exclude []string
	minBPM, maxBPM   *int
	minDur, maxDur   *float64
}

func TestLiveExtraction(t *testing.T) {
	model := os.Getenv(liveModelEnv)
	if model == "" {
		t.Skip(liveModelEnv + " is not set")
	}
	x, err := bedrock.NewFromDefaultConfig(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	vocab := liveVocabulary(t)
	vlog := liveCase{include: []string{"mood:calm", "use:vlog"}, exclude: []string{"vocals:true"}}
	cases := []liveCase{
		withQuery(vlog, "calm background music, no vocals, for my personal YouTube travel vlog"),
		withQuery(vlog, "For my personal YouTube travel vlog, find calm music without vocals"),
		withQuery(vlog, "I need calm music for a travel vlog, with no singing"),
		{
			query:   "epic soundtrack for a documentary trailer",
			include: []string{"genre:soundtrack", "mood:epic", "use:documentary", "use:trailer"},
		},
		{
			query:   "fast electronic track for a podcast intro",
			include: []string{"genre:electronica", "tempo:fast", "use:podcast"},
		},
		{
			query: "calm music at 100–120 BPM, lasting 60–90 seconds", include: []string{"mood:calm"},
			minBPM: ptr(100), maxBPM: ptr(120), minDur: ptr(60.0), maxDur: ptr(90.0),
		},
		{query: "music with no vocals", exclude: []string{"vocals:true"}},
		{query: "music at 100–120 BPM", minBPM: ptr(100), maxBPM: ptr(120)},
		{query: "news article about interest rates"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			e, err := x.Extract(context.Background(), tc.query, vocab)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			t.Logf("extraction: %+v", e)
			checkLive(t, e, tc)
		})
	}
}

func withQuery(c liveCase, q string) liveCase { c.query = q; return c }

func ptr[T any](v T) *T { return &v }

func checkLive(t *testing.T, e tagsearch.Extraction, want liveCase) {
	t.Helper()
	if !sameSet(e.Include, want.include) || !sameSet(e.Exclude, want.exclude) {
		t.Errorf("tags = include %v exclude %v, want include %v exclude %v",
			e.Include, e.Exclude, want.include, want.exclude)
	}
	if !samePtr(e.MinBPM, want.minBPM) || !samePtr(e.MaxBPM, want.maxBPM) ||
		!samePtr(e.MinDuration, want.minDur) || !samePtr(e.MaxDuration, want.maxDur) {
		t.Errorf("bounds = %+v, want bpm %v–%v duration %v–%v", e, want.minBPM, want.maxBPM, want.minDur, want.maxDur)
	}
}

func sameSet(got, want []string) bool {
	g, w := slices.Clone(got), slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	return slices.Equal(slices.Compact(g), w)
}

func samePtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
