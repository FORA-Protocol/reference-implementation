package selection

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
)

func TestDedupEncodingWarningUsesRequestLogger(t *testing.T) {
	var logs bytes.Buffer
	ctx := reqctx.IntoContext(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)).With("request_id", "dedup-test"))
	offer := &forav1.Offer{OfferId: "broken", Terms: []*forav1.LicenseTerm{{Pricing: &forav1.Pricing{Rate: string([]byte{0xff})}}}}
	if got := dedupKey(ctx, offer); got != identityKey(offer) {
		t.Fatalf("fallback key = %q", got)
	}
	for _, want := range []string{`"request_id":"dedup-test"`, `"msg":"broker.selection.term_encoding_failed"`, `"offer_id":"broken"`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing %s in log: %s", want, logs.String())
		}
	}
}
