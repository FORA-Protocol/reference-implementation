//go:build integration

package transport_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"golang.org/x/sync/errgroup"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// TestConsoleRegistration_ConcurrentSubmitsMintOnePublisher is the double
// submit that the sequential retry test cannot reach: two registrations for the
// same person in flight together.
//
// Both requests read "no grant yet" before either one writes, so the read-first
// check in the service lets both through. What stops the second payee is the
// unique index on (seat, issuer, subject): the second grant insert fails, its
// transaction rolls back with the publisher row it wrote, and it answers with
// the publisher the first one committed.
//
// Legs: both writes go signed HTTP -> service -> repositories -> database, and
// the publisher read-back comes through the signed resume route the console
// uses. The audit count is one tier lower, through the audit repository, for
// the reason auditActions gives.
//
// Not covered: that the losing transaction's publisher row is really gone. A
// publisher is reachable only through its grant, and the loser's minted id is
// never returned, so no route and no read repository can see an orphaned row.
// What keeps that row inside the transaction is the write contract itself:
// PublisherWriteRepo.Create takes the pgx.Tx the grant is written in, so it
// cannot commit on its own.
func TestConsoleRegistration_ConcurrentSubmitsMintOnePublisher(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")

	// Two submits, because the client keeps two idle connections per host. Each
	// goroutine first sends one signed call of its own, which opens its
	// connection and gets the console's key resolved and cached. Without that
	// warm-up one submit reuses a warm connection while the other waits for a
	// TCP handshake, and the first commits before the second reads: the
	// requests arrive one after another and the race never happens.
	const submits = 2
	ids := make([]string, submits)
	var ready sync.WaitGroup
	ready.Add(submits)
	start := make(chan struct{})
	var g errgroup.Group
	for i := range submits {
		g.Go(func() error {
			_, warmErr := f.postConcurrently(transport.ConsoleListRegistrationsPath, map[string]any{"actor": actor})
			ready.Done()
			if warmErr != nil {
				return fmt.Errorf("warm-up ListRegistrations: %w", warmErr)
			}
			<-start
			out, err := f.postConcurrently(transport.ConsoleRegisterPublisherPath,
				map[string]any{"actor": actor, "display_name": "Stoa Press"})
			ids[i], _ = out["publisher_id"].(string)
			return err
		})
	}
	ready.Wait()
	close(start)
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent submit: %v", err)
	}
	if ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("two concurrent registrations answered with publishers %q and %q, want one id", ids[0], ids[1])
	}

	listedID, _ := listRegistrations(t, f, actor)
	if listedID != ids[0] {
		t.Fatalf("ListRegistrations publisher_id = %q, want the id both submits returned, %q", listedID, ids[0])
	}
	// The losing submit rolled its transaction back, so only one registration
	// was recorded. This counts audit rows, not publisher rows: see the note on
	// the orphaned row above.
	if registered := countActions(t, f, "RegisterPublisher"); registered != 1 {
		t.Fatalf("two concurrent submits wrote %d RegisterPublisher audit rows, want 1", registered)
	}
}

// postConcurrently sends one signed registration request from a goroutine other
// than the test's own and returns the decoded body. It returns an error instead
// of calling t.Fatalf, which may only run on the test goroutine, and treats any
// status but 200 as an error.
func (f *consoleFixture) postConcurrently(pattern string, body map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	url := f.server.URL + consolePath(pattern)
	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode %s response (status %d): %w", url, resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("%s status %d, body %v", url, resp.StatusCode, out)
	}
	return out, nil
}

// listRegistrations reads back what a person holds through the console's own
// resume route: the publisher id and every site, each as the route renders it.
func listRegistrations(t *testing.T, f *consoleFixture, actor consoleActorBody) (string, []map[string]any) {
	t.Helper()
	status, list := f.post(t, consolePath(transport.ConsoleListRegistrationsPath), map[string]any{"actor": actor})
	if status != http.StatusOK {
		t.Fatalf("ListRegistrations status = %d, body = %v", status, list)
	}
	publisherID, _ := list["publisher_id"].(string)
	raw, _ := list["sites"].([]any)
	sites := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		site, _ := r.(map[string]any)
		sites = append(sites, site)
	}
	return publisherID, sites
}

// listedSite returns the one site a person holds, failing unless there is
// exactly one and it is the named domain.
func listedSite(t *testing.T, f *consoleFixture, actor consoleActorBody, domain string) map[string]any {
	t.Helper()
	_, sites := listRegistrations(t, f, actor)
	if len(sites) != 1 {
		t.Fatalf("ListRegistrations returned %d sites, want 1: %v", len(sites), sites)
	}
	if sites[0]["domain"] != domain {
		t.Fatalf("listed site domain = %v, want %s", sites[0]["domain"], domain)
	}
	return sites[0]
}

// auditDetailsOf reads the detail of every audit row on the seat recording
// action. Same tier as auditActions, for the same reason: the Exchange
// publishes no read surface for its audit log.
func auditDetailsOf(t *testing.T, f *consoleFixture, action string) []map[string]any {
	t.Helper()
	records, err := f.audit.ByTenant(f.ctx, f.seatID)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	var out []map[string]any
	for _, r := range records {
		if r.Action != action {
			continue
		}
		var detail map[string]any
		if err := json.Unmarshal(r.Detail, &detail); err != nil {
			t.Fatalf("decode %s audit detail: %v", action, err)
		}
		out = append(out, detail)
	}
	return out
}
