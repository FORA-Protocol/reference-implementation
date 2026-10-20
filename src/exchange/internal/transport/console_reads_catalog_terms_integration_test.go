//go:build integration

package transport_test

import (
	"slices"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	foraadminv1 "github.com/FORA-Protocol/protocol/gen/go/fora/admin/v1"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// The catalog and terms reads through the console routes. The catalog is
// written by the public catalog push, and the seat's rate and reporting policy
// by the operator's admin RPCs. A publisher's own rate, which no route sets, is
// written through its repository (setFeeOverride).

// catalogPage reads one page of the seller's resources.
func (p *publisherConsole) catalogPage(t *testing.T, s seller, pageSize int, cursor string) map[string]any {
	t.Helper()
	return p.read(t, transport.ConsoleListCatalogPath, consoleapi.ListCatalogRequest{
		Actor: s.actor, PublisherID: s.publisherID, PageSize: pageSize, Cursor: cursor,
	})
}

// pushAtRates pushes one resource per rate for the seller, all in one catalog
// push, and returns each resource's URI with its rate. A push is stored in one
// database transaction, so every resource it holds gets the same creation
// time, which is what a publisher's batch push produces.
func (p *publisherConsole) pushAtRates(t *testing.T, s seller, rates ...string) map[string]string {
	t.Helper()
	entries := make([]*forav1.ResourceEntry, 0, len(rates))
	pushed := make(map[string]string, len(rates))
	for i, rate := range rates {
		term := seedPricedTermEst(20)
		term.Pricing.Rate = rate
		entries = append(entries, &forav1.ResourceEntry{
			Domain: s.domain, Path: pathFor(i), Terms: []*forav1.LicenseTerm{term},
		})
		pushed["https://"+s.domain+pathFor(i)] = rate
	}
	_, err := p.h.catalogClient.PushResources(p.h.ctx, connect.NewRequest(
		newPushRequest(p.h.tenantID, "agent-test", entries)))
	if err != nil {
		t.Fatalf("push %d resources for %s: %v", len(entries), s.domain, err)
	}
	return pushed
}

// TestConsoleReads_CatalogPagesHoldOnlyThisPublishersResources pushes three
// resources for one publisher in one push, and one for another publisher on
// the same seat, and pages through the first publisher's two at a time. The
// three share a creation time, so the pages depend on the resource id that
// breaks the tie: every resource appears once, newest first and then by
// resource id descending, each with the price it was pushed at.
func TestConsoleReads_CatalogPagesHoldOnlyThisPublishersResources(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	s := p.newSeller(t, "stoa")
	pushed := p.pushAtRates(t, s, "0.05", "0.07", "0.09")
	other := p.newSeller(t, "harmonia")
	pushDiscoverTermOfferForTenant(t, p.h, p.h.tenantID, other.domain, pathFor(0), seedPricedTermEst(20))

	first := p.catalogPage(t, s, 2, "")
	cursor := field(t, first, "next_cursor")
	if cursor == "" {
		t.Fatal("the first of two pages has no next_cursor")
	}
	second := p.catalogPage(t, s, 2, cursor)
	if next := field(t, second, "next_cursor"); next != "" {
		t.Fatalf("the last page has next_cursor %q", next)
	}
	resources := append(rowsOf(t, first, "resources"), rowsOf(t, second, "resources")...)
	if len(resources) != len(pushed) {
		t.Fatalf("pages hold %d resources, want %d: %v", len(resources), len(pushed), resources)
	}
	assertNewestFirst(t, resources)
	listed := make(map[string]bool, len(resources))
	for _, r := range resources {
		uri := field(t, r, "uri")
		if listed[uri] {
			t.Errorf("resource %s is listed twice", uri)
		}
		listed[uri] = true
		if _, ok := r["pricing"]; ok {
			t.Errorf("resource %s carries a pricing member; the price is on its terms", uri)
		}
		want, ok := pushed[uri]
		if !ok {
			t.Errorf("resource %s is not one this publisher pushed", uri)
			continue
		}
		if got := termRate(t, r); got != want {
			t.Errorf("resource %s is priced at %q, want the %q it was pushed at", uri, got, want)
		}
	}
}

// assertNewestFirst asserts rows are ordered by created_at descending, then
// by resource_id descending.
func assertNewestFirst(t *testing.T, rows []map[string]any) {
	t.Helper()
	for i := 1; i < len(rows); i++ {
		prevAt, err1 := time.Parse(time.RFC3339Nano, field(t, rows[i-1], "created_at"))
		at, err2 := time.Parse(time.RFC3339Nano, field(t, rows[i], "created_at"))
		if err1 != nil || err2 != nil {
			t.Fatalf("created_at does not parse: %v, %v", rows[i-1], rows[i])
		}
		prevID, id := field(t, rows[i-1], "resource_id"), field(t, rows[i], "resource_id")
		if at.After(prevAt) || (at.Equal(prevAt) && id >= prevID) {
			t.Errorf("row %d (%s, %s) is not after row %d (%s, %s) in newest-first order",
				i, at, id, i-1, prevAt, prevID)
		}
	}
}

// termRate returns the rate of a resource's one term.
func termRate(t *testing.T, resource map[string]any) string {
	t.Helper()
	terms, ok := resource["terms"].([]any)
	if !ok || len(terms) != 1 {
		t.Fatalf("resource %v carries terms %v, want the one pushed", resource["uri"], resource["terms"])
	}
	term, _ := terms[0].(map[string]any)
	pricing, _ := term["pricing"].(map[string]any)
	rate, _ := pricing["rate"].(string)
	return rate
}

// publisherTerms reads the seller's terms.
func (p *publisherConsole) publisherTerms(t *testing.T, s seller) consoleapi.PublisherTerms {
	t.Helper()
	body := p.read(t, transport.ConsolePublisherTermsPath, consoleapi.PublisherTermsRequest{
		Actor: s.actor, PublisherID: s.publisherID,
	})
	policy, _ := body["reporting_policy"].(map[string]any)
	out := consoleapi.PublisherTerms{FeeRateSource: field(t, body, "fee_rate_source")}
	if bps, ok := body["fee_rate_bps"].(float64); ok {
		out.FeeRateBps = int(bps)
	}
	if tol, ok := policy["quantity_tolerance"].(float64); ok {
		out.ReportingPolicy.QuantityTolerance = tol
	}
	if w, ok := policy["window_seconds"].(float64); ok {
		out.ReportingPolicy.WindowSeconds = int32(w)
	}
	fields, ok := policy["required_fields"].([]any)
	if !ok {
		t.Fatalf("required_fields is not a list: %v", policy)
	}
	out.ReportingPolicy.RequiredFields = []string{}
	for _, f := range fields {
		s, _ := f.(string)
		out.ReportingPolicy.RequiredFields = append(out.ReportingPolicy.RequiredFields, s)
	}
	return out
}

// TestConsoleReads_TermsAreTheSeatDefaultsFilledIn sets the seat's rate
// through the admin RPC and leaves its reporting policy empty. The publisher is
// shown that rate and the policy the Exchange actually holds agents to: no
// required fields, a 20% quantity tolerance, and a 24-hour window.
func TestConsoleReads_TermsAreTheSeatDefaultsFilledIn(t *testing.T) {
	p := startPublisherConsole(t, 0)
	admin, _ := p.h.adminSurface(t)
	mustSetFee(t, p.h, admin, 750, nil)
	s := p.newSeller(t, "stoa")

	got := p.publisherTerms(t, s)
	want := consoleapi.PublisherTerms{
		FeeRateBps: 750, FeeRateSource: consoleapi.FeeRateSourceTenant,
		ReportingPolicy: consoleapi.ReportingPolicy{
			RequiredFields: []string{}, QuantityTolerance: 0.2, WindowSeconds: 86400,
		},
	}
	assertTerms(t, got, want)
}

// TestConsoleReads_TermsCarryTheSeatPolicyAndThePublisherOverride sets a
// reporting policy on the seat and a rate for the publisher. The publisher's
// rate beats the seat's, and the policy is shown as set.
func TestConsoleReads_TermsCarryTheSeatPolicyAndThePublisherOverride(t *testing.T) {
	p := startPublisherConsole(t, e2eFeeBps)
	admin, _ := p.h.adminSurface(t)
	tolerance, window := 0.1, int32(3600)
	mustSetPolicy(t, p.h, admin, &foraadminv1.ReportingPolicy{
		RequiredFields: []string{"billing_id"}, QuantityTolerance: &tolerance, WindowSeconds: &window,
	})
	s := p.newSeller(t, "stoa")
	// The deprecated quantity_tolerance set above is not read, so the seat keeps
	// the default the console shows for a policy that states none.
	policy := consoleapi.ReportingPolicy{RequiredFields: []string{"billing_id"}, QuantityTolerance: 0.2, WindowSeconds: 3600}

	assertTerms(t, p.publisherTerms(t, s), consoleapi.PublisherTerms{
		FeeRateBps: e2eFeeBps, FeeRateSource: consoleapi.FeeRateSourceTenant, ReportingPolicy: policy,
	})
	setFeeOverride(t, p, s, 2500)
	assertTerms(t, p.publisherTerms(t, s), consoleapi.PublisherTerms{
		FeeRateBps: 2500, FeeRateSource: consoleapi.FeeRateSourcePublisher, ReportingPolicy: policy,
	})
}

func assertTerms(t *testing.T, got, want consoleapi.PublisherTerms) {
	t.Helper()
	if got.FeeRateBps != want.FeeRateBps || got.FeeRateSource != want.FeeRateSource {
		t.Errorf("fee = %d from %q, want %d from %q",
			got.FeeRateBps, got.FeeRateSource, want.FeeRateBps, want.FeeRateSource)
	}
	g, w := got.ReportingPolicy, want.ReportingPolicy
	if !slices.Equal(g.RequiredFields, w.RequiredFields) || g.QuantityTolerance != w.QuantityTolerance ||
		g.WindowSeconds != w.WindowSeconds {
		t.Errorf("reporting policy = %+v, want %+v", g, w)
	}
}
