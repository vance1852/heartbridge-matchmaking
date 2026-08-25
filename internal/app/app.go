// Package app wires the configuration, storage, services, HTTP layer and
// background workers into one runnable application.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/auditlog"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/config"
	"github.com/vance1852/heartbridge-matchmaking/internal/httpapi"
	"github.com/vance1852/heartbridge-matchmaking/internal/idempotency"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository/sqliterepo"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/adminsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/authsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/membersvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/schedulesvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/storage/sqlitedb"
	"github.com/vance1852/heartbridge-matchmaking/internal/worker"
	"github.com/vance1852/heartbridge-matchmaking/migrations"
)

// Version is the build label reported by the health endpoints.
const Version = "1.0.0"

// Application is a fully wired instance of the service.
type Application struct {
	Config       config.Config
	Logger       *slog.Logger
	DB           *sqlitedb.DB
	Repositories sqliterepo.Set
	Auth         *authsvc.Service
	Members      *membersvc.Service
	Matches      *matchsvc.Service
	Schedule     *schedulesvc.Service
	Admin        *adminsvc.Service
	Guard        *idempotency.Guard
	Dispatcher   *worker.Dispatcher
	Sweeper      *worker.Sweeper
	Handler      http.Handler
	Clock        clock.Clock
}

// Options tunes how an application instance is built.
type Options struct {
	// Clock overrides wall-clock access; tests inject a deterministic clock.
	Clock clock.Clock
	// Logger overrides the structured logger.
	Logger *slog.Logger
	// Sender overrides the notification gateway.
	Sender worker.Sender
	// SkipMigrations is only used by callers that already migrated the database.
	SkipMigrations bool
}

// New builds and migrates an application instance.
func New(ctx context.Context, cfg config.Config, options Options) (*Application, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	logger := options.Logger
	if logger == nil {
		logger = logging.New(os.Stdout, cfg.LogLevel)
	}
	source := options.Clock
	if source == nil {
		source = clock.System{}
	}
	if err := ensureParentDirectory(cfg.DatabasePath); err != nil {
		return nil, err
	}

	db, err := sqlitedb.Open(ctx, sqlitedb.Options{
		Path:         cfg.DatabasePath,
		BusyTimeout:  cfg.DatabaseBusyTimeout,
		MaxOpenConns: cfg.DatabaseMaxOpenConns,
	})
	if err != nil {
		return nil, err
	}
	if !options.SkipMigrations {
		applied, err := db.Migrate(ctx, migrations.FS)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		for _, migration := range applied {
			logger.Info("migration applied", "version", migration.Version, "name", migration.Name)
		}
	}

	repos := sqliterepo.New(db)
	recorder := auditlog.NewRecorder(repos.Audit, source)
	guard := idempotency.NewGuard(repos.Idempotency, source, cfg.IdempotencyTTL)

	authService := authsvc.New(authsvc.Dependencies{
		Tx: db, Users: repos.Users, Sessions: repos.Sessions, Members: repos.Members,
		Audit: recorder, Clock: source, SessionTTL: cfg.SessionTTL,
	})
	memberService := membersvc.New(membersvc.Dependencies{
		Tx: db, Members: repos.Members, Entitlements: repos.Entitlements,
		Audit: recorder, Clock: source,
	})
	matchService := matchsvc.New(matchsvc.Dependencies{
		Tx: db, Matches: repos.Matches, Members: repos.Members, Entitlements: repos.Entitlements,
		Meetups: repos.Meetups, Slots: repos.Slots, Notifications: repos.Notifications,
		Audit: recorder, Clock: source,
	})
	scheduleService := schedulesvc.New(schedulesvc.Dependencies{
		Tx: db, Matches: repos.Matches, Meetups: repos.Meetups, Slots: repos.Slots,
		Entitlements: repos.Entitlements, Notifications: repos.Notifications,
		Audit: recorder, Clock: source,
	})
	adminService := adminsvc.New(adminsvc.Dependencies{
		Tx: db, Slots: repos.Slots, Entitlements: repos.Entitlements,
		Audit: recorder, Clock: source,
	})

	sender := options.Sender
	if sender == nil {
		sender = worker.DiscardSender{}
	}
	dispatcher := worker.NewDispatcher(repos.Notifications, sender, source, cfg.NotificationBatch)
	sweeper := worker.NewSweeper(matchService, authService, guard, source, cfg.SweeperBatch)

	application := &Application{
		Config: cfg, Logger: logger, DB: db, Repositories: repos,
		Auth: authService, Members: memberService, Matches: matchService,
		Schedule: scheduleService, Admin: adminService, Guard: guard,
		Dispatcher: dispatcher, Sweeper: sweeper, Clock: source,
	}
	server := httpapi.NewServer(httpapi.Dependencies{
		Auth: authService, Members: memberService, Matches: matchService,
		Schedule: scheduleService, Admin: adminService, Guard: guard,
		Clock: source, Readiness: application.Ready, Version: Version,
	})
	application.Handler = server.Handler(cfg.RequestTimeout)

	if err := application.bootstrap(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return application, nil
}

// ensureParentDirectory creates the directory holding the database file.
func ensureParentDirectory(path string) error {
	directory := filepath.Dir(path)
	if directory == "" || directory == "." {
		return nil
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "create database directory", err)
	}
	return nil
}

// Ready is the readiness probe: it verifies the database answers and that the
// schema is at the expected version.
func (a *Application) Ready(ctx context.Context) error {
	if err := a.DB.Ping(ctx); err != nil {
		return err
	}
	version, err := a.DB.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if version <= 0 {
		return apperr.New(apperr.CodeUnavailable, "database schema has not been migrated")
	}
	return nil
}

// Close releases the resources of the application.
func (a *Application) Close() error {
	if a == nil || a.DB == nil {
		return nil
	}
	return a.DB.Close()
}

// Run serves HTTP and the background loops until ctx is cancelled, then shuts
// down gracefully within the configured timeout.
func (a *Application) Run(ctx context.Context) error {
	loopCtx, stopLoops := context.WithCancel(logging.WithLogger(ctx, a.Logger))
	defer stopLoops()

	group := &worker.Group{}
	group.StartDispatcher(loopCtx, a.Dispatcher, a.Config.NotificationInterval)
	group.StartSweeper(loopCtx, a.Sweeper, a.Config.SweeperInterval)

	server := &http.Server{
		Addr:              a.Config.HTTPAddress,
		Handler:           logging.Middleware(a.Logger)(a.Handler),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		a.Logger.Info("http server listening", "address", a.Config.HTTPAddress, "config", a.Config.Redacted())
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		stopLoops()
		group.Wait()
		return err
	case <-ctx.Done():
		a.Logger.Info("shutdown requested")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.Config.ShutdownTimeout)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	stopLoops()
	for _, err := range group.Wait() {
		if shutdownErr == nil {
			shutdownErr = err
		}
	}
	if err := <-serveErr; err != nil && shutdownErr == nil {
		shutdownErr = err
	}
	a.Logger.Info("shutdown complete")
	return shutdownErr
}

// TxManager exposes the transaction manager, which is the database handle.
func (a *Application) TxManager() repository.TxManager { return a.DB }
