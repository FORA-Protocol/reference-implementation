package main

import (
	"context"
	"fmt"
	"log/slog"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/bootfixture"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/denomination"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// bootSettings are what run() resolves from the environment before anything
// connects: the registration settings and the boot fixture's plan. Both are
// checked here, so a typo in either stops the boot for the cost of a parse
// rather than of a database connection and a migration run on every restart.
type bootSettings struct {
	registration registrationConfig
	// fixture is nil when EXCHANGE_BOOT_FIXTURE_FILE is unset.
	fixture *service.BootPlan
}

func loadBootSettings(logger *slog.Logger) (bootSettings, error) {
	registration, err := loadRegistrationConfig(logger)
	if err != nil {
		return bootSettings{}, err
	}
	fixture, err := loadBootFixture(logger)
	if err != nil {
		return bootSettings{}, err
	}
	return bootSettings{registration: registration, fixture: fixture}, nil
}

// loadBootFixture reads and plans the boot fixture EXCHANGE_BOOT_FIXTURE_FILE
// names (ADR-029); this is the one reader of that setting. Unset, there is no
// fixture, and a deployment that creates its rows another way boots as before. A
// fixture that cannot be read, or that registration's rules refuse, stops the
// boot.
func loadBootFixture(logger *slog.Logger) (*service.BootPlan, error) {
	path := runhttp.EnvTrimmed("EXCHANGE_BOOT_FIXTURE_FILE")
	if path == "" {
		logger.Info("boot fixture: none configured")
		return nil, nil
	}
	f, err := bootfixture.Load(path)
	if err != nil {
		return nil, fmt.Errorf("EXCHANGE_BOOT_FIXTURE_FILE %s: %w", path, err)
	}
	plan, err := service.PlanBootFixture(f, denomination.FromEnv())
	if err != nil {
		return nil, fmt.Errorf("EXCHANGE_BOOT_FIXTURE_FILE %s: %w", path, err)
	}
	logger.Info("boot fixture loaded",
		"path", path, "sha256", plan.SHA256, "seats", len(plan.Seats), "publishers", len(plan.Publishers))
	return &plan, nil
}

// applyBootFixture creates the rows the fixture declares that are not stored
// yet, before the Exchange serves anything. A refused fixture stops the boot. A
// stored row whose settings differ from the fixture is kept, with a warning
// naming the settings, because an operator or a product surface may have
// changed it on purpose since the row was created.
func applyBootFixture(ctx context.Context, logger *slog.Logger, b tenantBoot) error {
	if b.fixture == nil {
		return nil
	}
	svc := service.NewBootFixtureServiceFromPool(b.pool, b.queries, b.keys)
	report, err := svc.Apply(ctx, *b.fixture)
	if err != nil {
		return fmt.Errorf("EXCHANGE_BOOT_FIXTURE_FILE %s: %w", b.fixture.Path, err)
	}
	for _, d := range report.Drift {
		logger.Warn("boot fixture: a stored row differs from the fixture; the stored values are kept",
			"row", d.Row, "fields", d.Fields)
	}
	logger.Info("boot fixture applied",
		"path", b.fixture.Path, "created", report.Created, "kept", len(report.Kept), "drifted", len(report.Drift))
	return nil
}
