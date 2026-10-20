package transport

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// TestWireReportingStatus_MapsEveryStatusBothWays holds the mapping between
// the service's reporting statuses and the console's wire values total in both
// directions. A status the service labels a row with but cannot put on the
// wire would fail the list; a wire value no service status produces would be a
// filter option that lists nothing; a value that does not read back as the
// status it came from would filter by a different status than the row shows.
func TestWireReportingStatus_MapsEveryStatusBothWays(t *testing.T) {
	t.Parallel()
	seen := map[string]service.ReportingStatus{}
	for _, s := range service.ReportingStatuses() {
		wire, err := wireReportingStatus(s)
		if err != nil {
			t.Fatalf("service status %q has no wire value: %v", s, err)
		}
		if !slices.Contains(consoleapi.AllReportingStatuses(), wire) {
			t.Fatalf("service status %q maps to %q, which is not a consoleapi status", s, wire)
		}
		if prev, dup := seen[wire]; dup {
			t.Fatalf("service statuses %q and %q both map to %q", prev, s, wire)
		}
		seen[wire] = s
		back, err := reportingStatusFromWire(wire)
		if err != nil || back != s {
			t.Fatalf("wire status %q reads back as %q (err %v), want %q", wire, back, err, s)
		}
	}
	for _, wire := range consoleapi.AllReportingStatuses() {
		if _, ok := seen[wire]; !ok {
			t.Fatalf("consoleapi status %q is produced by no service status", wire)
		}
	}
}

// TestReportingStatusFromWire_RefusesAnUnknownStatus pins the refusal: a value
// that is not a status is the caller's mistake, and the message names every
// status there is. No status is no filter at all.
func TestReportingStatusFromWire_RefusesAnUnknownStatus(t *testing.T) {
	t.Parallel()
	_, err := reportingStatusFromWire("disputed")
	e, ok := errors.AsType[*exchange.Error](err)
	if !ok || e.Kind != exchange.KindInvalidRequest {
		t.Fatalf("an unknown status = %v, want an invalid-request error", err)
	}
	for _, wire := range consoleapi.AllReportingStatuses() {
		if !strings.Contains(e.Error(), wire) {
			t.Errorf("the refusal %q does not name %q", e.Error(), wire)
		}
	}
	if s, err := reportingStatusFromWire(""); err != nil || s != "" {
		t.Fatalf("no status = %q, %v; want none and no error", s, err)
	}
}
