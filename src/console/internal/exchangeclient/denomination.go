package exchangeclient

import (
	"context"
	"sync"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

// denominationTTL is how long one answer about the Exchange's denomination is
// reused. The denomination is deployment configuration and changes only when
// the Exchange is reconfigured, so asking on every preview would be a signed
// round trip per keystroke for an answer that does not move. A minute bounds
// how long the console previews in a denomination the Exchange has dropped.
const denominationTTL = time.Minute

// denominationCache holds the last answer and when it was read.
type denominationCache struct {
	mu      sync.Mutex
	value   termspreset.Defaults
	fetched time.Time
}

// TermsDefaults reports the currency and metering unit the Exchange prices
// every registered arrangement in. The console prices its previews and its
// generated files in exactly this, so what a publisher is shown is what the
// Exchange stores; the console has no denomination of its own to configure.
//
// A failure is returned rather than papered over with a guess: a preview in
// the wrong currency is worse than a page that says the Exchange is not
// answering.
func (c *Client) TermsDefaults(ctx context.Context) (termspreset.Defaults, error) {
	c.denomination.mu.Lock()
	defer c.denomination.mu.Unlock()
	if !c.denomination.fetched.IsZero() && c.clk.Now().Sub(c.denomination.fetched) < denominationTTL {
		return c.denomination.value, nil
	}
	var out consoleapi.Denomination
	if err := c.call(ctx, consoleapi.PathDenomination, consoleapi.DenominationRequest{}, &out); err != nil {
		return termspreset.Defaults{}, err
	}
	c.denomination.value = termspreset.Defaults{Currency: out.Currency, Unit: out.Unit}
	c.denomination.fetched = c.clk.Now()
	return c.denomination.value, nil
}
