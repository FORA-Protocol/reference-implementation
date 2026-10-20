package purchase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// TestOutcomeLevel pins the one level rule of the purchase audit for every
// outcome: INFO for a success; WARN for a refusal of the caller, whatever the
// caller's context does; and for a fault, ERROR on the Broker's side or WARN at
// an Exchange, dropping to DEBUG only when the fault's cause is the request's
// own context ending.
func TestOutcomeLevel(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	unrelated := errors.New("registry read failed")
	wrapped := fmt.Errorf("exchange registry: %w", cancelled.Err())

	contexts := []struct {
		name string
		ctx  context.Context
		err  error
		left bool // err is the request's own context ending
	}{
		{"live context", context.Background(), unrelated, false},
		{"cancelled context, unrelated error", cancelled, unrelated, false},
		{"cancelled context, its cancellation", cancelled, cancelled.Err(), true},
		{"cancelled context, its cancellation wrapped", cancelled, wrapped, true},
		{"context past its deadline, the deadline", expired, expired.Err(), true},
	}
	fault := func(level slog.Level) func(bool) slog.Level {
		return func(left bool) slog.Level {
			if left {
				return slog.LevelDebug
			}
			return level
		}
	}
	fixed := func(level slog.Level) func(bool) slog.Level {
		return func(bool) slog.Level { return level }
	}
	want := map[outcome]func(left bool) slog.Level{
		outcomeValidated:              fixed(slog.LevelInfo),
		outcomeRejectedAuthz:          fixed(slog.LevelWarn),
		outcomeRejectedInvalid:        fixed(slog.LevelWarn),
		outcomeRejectedExchange:       fixed(slog.LevelWarn),
		outcomeRejectedInternal:       fault(slog.LevelError),
		outcomeUpstreamRefused:        fault(slog.LevelWarn),
		outcomeUpstreamUnattributable: fixed(slog.LevelWarn),
		outcomeTotalsOmitted:          fixed(slog.LevelWarn),
	}
	for o, rule := range want {
		for _, c := range contexts {
			if got, wantLevel := o.level(c.ctx, c.err), rule(c.left); got != wantLevel {
				t.Errorf("%s with %s: level %v, want %v", o, c.name, got, wantLevel)
			}
		}
	}
}
