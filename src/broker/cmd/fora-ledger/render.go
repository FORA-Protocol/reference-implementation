package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Render writes the chain and its assertions. Text, not JSON: the audience is a
// person reading a terminal, and the values a machine would want are the ones
// the admin RPC already serves.
func Render(w io.Writer, l Ledger) error {
	var b strings.Builder
	line(&b, "FORA evidence chain\n  transaction %s\n  tenant      %s\n\n", l.TransactionID, l.TenantID)
	renderRows(&b, l.Rows)
	b.WriteString("\n")
	renderAssertions(&b, l.Assertions)
	_, err := io.WriteString(w, b.String())
	return err
}

// line writes one formatted line. Every destination here is a strings.Builder,
// directly or behind a tabwriter, and neither can fail — so the error is
// discarded in this one place rather than at every call site.
func line(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func renderRows(b *strings.Builder, rows []Row) {
	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	line(tw, "PARTY\tSTEP\tEVENT\tCORRELATOR\tCRYPTO\n")
	for _, r := range rows {
		if r.Absence != "" {
			// A gap prints its reason across the row rather than leaving empty
			// cells, which would read as a step that happened and recorded
			// nothing.
			line(tw, "%s\t%s\t— %s\n", r.Party, r.Step, r.Absence)
			continue
		}
		line(tw, "%s\t%s\t%s\t%s\t%s\n", r.Party, r.Step, r.Event, r.Correlator, r.Crypto)
	}
	// tabwriter buffers until Flush; the error it reports is the underlying
	// writer's, and a strings.Builder never fails.
	_ = tw.Flush()
}

// renderAssertions prints each verdict with one of three marks and then a
// summary that keeps them apart. A check whose source was never read is NOT a
// failed check, and collapsing the two would make an operator with a closed
// tunnel see the same output as an operator holding a forged row.
func renderAssertions(b *strings.Builder, assertions []Assertion) {
	b.WriteString("ASSERTIONS\n")
	marks := map[string]string{stateHeld: "v", stateFailed: "x", stateUnchecked: "?"}
	for _, a := range assertions {
		line(b, "  [%s] %s\n        %s\n", marks[a.State()], a.Name, a.Detail)
	}
	line(b, "\n%s\n", Summarize(assertions))
}

// The three states an assertion can be in. They are strings rather than an
// enum because both renderers key on them: the text one for its mark, the
// HTML one for its badge label and class.
const (
	stateHeld      = "held"
	stateFailed    = "failed"
	stateUnchecked = "unchecked"
)

// State names the verdict. Unchecked wins over OK because an unchecked
// assertion carries no verdict at all — its OK is simply the zero value.
func (a Assertion) State() string {
	switch {
	case a.Unchecked:
		return stateUnchecked
	case a.OK:
		return stateHeld
	default:
		return stateFailed
	}
}

// Tally counts the assertions by state.
type Tally struct {
	Held, Failed, Skipped int
}

// TallyOf counts every assertion once, by its state.
func TallyOf(assertions []Assertion) Tally {
	var t Tally
	for _, a := range assertions {
		switch a.State() {
		case stateHeld:
			t.Held++
		case stateFailed:
			t.Failed++
		default:
			t.Skipped++
		}
	}
	return t
}

// Summarize writes the one-line verdict both renderers end with. The wording
// is shared so an operator comparing a terminal run with a saved report reads
// the same sentence in both.
func Summarize(assertions []Assertion) string {
	t := TallyOf(assertions)
	var b strings.Builder
	line(&b, "%d of %d checked assertions hold", t.Held, t.Held+t.Failed)
	if t.Failed > 0 {
		line(&b, "; %d FAILED", t.Failed)
	}
	if t.Skipped > 0 {
		line(&b, "; %d could not be checked because a source was not read", t.Skipped)
	}
	b.WriteString(".")
	return b.String()
}
