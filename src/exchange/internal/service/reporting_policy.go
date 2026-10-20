package service

import (
	"encoding/json"
	"fmt"
	"time"
)

// reportingPolicy is the JSON shape the tenant's reporting_policy column
// carries. All fields optional; missing keys fall back to defaults.
type reportingPolicy struct {
	RequiredFields    []string `json:"required_fields,omitempty"`
	QuantityTolerance *float64 `json:"quantity_tolerance,omitempty"`
	// WindowSeconds, when set, overrides cfg.ReportWindow for new obligations
	// minted against this tenant. The §B independent finding called for
	// sourcing the window from the offer's ReportingObligation.window, but
	// the wire TransactionRequest carries no reporting block — so the
	// tenant-policy JSONB is the substitute granularity that does not
	// require a protocol bump.
	WindowSeconds *int32 `json:"window_seconds,omitempty"`
}

// decodeReportingPolicy unmarshals the raw JSONB blob. A blank / missing /
// malformed policy yields a zero-value struct (no required fields, tolerance
// falls back to the service default) — we deliberately do not surface decode
// errors to the request path because the column has a `{}` DEFAULT and any
// older row that survives is safe to treat as "no policy".
func decodeReportingPolicy(raw []byte) reportingPolicy {
	var p reportingPolicy
	if len(raw) == 0 {
		return p
	}
	_ = json.Unmarshal(raw, &p)
	return p
}

// encodeReportingPolicy marshals a reporting policy to the JSONB the tenants
// column stores. omitempty on the struct means an omitted optional (nil
// tolerance/window, empty required_fields) is absent from the JSON, so the read
// side (decodeReportingPolicy) falls back to the Exchange default — the encode
// dual used by the admin SetReportingPolicy full-replace write.
func encodeReportingPolicy(required []string, tol *float64, window *int32) ([]byte, error) {
	b, err := json.Marshal(reportingPolicy{
		RequiredFields:    required,
		QuantityTolerance: tol,
		WindowSeconds:     window,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal reporting policy: %w", err)
	}
	return b, nil
}

// windowSecondsForObligation returns the obligation's reporting window:
// tenants.reporting_policy.window_seconds when set (so publishers can shorten
// or extend the protocol default per tenant), otherwise the service default
// from cfg.ReportWindow. Encoded as int32 seconds on the obligation row.
func windowSecondsForObligation(policy reportingPolicy, fallback time.Duration) int32 {
	if policy.WindowSeconds != nil && *policy.WindowSeconds > 0 {
		return *policy.WindowSeconds
	}
	return int32(fallback.Seconds())
}

// DefaultReportWindow is how long an agent has to file a usage report when the
// seat's reporting policy sets no window.
const DefaultReportWindow = 24 * time.Hour

// EffectivePolicy is a seat's reporting policy with the Exchange's defaults
// filled in, rather than what an operator configured. The obligation planner
// and the publisher's terms read both resolve it here, so they fill the
// defaults the same way. Two things still differ per obligation: the planner
// drops billing_id from the required fields of a free sale, which stores no
// billing id, and the window is the one each service was configured with,
// which the composition root passes to both from one value.
type EffectivePolicy struct {
	RequiredFields    []string
	QuantityTolerance float64
	WindowSeconds     int32
}

// effectiveReportingPolicy resolves the stored policy against the defaults:
// defaultQuantityTolerance when the policy sets no tolerance, and fallbackWindow
// when it sets no window. RequiredFields is never nil.
func effectiveReportingPolicy(raw []byte, fallbackWindow time.Duration) EffectivePolicy {
	policy := decodeReportingPolicy(raw)
	tolerance := defaultQuantityTolerance
	if policy.QuantityTolerance != nil {
		tolerance = *policy.QuantityTolerance
	}
	fields := policy.RequiredFields
	if fields == nil {
		fields = []string{}
	}
	return EffectivePolicy{
		RequiredFields:    fields,
		QuantityTolerance: tolerance,
		WindowSeconds:     windowSecondsForObligation(policy, fallbackWindow),
	}
}
