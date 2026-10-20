// Package logging builds the logger a service process writes through, and holds
// the one rule for the level of a failure whose caller went away (LevelFor).
// The operator chooses the process level with the service's own variable
// (BROKER_LOG_LEVEL, EXCHANGE_LOG_LEVEL, IDENTITY_LOG_LEVEL); the format is
// fixed at one JSON record per line.
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// levels is every value a level variable accepts, in severity order.
// ParseLevel matches against it and its error message lists it, so the two
// cannot disagree about what is allowed.
var levels = []struct {
	name  string
	level slog.Level
}{
	{"DEBUG", slog.LevelDebug},
	{"INFO", slog.LevelInfo},
	{"WARN", slog.LevelWarn},
	{"ERROR", slog.LevelError},
}

// ParseLevel reads a level name in any case. Empty means INFO, the level every
// service logged at before the setting existed, so a deployment that sets
// nothing sees no change.
//
// Anything else is an error, never a fallback. An operator who wrote a value
// meant something by it, and answering a typo with INFO is how a service ends
// up logging at a level nobody chose. That includes names other tools accept,
// such as WARNING or TRACE, and the offset forms slog's own parser takes, such
// as INFO+2: the setting has exactly four values.
func ParseLevel(raw string) (slog.Level, error) {
	if raw == "" {
		return slog.LevelInfo, nil
	}
	for _, l := range levels {
		if strings.EqualFold(raw, l.name) {
			return l.level, nil
		}
	}
	return 0, fmt.Errorf("%q is not a log level (want %s, in any case)", raw, levelNames())
}

// levelNames renders the accepted values as "DEBUG, INFO, WARN or ERROR".
func levelNames() string {
	names := make([]string, len(levels))
	for i, l := range levels {
		names[i] = l.name
	}
	last := len(names) - 1
	return strings.Join(names[:last], ", ") + " or " + names[last]
}

// Setup builds the process logger at the level raw names, writing to w, and
// installs it as the process default. levelVar is the variable raw was read
// from; it only names the setting in the error. The caller reads it with
// runhttp.EnvTrimmed, the one rule for whether a variable is set, so
// surrounding spaces are ignored and a value of only spaces counts as unset.
//
// Installing the default is what makes one setting cover the process rather
// than only the code handed this logger. Code running outside the request-id
// middleware logs through slog.Default() (see reqctx.FromContext), and so does
// the protocol SDK wherever it is not handed a logger. Lines the Go standard
// library writes through the log package, which in these services means
// net/http's own server messages, a recovered handler panic among them, are
// recorded at ERROR, so no setting hides them. One writer is outside it: the
// go-redis client keeps its own plain-text logger on standard error.
//
// A bad value comes back as an error that names the variable, together with a
// logger at INFO for the caller to report it through before exiting. That
// logger is not installed: a process that refuses to start has nothing else to
// log.
func Setup(w io.Writer, levelVar, raw string) (*slog.Logger, error) {
	level, err := ParseLevel(raw)
	if err != nil {
		return newLogger(w, slog.LevelInfo), fmt.Errorf("%s: %w", levelVar, err)
	}
	logger := newLogger(w, level)
	slog.SetDefault(logger)
	slog.SetLogLoggerLevel(slog.LevelError)
	return logger, nil
}

// LevelFor returns the level to record a failure at: level, or DEBUG when err
// is ctx ending — err is, or wraps, the error ctx reports once it has been
// cancelled or its deadline has passed.
//
// Such a failure is the caller going away, not a fault: a client disconnected
// or gave up, or the process is shutting down. The code that owns the
// operation records what it does about that, and at WARN or ERROR every
// abandoned request read like an outage or a database fault. These services
// set no request deadline of their own, so an expired one always came from the
// caller. A timeout inside the operation, such as a dependency that did not
// answer in time, leaves ctx running and keeps its level.
//
// The test is the error, not only the context: an outcome decided on other
// grounds keeps its level although the caller has left by the time it is
// recorded. That alone does not protect a refusal of the caller. A refusal can
// wait on a lookup the caller's context bounds (the key directory of an agent
// seen for the first time is fetched on it), and a caller that hangs up during
// that lookup leaves a refusal whose err is ctx ending. So LevelFor is for the
// failure of an operation done for one caller, and a refusal of the caller is
// never passed through it: its record has to stay visible whatever the caller
// does. Nor is it the tool for work several callers share: such work should not
// inherit any single caller's cancellation in the first place, so one caller
// leaving cannot fail it for the others.
func LevelFor(ctx context.Context, err error, level slog.Level) slog.Level {
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		return slog.LevelDebug
	}
	return level
}

func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}
