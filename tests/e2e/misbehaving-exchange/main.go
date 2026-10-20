// Command misbehaving-exchange is the e2e suite's Exchange impostor: a party
// the Broker routes to like any Exchange, which answers the Broker's
// sub-requests the way a test tells it to.
//
// The relay obligations of BrokerService.ExecuteTransaction, in fora.proto of
// the pinned module github.com/FORA-Protocol/protocol, describe what the Broker
// does when an Exchange refuses a whole sub-request, does not answer, answers
// too slowly or answers with the wrong number of items. The reference Exchange
// does none of those things on purpose, so the suite needs a party that does.
//
// What it serves, for each identity it answers as (IMPOSTOR_IDENTITIES, each a
// bare host:port that a compose network alias resolves to this container):
//
//   - /.well-known/fora.json, an Exchange manifest naming the identity and an
//     endpoint on it, built and checked by the producer every in-repo service
//     uses, so the Broker's registry probe finds the identity live and its
//     endpoint resolver finds where to send a purchase;
//   - its Web Bot Auth directory, publishing the one key it signs offers with;
//   - ExchangeService.DiscoverResources, answering every requested URI with one
//     offer signed by the SDK's offer signer, so an agent's SDK verifies it the
//     way it verifies any Exchange's offer;
//   - ExchangeService.ExecuteTransaction, answered in the fault mode the test
//     set for the buying agent (see faults.go);
//   - /control/, where the harness sets that mode and reads back which
//     sub-requests arrived.
//
// It verifies no request signature and keeps no ledger. It exists to be the
// other side of the Broker's relay, nothing more, and no production image
// builds it.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
)

const (
	addrEnv       = "IMPOSTOR_ADDR"
	defaultAddr   = ":8081"
	identitiesEnv = "IMPOSTOR_IDENTITIES"
)

func main() {
	runhttp.MaybeHealthcheck(addrEnv, defaultAddr)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	handler, err := build(clock.System{}, os.Getenv(identitiesEnv))
	if err != nil {
		logger.Error("misbehaving-exchange.exit", "err", err)
		os.Exit(1)
	}
	runhttp.Serve("misbehaving-exchange", runhttp.EnvOr(addrEnv, defaultAddr), handler, logger)
}

// build assembles every route the impostor serves, for the comma-separated
// identities it answers as.
func build(clk clock.Clock, identityList string) (http.Handler, error) {
	identities, err := parseIdentities(identityList)
	if err != nil {
		return nil, err
	}
	priv, _, err := helpers.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("offer key: %w", err)
	}
	docs, err := newDocuments(identities, priv, clk)
	if err != nil {
		return nil, err
	}
	book := newFaultBook()
	mux := http.NewServeMux()
	// The generated handler, without the SDK server face: the impostor verifies
	// no request signature. It answers JSON in the FORA wire form, as every
	// FORA server does, through the SDK's codec.
	path, exchangeHandler := forav1connect.NewExchangeServiceHandler(&impostor{
		key: priv, clk: clk, book: book, identities: identities,
	}, connect.WithCodec(connectserver.EmitUnpopulatedJSONCodec()))
	mux.Handle(path, exchangeHandler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET "+forawellknown.Path, docs.serveManifest)
	mux.HandleFunc("GET "+forawellknown.WBAPath, docs.serveDirectory)
	book.register(mux)
	return mux, nil
}

// parseIdentities reads the identity list. Every entry must be a bare host, the
// form an offer's exchange and a manifest's domain take.
func parseIdentities(list string) (map[string]bool, error) {
	identities := make(map[string]bool)
	for _, raw := range strings.Split(list, ",") {
		identity := strings.TrimSpace(raw)
		if identity == "" {
			continue
		}
		if !helpers.IsBareDomain(identity) {
			return nil, fmt.Errorf("%s: %q is not a bare host", identitiesEnv, identity)
		}
		identities[identity] = true
	}
	if len(identities) == 0 {
		return nil, fmt.Errorf("%s names no identity to answer as", identitiesEnv)
	}
	return identities, nil
}
