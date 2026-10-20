package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
	"google.golang.org/protobuf/encoding/protojson"
)

// mode is how the impostor answers one agent's sub-requests.
type mode string

const (
	// modeHonest answers one result item per item sent, each bought.
	modeHonest mode = "honest"
	// modeRefuse refuses the whole sub-request with the chosen Connect code and,
	// when one is given, the chosen ErrorDetail.
	modeRefuse mode = "refuse"
	// modeDrop closes the connection without answering: an Exchange that could
	// not be reached for this call.
	modeDrop mode = "drop"
	// modeStall answers only after stallFor, longer than the Broker waits: an
	// Exchange that did not answer in time.
	modeStall mode = "stall"
	// modeFewer answers OK with one result item fewer than the items sent.
	modeFewer mode = "fewer"
	// modeMore answers OK with one result item more than the items sent.
	modeMore mode = "more"
)

// fault is one agent's chosen mode, with the refusal it carries in modeRefuse.
type fault struct {
	mode   mode
	code   connect.Code
	detail *forav1.ErrorDetail
}

// faultRequest is the control body that sets a fault. detail is a protojson
// ErrorDetail.
type faultRequest struct {
	Mode   string          `json:"mode"`
	Code   string          `json:"code"`
	Detail json.RawMessage `json:"detail"`
}

// arrival is one sub-request as it arrived, for the harness to read back.
type arrival struct {
	Exchange       string   `json:"exchange"`
	IdempotencyKey string   `json:"idempotency_key"`
	OfferIDs       []string `json:"offer_ids"`
}

// faultBook holds each agent's fault and the sub-requests that arrived for it,
// keyed by requester.domain. Every e2e test buys as an agent of its own, so a
// fault set for one test's agent never reaches another test's purchase.
type faultBook struct {
	mu       sync.Mutex
	faults   map[string]fault
	arrivals map[string][]arrival
}

func newFaultBook() *faultBook {
	return &faultBook{faults: make(map[string]fault), arrivals: make(map[string][]arrival)}
}

// register mounts the control routes.
func (b *faultBook) register(mux *http.ServeMux) {
	mux.HandleFunc("PUT /control/faults/{requester}", b.putFault)
	mux.HandleFunc("GET /control/arrivals/{requester}", b.getArrivals)
}

// faultFor is the fault set for requester, honest when none was.
func (b *faultBook) faultFor(requester string) fault {
	b.mu.Lock()
	defer b.mu.Unlock()
	if f, ok := b.faults[requester]; ok {
		return f
	}
	return fault{mode: modeHonest}
}

// record keeps one arrived sub-request for requester.
func (b *faultBook) record(requester string, a arrival) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.arrivals[requester] = append(b.arrivals[requester], a)
}

func (b *faultBook) putFault(w http.ResponseWriter, r *http.Request) {
	var body faultRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "control body: "+err.Error(), http.StatusBadRequest)
		return
	}
	f, err := parseFault(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	b.faults[r.PathValue("requester")] = f
	b.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (b *faultBook) getArrivals(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	arrived := append([]arrival{}, b.arrivals[r.PathValue("requester")]...)
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(arrived)
}

// parseFault checks a control body. A refusal needs a code that is not OK; any
// other mode takes neither a code nor a detail, so a body that asks for one
// with a mode that would ignore it is refused rather than half-applied.
func parseFault(body faultRequest) (fault, error) {
	f := fault{mode: mode(body.Mode)}
	switch f.mode {
	case modeHonest, modeDrop, modeStall, modeFewer, modeMore:
		if body.Code != "" || len(body.Detail) > 0 {
			return fault{}, fmt.Errorf("mode %q takes no code or detail", body.Mode)
		}
		return f, nil
	case modeRefuse:
	default:
		return fault{}, fmt.Errorf("unknown mode %q", body.Mode)
	}
	if err := f.code.UnmarshalText([]byte(body.Code)); err != nil {
		return fault{}, fmt.Errorf("refusal code: %w", err)
	}
	if len(body.Detail) > 0 {
		f.detail = &forav1.ErrorDetail{}
		if err := protojson.Unmarshal(body.Detail, f.detail); err != nil {
			return fault{}, fmt.Errorf("refusal detail: %w", err)
		}
	}
	return f, nil
}

// refusal is the Connect error a modeRefuse fault answers with.
func (f fault) refusal() error {
	cerr := connect.NewError(f.code, errors.New("misbehaving-exchange: the whole sub-request is refused"))
	if f.detail == nil {
		return cerr
	}
	return connectserver.AttachDetail(cerr, f.detail)
}
