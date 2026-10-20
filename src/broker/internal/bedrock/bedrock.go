// Package bedrock turns a natural-language query into a tag extraction with a
// model on Amazon Bedrock (Converse API). Credentials and region come from the
// standard AWS configuration chain, so the deployment decides where they are
// read from: the demo stack gives the Broker its own shared-credentials file
// and turns the instance metadata service off.
package bedrock

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

// Timeout bounds one extraction, SDK retries included. A shorter deadline on
// the caller's context wins.
const Timeout = 10 * time.Second

const maxTokens = 512

// ErrUnavailable wraps every failure: a Bedrock error, a timeout, or output
// that is not a valid extraction. The caller reports it as temporarily
// unavailable and does not fall back to another provider.
var ErrUnavailable = errors.New("tag extraction unavailable")

// Converser is the part of the Bedrock runtime client the extractor uses.
type Converser interface {
	Converse(ctx context.Context, in *bedrockruntime.ConverseInput,
		opts ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// Extractor calls one Bedrock model.
type Extractor struct {
	client Converser
	model  string
}

// New returns an extractor for model over client.
func New(client Converser, model string) *Extractor {
	return &Extractor{client: client, model: model}
}

// NewFromDefaultConfig builds the Bedrock runtime client from the standard AWS
// configuration chain (environment, shared profile, or the instance role).
func NewFromDefaultConfig(ctx context.Context, model string) (*Extractor, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("bedrock: load AWS configuration: %w", err)
	}
	if cfg.Region == "" {
		return nil, errors.New("bedrock: no AWS region is configured (set AWS_REGION)")
	}
	return New(bedrockruntime.NewFromConfig(cfg), model), nil
}

// Extract asks the model for the tags in query, restricted to vocabulary.
// The result is parsed and schema-checked but may still carry tags outside
// the vocabulary; tagsearch.Index.Known drops them.
func (x *Extractor) Extract(ctx context.Context, query string, vocabulary []string) (tagsearch.Extraction, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	out, err := x.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(x.model),
		System:  []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: SystemPrompt(vocabulary)}},
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: query}},
		}},
		InferenceConfig: &types.InferenceConfiguration{
			Temperature: aws.Float32(0),
			MaxTokens:   aws.Int32(maxTokens),
		},
	})
	if err != nil {
		return tagsearch.Extraction{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	text, err := outputText(out)
	if err != nil {
		return tagsearch.Extraction{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	e, err := tagsearch.ParseExtraction(text)
	if err != nil {
		return tagsearch.Extraction{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return e, nil
}

func outputText(out *bedrockruntime.ConverseOutput) (string, error) {
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return "", errors.New("response has no message")
	}
	var b strings.Builder
	for _, block := range msg.Value.Content {
		if t, ok := block.(*types.ContentBlockMemberText); ok {
			b.WriteString(t.Value)
		}
	}
	if b.Len() == 0 {
		return "", errors.New("response has no text")
	}
	return b.String(), nil
}

// SystemPrompt is the instruction sent with every query.
func SystemPrompt(vocabulary []string) string {
	return `You convert a request for music into search tags. Reply with exactly one JSON object and nothing else,
with these fields:
{"include": [...], "exclude": [...], "min_bpm": null, "max_bpm": null,
 "min_duration_seconds": null, "max_duration_seconds": null}

Rules:
1. If the request is not asking for music, reply with empty arrays and null bounds.
2. Use only tags from the vocabulary below, written exactly as listed ("facet:value").
3. Extract only musical attributes the request states. Map the user's words to the closest vocabulary
   tag, for example "electronic" to genre:electronica. Never add an attribute the request does not
   state: "fast" is tempo:fast only, never also energy:high.
4. The kind of content the music is for is a use:* tag: a vlog, podcast (including a podcast intro),
   documentary, trailer or corporate video. Always include it when the request names one.
5. "No vocals", "without vocals", "no singing" and "instrumental" are always exclude ["vocals:true"].
   Never write include ["vocals:false"].
6. Licensing and usage words are never tags: personal, commercial, online, YouTube, broadcast,
   television, advertising, and customer types such as individual, broadcaster or advertiser.
7. A numeric BPM request sets min_bpm and/or max_bpm (whole numbers) and adds no tempo:* tag.
   Still extract every other attribute in the request, such as its mood or genre.
8. A length request sets min_duration_seconds and/or max_duration_seconds, in seconds.
9. Use null, or leave the field out, for any bound the request does not give.
   Use empty arrays when nothing applies.

Examples (the tags assume they are in the vocabulary):
Request: "slow piano piece for a wedding video, no singing"
{"include": ["tempo:slow", "instrument:piano"], "exclude": ["vocals:true"]}
Request: "upbeat jazz between 110 and 130 BPM for my podcast, under two minutes"
{"include": ["mood:upbeat", "genre:jazz", "use:podcast"], "exclude": [], "min_bpm": 110, "max_bpm": 130,
 "max_duration_seconds": 120}
Request: "a recipe for pancakes"
{"include": [], "exclude": []}

Vocabulary:
` + strings.Join(vocabulary, "\n")
}
