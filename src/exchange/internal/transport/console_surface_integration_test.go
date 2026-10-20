//go:build integration

package transport_test

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// What the console surface answers besides a registration: the kind every
// refusal carries, the status each kind is answered with, and the denomination
// the Exchange prices in. The lifetime a signature may claim is capped at five
// minutes by the Web Bot Auth profile, and no SDK signer emits a longer window
// (helpers.MaxSignatureLifetime), so the surface's own clamp at the same five
// minutes is not reachable through any client this repository builds.

// TestConsoleSurface_UnsignedRequestNamesWhatASignatureMustCover pins that the
// console surface, which verifies outside the SDK's connectserver mount, answers
// a request with no signature the way that mount answers every FORA RPC: 401,
// the unauthenticated kind, and the Accept-Signature value naming the
// components and parameters a signature here must carry.
func TestConsoleSurface_UnsignedRequestNamesWhatASignatureMustCover(t *testing.T) {
	f := startConsoleFixture(t)
	f.client = &http.Client{}
	actor := newActor("editor@publisher.example")

	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost,
		f.server.URL+consolePath(transport.ConsoleRegisterPublisherPath),
		strings.NewReader(`{"display_name":"Stoa Press"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unsigned request = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	if got, want := resp.Header.Get(helpers.AcceptSignatureHeader), helpers.AcceptSignature(false); got != want {
		t.Errorf("%s = %q, want %q", helpers.AcceptSignatureHeader, got, want)
	}
	f.client = consoleSigningClient(t, f.server, f.directory, f.consoleKey)
	assertNoPublisherFor(t, f, actor)
}

// TestConsoleSurface_RefusalsCarryTheirKind pins that a caller fault names its
// kind in the body, which is what the console decides whose fault it is from.
func TestConsoleSurface_RefusalsCarryTheirKind(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	actor.EmailVerified = false

	status, body := f.post(t, consolePath(transport.ConsoleRegisterPublisherPath), map[string]any{
		"actor": actor, "display_name": "Stoa Press",
	})
	if status != http.StatusBadRequest || body["kind"] != consoleapi.ErrorKindInvalidRequest {
		t.Fatalf("an unverified actor = %d %v, want %d %s",
			status, body, http.StatusBadRequest, consoleapi.ErrorKindInvalidRequest)
	}
}

// TestConsoleSurface_ReportsItsDenomination covers the route the console
// prices its previews by. The answer is the deployment's configuration with
// the fallbacks filled in, so the console never has to know what an unset unit
// means.
func TestConsoleSurface_ReportsItsDenomination(t *testing.T) {
	f := startConsoleFixtureWith(t, func(o *exchangetest.Options) {
		o.TermsDefaults = termspreset.Defaults{Currency: "USD"}
	})

	status, body := f.post(t, consolePath(transport.ConsoleDenominationPath), map[string]any{})
	if status != http.StatusOK || body["currency"] != "USD" || body["unit"] != "fetches" {
		t.Fatalf("denomination = %d %v, want USD per fetches", status, body)
	}

	status, body = f.post(t, consolePath(transport.ConsoleDenominationPath), map[string]any{"currency": "EUR"})
	if status != http.StatusBadRequest || body["kind"] != consoleapi.ErrorKindInvalidRequest {
		t.Fatalf("a denomination request with a field the Exchange does not know = %d %v", status, body)
	}
}

// TestConsoleSurface_AnswersABrokenBodyAsMalformed separates the two ways a
// body read fails. A body past the cap is a size refusal. A connection that
// ends before the body it announced is a malformed request: answering it 413
// would tell the console to send less when nothing it sent was too large.
func TestConsoleSurface_AnswersABrokenBodyAsMalformed(t *testing.T) {
	f := startConsoleFixture(t)
	path := consolePath(transport.ConsoleListRegistrationsPath)

	status, kind := rawConsolePost(t, f, path, 100, `{"actor":`)
	if status != http.StatusBadRequest || kind != consoleapi.ErrorKindInvalidRequest {
		t.Fatalf("a body cut short = %d %s, want %d %s",
			status, kind, http.StatusBadRequest, consoleapi.ErrorKindInvalidRequest)
	}
	big := strings.Repeat("x", 64*1024)
	status, kind = rawConsolePost(t, f, path, len(big), big)
	if status != http.StatusRequestEntityTooLarge || kind != consoleapi.ErrorKindInvalidRequest {
		t.Fatalf("a body past the cap = %d %s, want %d", status, kind, http.StatusRequestEntityTooLarge)
	}
}

// rawConsolePost writes one request by hand, announcing length bytes of body
// and sending body, then closes its side of the connection. A client library
// would refuse to send a body shorter than it announced, which is the case
// under test.
func rawConsolePost(t *testing.T, f *consoleFixture, path string, length int, body string) (int, string) {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", f.server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	head := "POST " + path + " HTTP/1.1\r\nHost: " + f.server.Listener.Addr().String() +
		"\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(length) + "\r\n\r\n"
	if _, err := conn.Write([]byte(head + body)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded consoleapi.ErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	return resp.StatusCode, decoded.Kind
}
