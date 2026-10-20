//go:build integration

package transport_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/url"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	sdkconnect "github.com/FORA-Protocol/protocol/sdk/go/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/catalogid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/metadatacodec"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// The Exchange records, on every catalog row, whether the last push that wrote
// it was the automatic import's, read off the push's X-Fora-Catalog-Source
// header. Every push here is a real signed PushResources call from the ingest
// client, with the header set -- or left out -- by a client interceptor, and
// offers are read back through DiscoverResources. Two things have no public
// read and go through the automatic import's own repository port (storedPage),
// the Testing Doctrine's documented fallback: the flag itself, which no RPC or
// console route reads (catalog.auto_imported), and the stored metadata
// document, which TestCatalogSource_TheLabelIsNotPageData compares whole where
// DiscoverResources shows only a projection of it.

// sourceContributor is the contributor these tests push as. The harness
// publisher's manifest authorizes it and its key is served from its own
// directory, so both contributor gates pass.
const sourceContributor = "source-contributor.example"

// newSourceHarness is the push harness with sourceContributor authorized. It
// returns the contributor's key.
func newSourceHarness(t *testing.T) (*pushHarness, ed25519.PrivateKey) {
	t.Helper()
	h := newPushHarness(t)
	h.publisher.setContributors(sourceContributor)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate contributor key: %v", err)
	}
	h.publishAgent(t, sourceContributor, priv)
	return h, priv
}

// withRawHeader sets name to values on every request the client sends, spelled
// exactly as given. Header().Set would put the name in canonical form, and the
// lowercase case below is the one that proves the Exchange matches it in any
// case. No values leaves the request without the header.
func withRawHeader(name string, values ...string) sdkconnect.ClientOption {
	return sdkconnect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if len(values) > 0 {
				req.Header()[name] = values
			}
			return next(ctx, req)
		}
	}))
}

// pushAs pushes entries as sourceContributor through the ingest client, with
// opts added to the client, and returns the push's error.
func pushAs(
	t *testing.T, h *pushHarness, priv ed25519.PrivateKey, opts []sdkconnect.ClientOption, entries ...*forav1.ResourceEntry,
) error {
	t.Helper()
	client, err := ingest.NewCatalogClient(h.server.URL, exchangetest.SignatureAgentOrigin(sourceContributor), priv, nil, opts...)
	if err != nil {
		t.Fatalf("build catalog client: %v", err)
	}
	_, err = ingest.PushEntries(h.ctx, client, pushTarget(h.tenantID, sourceContributor), entries)
	return err
}

// labelled is the client option the automatic import's label travels in.
func labelled() []sdkconnect.ClientOption {
	return []sdkconnect.ClientOption{withRawHeader(catalogid.SourceHeader, catalogid.SourceAutoImport)}
}

// sourceEntry is an entry for path on the harness site, priced at rate.
func sourceEntry(h *pushHarness, path, rate string) *forav1.ResourceEntry {
	term := seedPricedTerm()
	term.Pricing.Rate = rate
	return &forav1.ResourceEntry{Domain: h.publisherDom, Path: path, Terms: []*forav1.LicenseTerm{term}}
}

// storedPage reads the row at uri through the automatic import's repository
// port. It is the documented fallback for what no public route shows:
// catalog.auto_imported, which no RPC or console route reads, and the stored
// metadata document, of which DiscoverResources shows only a projection
// (service/offer_metadata.go) and nothing at all when the document cannot be
// decoded (decodeRowMetadata in service/catalog_snapshot.go). The scenarios
// that must show a stored row untouched compare the whole row through it for
// that reason. A check that reads through it stops at the repository: it is a
// persistence round-trip, not a protocol one.
//
// SHARED helper: the importer scenarios read the rows they are about through it
// as well.
func storedPage(t *testing.T, h *pushHarness, uri string) repo.StoredPage {
	t.Helper()
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse %s: %v", uri, err)
	}
	pages, err := repo.NewSitePagesRepo(sqlc.New(h.pool)).PagesOfSite(h.ctx, h.tenantID, u.Host)
	if err != nil {
		t.Fatalf("PagesOfSite(%s): %v", u.Host, err)
	}
	for _, p := range pages {
		if p.URI == uri {
			return p
		}
	}
	t.Fatalf("no catalog row at %s", uri)
	return repo.StoredPage{}
}

