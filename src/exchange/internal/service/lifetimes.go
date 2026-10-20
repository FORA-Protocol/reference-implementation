package service

import "time"

// Default lifetimes of what the Exchange issues: an offer stays valid this long
// after discovery, and a delivery URL this long after its purchase. ExchangeConfig
// falls back to them when OfferTTL or URLTTL is unset.
const (
	DefaultOfferTTL = 5 * time.Minute
	DefaultURLTTL   = 5 * time.Minute
)

// Lifetimes overrides, per tenant, how long the offers and delivery URLs the
// Exchange issues stay valid. Each method reports the tenant's override and
// whether it has one.
//
// Production wires none (ExchangeDeps.Lifetimes is nil), so every tenant gets the
// configured lifetimes. The e2e build wires its control service here (ADR-028 D6),
// so a test can make one seat issue short-lived offers and URLs and then wait for
// them to expire.
type Lifetimes interface {
	OfferTTL(tenantID string) (time.Duration, bool)
	URLTTL(tenantID string) (time.Duration, bool)
}

// offerTTL is how long an offer issued for tenantID stays valid.
func (s *ExchangeService) offerTTL(tenantID string) time.Duration {
	if s.lifetimes == nil {
		return s.cfg.OfferTTL
	}
	override, ok := s.lifetimes.OfferTTL(tenantID)
	return shortenedTTL(s.cfg.OfferTTL, override, ok)
}

// urlTTL is how long a delivery URL issued for tenantID stays valid.
func (s *ExchangeService) urlTTL(tenantID string) time.Duration {
	if s.lifetimes == nil {
		return s.cfg.URLTTL
	}
	override, ok := s.lifetimes.URLTTL(tenantID)
	return shortenedTTL(s.cfg.URLTTL, override, ok)
}

// shortenedTTL applies an override only when it shortens the configured lifetime.
// An override can therefore never extend an offer or a delivery URL past what the
// deployment configured, and a zero or negative one is ignored rather than
// issuing something that is expired on arrival.
func shortenedTTL(configured, override time.Duration, ok bool) time.Duration {
	if !ok || override <= 0 || override >= configured {
		return configured
	}
	return override
}
