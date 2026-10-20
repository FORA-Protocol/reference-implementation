package bedrock_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/bedrock"
)

// fakeConverser replaces Bedrock at the adapter boundary.
type fakeConverser struct {
	out      *bedrockruntime.ConverseOutput
	err      error
	in       *bedrockruntime.ConverseInput
	deadline time.Time
}

func (f *fakeConverser) Converse(ctx context.Context, in *bedrockruntime.ConverseInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseOutput, error) {
	f.in = in
	f.deadline, _ = ctx.Deadline()
	return f.out, f.err
}

func textOutput(s string) *bedrockruntime.ConverseOutput {
	return &bedrockruntime.ConverseOutput{Output: &types.ConverseOutputMemberMessage{Value: types.Message{
		Role:    types.ConversationRoleAssistant,
		Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: s}},
	}}}
}

func TestExtractSendsQueryAndParsesOutput(t *testing.T) {
	fake := &fakeConverser{out: textOutput(`{"include":["mood:calm"],"exclude":["vocals:true"],"max_bpm":110}`)}
	start := time.Now()
	e, err := bedrock.New(fake, "amazon.nova-lite-v1:0").
		Extract(context.Background(), "calm music, no vocals", []string{"mood:calm", "vocals:true"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !slices.Equal(e.Include, []string{"mood:calm"}) || !slices.Equal(e.Exclude, []string{"vocals:true"}) ||
		e.MaxBPM == nil || *e.MaxBPM != 110 {
		t.Errorf("extraction = %+v", e)
	}
	if *fake.in.ModelId != "amazon.nova-lite-v1:0" || *fake.in.InferenceConfig.Temperature != 0 {
		t.Errorf("model or temperature not sent: %+v", fake.in)
	}
	sys := fake.in.System[0].(*types.SystemContentBlockMemberText).Value
	if !strings.Contains(sys, "mood:calm\nvocals:true") {
		t.Errorf("system prompt lacks the vocabulary: %s", sys)
	}
	if q := fake.in.Messages[0].Content[0].(*types.ContentBlockMemberText).Value; q != "calm music, no vocals" {
		t.Errorf("query = %q", q)
	}
	if fake.deadline.IsZero() || fake.deadline.After(start.Add(bedrock.Timeout+time.Second)) {
		t.Errorf("deadline = %v, want at most %v from start", fake.deadline, bedrock.Timeout)
	}
}

func TestExtractKeepsShorterCallerDeadline(t *testing.T) {
	fake := &fakeConverser{out: textOutput(`{"include":[],"exclude":[]}`)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want, _ := ctx.Deadline()
	if _, err := bedrock.New(fake, "m").Extract(ctx, "q", nil); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !fake.deadline.Equal(want) {
		t.Errorf("deadline = %v, want caller deadline %v", fake.deadline, want)
	}
}

func TestExtractFailuresAreUnavailable(t *testing.T) {
	cases := map[string]*fakeConverser{
		"bedrock error":     {err: errors.New("ThrottlingException")},
		"timeout":           {err: context.DeadlineExceeded},
		"no message":        {out: &bedrockruntime.ConverseOutput{}},
		"no text":           {out: textOutput("")},
		"not json":          {out: textOutput("calm music")},
		"schema violation":  {out: textOutput(`{"include":"mood:calm","exclude":[]}`)},
		"inverted duration": {out: textOutput(`{"include":[],"exclude":[],"min_duration_seconds":9,"max_duration_seconds":1}`)},
	}
	for name, fake := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := bedrock.New(fake, "m").Extract(context.Background(), "q", nil); !errors.Is(err, bedrock.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
		})
	}
}