// TestCatalogSource_OnlyTheExactLabelMarksTheRow drives each spelling of the
// header a push can carry and reads back what its row records. Only the header
// present once with exactly auto-import marks the row as the import's; its name
// is matched in any case, as an HTTP header's name is. Every other shape leaves
// the row to its owner.
func TestCatalogSource_OnlyTheExactLabelMarksTheRow(t *testing.T) {
	h, priv := newSourceHarness(t)
	cases := []struct {
		name   string
		header string
		values []string
		want   bool
	}{
		{"the label", catalogid.SourceHeader, []string{"auto-import"}, true},
		{"the label under a lowercase name", "x-fora-catalog-source", []string{"auto-import"}, true},
		{"no header", catalogid.SourceHeader, nil, false},
		{"the value in another case", catalogid.SourceHeader, []string{"Auto-Import"}, false},
		{"another value", catalogid.SourceHeader, []string{"manual"}, false},
		{"a list holding the value", catalogid.SourceHeader, []string{"auto-import, manual"}, false},
		{"the header twice", catalogid.SourceHeader, []string{"auto-import", "auto-import"}, false},
	}
	for i, tc := range cases {
		path := "/source/case-" + string(rune('a'+i))
		opts := []sdkconnect.ClientOption{withRawHeader(tc.header, tc.values...)}
		if err := pushAs(t, h, priv, opts, sourceEntry(h, path, "0.05")); err != nil {
			t.Fatalf("%s: push: %v", tc.name, err)
		}
		if got := storedPage(t, h, "https://"+h.publisherDom+path).AutoImported; got != tc.want {
			t.Errorf("%s: auto_imported = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestCatalogSource_TheLastPushDecides re-pushes one page with and without the
// label. The row records whichever push wrote it last, so a push that is not
// the import's hands a row the import wrote back to its owner, and the import's
// next push takes it again.
func TestCatalogSource_TheLastPushDecides(t *testing.T) {
	h, priv := newSourceHarness(t)
	const path = "/source/owned"
	for i, step := range []struct {
		opts []sdkconnect.ClientOption
		want bool
	}{
		{labelled(), true},
		{nil, false},
		{labelled(), true},
	} {
		if err := pushAs(t, h, priv, step.opts, sourceEntry(h, path, "0.05")); err != nil {
			t.Fatalf("push %d: %v", i+1, err)
		}
		if got := storedPage(t, h, "https://"+h.publisherDom+path).AutoImported; got != step.want {
			t.Fatalf("after push %d auto_imported = %v, want %v", i+1, got, step.want)
		}
	}
}

// TestCatalogSource_ARefusedPushChangesNoRow: a labelled push that the Exchange
// refuses -- one of its entries names a site this seat does not sell -- stores
// nothing, so the page it would have re-priced keeps both its price and its
// owner.
func TestCatalogSource_ARefusedPushChangesNoRow(t *testing.T) {
	h, priv := newSourceHarness(t)
	const path = "/source/kept"
	if err := pushAs(t, h, priv, nil, sourceEntry(h, path, "0.05")); err != nil {
		t.Fatalf("arrange the page: %v", err)
	}
	stranger := &forav1.ResourceEntry{
		Domain: "unregistered.example", Path: "/x", Terms: []*forav1.LicenseTerm{seedPricedTerm()},
	}
	err := pushAs(t, h, priv, labelled(), sourceEntry(h, path, "0.07"), stranger)
	var refused *sdkconnect.CallError
	if !errors.As(err, &refused) || refused.Kind != sdkconnect.CallRefused {
		t.Fatalf("a push naming an unregistered site = %v; want the Exchange's refusal", err)
	}
	if got := singleOffer(t, h, "https://"+h.publisherDom+path).GetPricing().GetRate(); got != "0.05" {
		t.Fatalf("the refused push re-priced the page to %s", got)
	}
	if storedPage(t, h, "https://"+h.publisherDom+path).AutoImported {
		t.Fatal("the refused labelled push handed the page to the import")
	}
}

// TestCatalogSource_TheLabelIsNotPageData: the label changes whose row it is
// and nothing else. The offer an agent is shown carries exactly the title and
// metadata that were pushed, and no trace of the label; the stored metadata is
// exactly the pushed entry's.
func TestCatalogSource_TheLabelIsNotPageData(t *testing.T) {
	h, priv := newSourceHarness(t)
	const path = "/source/described"
	ext, err := structpb.NewStruct(map[string]any{"desk": "science"})
	if err != nil {
		t.Fatal(err)
	}
	entry := sourceEntry(h, path, "0.05")
	entry.Title = proto.String("On rivers")
	entry.ContentHash = proto.String("sha256:abc")
	entry.HashMethod = proto.String("sha256")
	entry.Ext = ext
	if err := pushAs(t, h, priv, labelled(), entry); err != nil {
		t.Fatalf("push: %v", err)
	}

	offer := singleOffer(t, h, "https://"+h.publisherDom+path)
	if offer.GetTitle() != "On rivers" || offer.GetIdentity().GetContentHash() != "sha256:abc" {
		t.Errorf("offer title %q hash %q; want the pushed ones", offer.GetTitle(), offer.GetIdentity().GetContentHash())
	}
	if !proto.Equal(offer.GetExt(), ext) {
		t.Errorf("offer ext = %v; want exactly the pushed %v", offer.GetExt(), ext)
	}
	if shown := offer.String(); strings.Contains(shown, catalogid.SourceAutoImport) {
		t.Errorf("the offer an agent is shown carries the import's label: %s", shown)
	}
	stored, err := metadatacodec.Unmarshal(storedPage(t, h, "https://"+h.publisherDom+path).Metadata)
	if err != nil {
		t.Fatalf("decode the stored metadata: %v", err)
	}
	pushed, err := metadatacodec.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	want, err := metadatacodec.Unmarshal(pushed)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(stored, want) {
		t.Errorf("stored metadata = %v; want exactly the pushed entry's %v", stored, want)
	}
}
