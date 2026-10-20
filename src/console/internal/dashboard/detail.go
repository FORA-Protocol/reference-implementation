package dashboard

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/evidenceview"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/refusal"
)

// TransactionView is one transaction: what was sold and on what terms, to
// whom, where its reporting stands, and the signed evidence of it.
type TransactionView struct {
	ID     string
	When   string
	Status Status
	// Agent is the agent that bought.
	Agent string
	// Offer is what the agent accepted, read from the bytes the Exchange
	// signed. It is nil when those bytes do not read as an offer.
	Offer *OfferView
	// Reporting says where the agent's usage report stands, in a sentence
	// built from the reporting status and the obligation the Exchange
	// returned.
	Reporting  string
	Obligation *ObligationView
	// SignedURLHash is the SHA-256 of the delivery address the agent was
	// given, in base64. The address itself is never shown.
	SignedURLHash string
	// Evidence is the signed bytes, signatures and keys the page's Verify
	// button checks in the browser, as JSON.
	Evidence string
	// List is the transaction list over the month the transaction was made in.
	List string
}

// OfferView is the signed offer's terms, as a publisher reads them.
type OfferView struct {
	ID    string
	Title string
	URL   string
	// Price is the price the offer stated; UnitCost is the per-unit cost the
	// Exchange normalised it to.
	Price string
	// Terms says each license term the offer carried, one line per term.
	Terms             []string
	UnitCost          string
	EstimatedQuantity string
	// ReportWindow is the time the offer gave the agent to report its usage,
	// when the offer set one of its own. Empty when it left that to the
	// seat's policy, which the domains page states.
	ReportWindow string
	Expires      string
}

// ObligationView is where the agent's usage report stands.
type ObligationView struct {
	State     string
	Due       string
	Consumed  string
	Fulfilled string
	Outcome   string
}

// Transaction reads one of the publisher's transactions.
func (s *Service) Transaction(
	ctx context.Context, actor exchangeclient.Actor, nav Nav, transactionID string,
) (TransactionView, error) {
	d, err := s.reader.TransactionDetail(ctx, exchangeclient.TransactionDetailRequest{
		Actor: actor, PublisherID: nav.PublisherID, TransactionID: transactionID,
	})
	if err != nil {
		return TransactionView{}, failure(err)
	}
	if d.Evidence == nil {
		return TransactionView{}, refusal.New(refusal.KindNotFound, notFoundText)
	}
	evidence, err := verifyPayload(d.Evidence)
	if err != nil {
		return TransactionView{}, err
	}
	view := TransactionView{
		ID:         d.Evidence.TransactionID,
		When:       timeText(d.Evidence.CreatedAt),
		Status:     statusOf(d.ReportingStatus),
		Agent:      d.Evidence.RequesterID,
		Offer:      offerView(d.Evidence.OfferCanonicalBytes),
		Reporting:  reportingText(d.ReportingStatus, d.ObligationState),
		Obligation: obligationView(d.ObligationState),
		Evidence:   evidence,
		List:       periodLink(nav.Transactions(), MonthOf(d.Evidence.CreatedAt, s.clk.Now())),
	}
	if st := d.TransactionState; st != nil {
		view.Agent = cmp.Or(st.AgentID, view.Agent)
		view.SignedURLHash = base64.StdEncoding.EncodeToString(st.SignedURLHash)
	}
	return view, nil
}

// offerView reads the offer from the exact bytes the Exchange signed. They are
// the offer's canonical JSON with the signature left out, so they read back as
// the offer the agent was shown. Bytes that do not read give no view: the
// page then says so, and the Verify button still checks them.
func offerView(raw []byte) *OfferView {
	var offer forav1.Offer
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &offer); err != nil {
		return nil
	}
	p := offer.GetPricing()
	view := &OfferView{
		ID:       offer.GetOfferId(),
		Title:    offer.GetTitle(),
		URL:      offer.GetIdentity().GetCanonicalUrl(),
		Price:    termspreset.PriceText(p),
		Terms:    otherTerms(describeTerms(offer.GetTerms()), termspreset.PriceText(p)),
		UnitCost: p.GetUnitCost(),
	}
	if w := offer.GetReporting().GetWindow(); w != nil {
		view.ReportWindow = durationText(w.AsDuration())
	}
	if p.EstimatedQuantity != nil {
		view.EstimatedQuantity = strconv.Itoa(int(p.GetEstimatedQuantity()))
	}
	if offer.ExpiresAt != nil {
		view.Expires = timeText(offer.GetExpiresAt().AsTime())
	}
	return view
}

// reportingText says where the usage report stands, from the reporting status
// the Exchange derived and the obligation it returned. Whether a report is
// owed at all is the Exchange's rule; the page only says what it answered. A
// status it does not know is shown as the Exchange wrote it.
func reportingText(status string, o *evidenceview.ObligationState) string {
	due := ""
	if o != nil {
		due = timeText(o.WindowEnd)
	}
	switch status {
	case consoleapi.ReportingNotRequired:
		return "No usage report is owed for this sale."
	case consoleapi.ReportingPending:
		return "The agent owes a usage report by " + due + "."
	case consoleapi.ReportingOverdue:
		return "The agent's usage report was due by " + due + " and has not been received."
	case consoleapi.ReportingReceived:
		return "The agent's usage report was received."
	default:
		return "Report status: " + status + "."
	}
}

func obligationView(o *evidenceview.ObligationState) *ObligationView {
	if o == nil {
		return nil
	}
	view := &ObligationView{State: o.State, Due: timeText(o.WindowEnd)}
	if o.ConsumedQuantity != nil {
		view.Consumed = *o.ConsumedQuantity
	}
	if o.FulfilledAt != nil {
		view.Fulfilled = timeText(*o.FulfilledAt)
	}
	if o.ValidationOutcome != nil {
		view.Outcome = *o.ValidationOutcome
	}
	return view
}

// verifyInput is what the browser checks: each signature, the bytes it signs,
// and the key it verifies under. Bytes and keys are standard base64, and
// signatures hex, as the Exchange serves them.
type verifyInput struct {
	OfferCanonicalBytes           []byte `json:"offer_canonical_bytes"`
	OfferSig                      string `json:"offer_sig"`
	ExchangeSigningPublicKey      []byte `json:"exchange_signing_public_key"`
	AgentAcceptanceCanonicalBytes []byte `json:"agent_acceptance_canonical_bytes"`
	AgentAcceptanceSignature      string `json:"agent_acceptance_signature"`
	AgentPublicKey                []byte `json:"agent_public_key"`
}

func verifyPayload(e *evidenceview.Evidence) (string, error) {
	raw, err := json.Marshal(verifyInput{
		OfferCanonicalBytes:           e.OfferCanonicalBytes,
		OfferSig:                      e.OfferSig,
		ExchangeSigningPublicKey:      e.ExchangeSigningPublicKey,
		AgentAcceptanceCanonicalBytes: e.AgentAcceptanceCanonicalBytes,
		AgentAcceptanceSignature:      e.AgentAcceptanceSignature,
		AgentPublicKey:                e.AgentPublicKey,
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// durationText writes a reporting window in the largest whole unit it is.
func durationText(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d >= time.Hour && d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d/time.Minute), "minute")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// otherTerms drops the lines that only repeat the offer's price, which the
// page already shows: a term that states nothing but its price says nothing
// the price line does not.
func otherTerms(lines []string, price string) []string {
	return slices.DeleteFunc(lines, func(line string) bool { return line == price })
}
