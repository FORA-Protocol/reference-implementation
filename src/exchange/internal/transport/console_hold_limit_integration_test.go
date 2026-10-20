//go:build integration

package transport_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// How many sites one payee may hold without proving them. Saving holds a
// hostname for the payee before anything proves it is theirs, so the domain
// route caps the sites a payee holds saved and not verified on one seat at
// consoleapi.MaxUnverifiedDomains. Verified sites do not count.
//
// Legs: every save goes signed HTTP -> service -> repositories -> database, and
// what the payee holds is read back through the signed resume route. Audit rows
// are read one tier lower, through the audit repository, for the reason
// auditActions gives.

// heldDomain is the i-th site a test saves.
func heldDomain(i int) string { return fmt.Sprintf("site%d.stoa.example", i) }

// TestConsoleRegistration_CapsTheSitesHeldWithoutProof: a payee holding the
// most unverified sites it may is refused one more, as a failed precondition
// that writes nothing. Saving one it already holds again is not a new hold, and
// verifying one makes room for another. The cap is each payee's own: another
// payee on the same seat still saves a site while this one is at the cap.
func TestConsoleRegistration_CapsTheSitesHeldWithoutProof(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	for i := range consoleapi.MaxUnverifiedDomains {
		registerPending(t, f, actor, publisherID, heldDomain(i))
	}

	over := heldDomain(consoleapi.MaxUnverifiedDomains)
	status, body := saveSite(t, f, actor, publisherID, over)
	if status != http.StatusBadRequest || body["kind"] != consoleapi.ErrorKindFailedPrecondition {
		t.Fatalf("saving one site past the cap = %d %v; want 400 %s",
			status, body, consoleapi.ErrorKindFailedPrecondition)
	}
	if _, sites := listRegistrations(t, f, actor); len(sites) != consoleapi.MaxUnverifiedDomains {
		t.Fatalf("after the refused save the payee holds %d sites, want %d", len(sites), consoleapi.MaxUnverifiedDomains)
	}
	if n := countActions(t, f, "RegisterDomain"); n != consoleapi.MaxUnverifiedDomains {
		t.Fatalf("%d RegisterDomain audit rows, want %d: the refused save wrote one", n, consoleapi.MaxUnverifiedDomains)
	}

	// The same site saved again is the one the payee already holds.
	registerPending(t, f, actor, publisherID, heldDomain(0))

	f.manifest.serve(heldDomain(0), publisherManifest(heldDomain(0), publisherID, consoleContributorHost))
	if checked := checkDomain(t, f, actor, publisherID, heldDomain(0)); checked["passed"] != true {
		t.Fatalf("CheckDomain did not verify %s: %v", heldDomain(0), checked)
	}
	registerPending(t, f, actor, publisherID, over)

	other := newActor("desk@harmonia.example")
	registerPending(t, f, other, f.registerPublisher(t, other, "Harmonia Records"), "harmonia.example")
}

// TestConsoleRegistration_ConcurrentSavesShareTheLastPlace: with one place left
// under the cap, several saves of different sites for the same payee arrive at
// once. Exactly one is written; every other is refused as a failed
// precondition. Counting before writing would let them all through.
func TestConsoleRegistration_ConcurrentSavesShareTheLastPlace(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	for i := range consoleapi.MaxUnverifiedDomains - 1 {
		registerPending(t, f, actor, publisherID, heldDomain(i))
	}

	// Each goroutine warms its own connection first, for the reason
	// TestConsoleRegistration_ConcurrentSubmitsMintOnePublisher gives: without
	// it the requests arrive one after another and never race.
	const racers = 6
	saved := make([]bool, racers)
	var ready sync.WaitGroup
	ready.Add(racers)
	start := make(chan struct{})
	var g errgroup.Group
	for i := range racers {
		g.Go(func() error {
			_, warmErr := f.postConcurrently(transport.ConsoleListRegistrationsPath, map[string]any{"actor": actor})
			ready.Done()
			if warmErr != nil {
				return fmt.Errorf("warm-up ListRegistrations: %w", warmErr)
			}
			<-start
			out, err := f.postConcurrently(transport.ConsoleRegisterDomainPath,
				siteBody(actor, publisherID, heldDomain(consoleapi.MaxUnverifiedDomains+i)))
			if err == nil {
				saved[i] = true
				return nil
			}
			if out["kind"] != consoleapi.ErrorKindFailedPrecondition {
				return fmt.Errorf("racer %d: %w", i, err)
			}
			return nil
		})
	}
	ready.Wait()
	close(start)
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent saves: %v", err)
	}
	var written int
	for _, ok := range saved {
		if ok {
			written++
		}
	}
	if written != 1 {
		t.Fatalf("%d of %d concurrent saves were written into the last place, want exactly 1", written, racers)
	}
	if _, sites := listRegistrations(t, f, actor); len(sites) != consoleapi.MaxUnverifiedDomains {
		t.Fatalf("the payee holds %d sites, want %d", len(sites), consoleapi.MaxUnverifiedDomains)
	}
}

// TestConsoleRegistration_ASaveWaitsForThePayeesLock pins the mechanism behind
// the test above. The race it closes is a window of a few milliseconds, which a
// burst of requests hits only some of the time, so that test alone would pass a
// missing lock most of the time. Here a transaction holds the payee's row lock
// while a save arrives: the save has to wait until the lock is released, and
// then go through.
//
// Tier-2 arrange through the production write port: no public route holds a
// payee's lock, and none should. The lock is taken by the domain route's own
// transactions, and this one stands in for a second of them.
func TestConsoleRegistration_ASaveWaitsForThePayeesLock(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")

	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatalf("begin the lock holder: %v", err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if err := repo.NewPublisherWriteRepo(sqlc.New(f.pool)).Lock(f.ctx, tx, publisherID); err != nil {
		t.Fatalf("hold the payee's lock: %v", err)
	}

	answered := make(chan error, 1)
	go func() {
		_, err := f.postConcurrently(transport.ConsoleRegisterDomainPath, siteBody(actor, publisherID, heldDomain(0)))
		answered <- err
	}()
	select {
	case err := <-answered:
		t.Fatalf("a save was answered while another transaction held the payee's lock (err %v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatalf("release the payee's lock: %v", err)
	}
	select {
	case err := <-answered:
		if err != nil {
			t.Fatalf("the save failed once the lock was released: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the save was not answered once the lock was released")
	}
}
