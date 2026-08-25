// Package config loads the runtime configuration from the environment. No
// secret, password or token is ever compiled into the binary or committed.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// Environment is a free-form deployment label used in logs.
	Environment string
	// HTTPAddress is the listen address of the API server.
	HTTPAddress string
	// DatabasePath is the SQLite file backing the service.
	DatabasePath string
	// DatabaseBusyTimeout is how long a writer waits for a competing writer.
	DatabaseBusyTimeout time.Duration
	// DatabaseMaxOpenConns bounds the connection pool.
	DatabaseMaxOpenConns int
	// SessionTTL is the lifetime of a freshly issued session.
	SessionTTL time.Duration
	// IdempotencyTTL is how long a replay record stays valid.
	IdempotencyTTL time.Duration
	// RequestTimeout bounds the handling of a single HTTP request.
	RequestTimeout time.Duration
	// ShutdownTimeout bounds the graceful shutdown of the server.
	ShutdownTimeout time.Duration
	// NotificationInterval is the polling interval of the outbox dispatcher.
	NotificationInterval time.Duration
	// NotificationBatch is how many outbox rows one dispatcher tick claims.
	NotificationBatch int
	// SweeperInterval is the polling interval of the expiry sweeper.
	SweeperInterval time.Duration
	// SweeperBatch is how many expired matches one sweeper tick handles.
	SweeperBatch int
	// LogLevel is one of debug, info, warn or error.
	LogLevel string
	// BootstrapAdminEmail optionally seeds an operations account at startup.
	BootstrapAdminEmail string
	// BootstrapAdminPassword is the password of the seeded operations account.
	BootstrapAdminPassword string
}

// Prefix is prepended to every environment variable of the service.
const Prefix = "HEARTBRIDGE_"

// Load resolves the configuration from the process environment, applying the
// documented defaults and validating the result.
func Load() (Config, error) {
	cfg := Config{
		Environment:            stringVar("ENV", "development"),
		HTTPAddress:            stringVar("HTTP_ADDR", ":8080"),
		DatabasePath:           stringVar("DB_PATH", "data/heartbridge.sqlite"),
		LogLevel:               strings.ToLower(stringVar("LOG_LEVEL", "info")),
		BootstrapAdminEmail:    stringVar("BOOTSTRAP_ADMIN_EMAIL", ""),
		BootstrapAdminPassword: stringVar("BOOTSTRAP_ADMIN_PASSWORD", ""),
	}

	var err error
	if cfg.DatabaseBusyTimeout, err = durationVar("DB_BUSY_TIMEOUT", 5*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseMaxOpenConns, err = intVar("DB_MAX_OPEN_CONNS", 8); err != nil {
		return Config{}, err
	}
	if cfg.SessionTTL, err = durationVar("SESSION_TTL", 12*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.IdempotencyTTL, err = durationVar("IDEMPOTENCY_TTL", 24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = durationVar("REQUEST_TIMEOUT", 15*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = durationVar("SHUTDOWN_TIMEOUT", 20*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.NotificationInterval, err = durationVar("NOTIFICATION_INTERVAL", 2*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.NotificationBatch, err = intVar("NOTIFICATION_BATCH", 20); err != nil {
		return Config{}, err
	}
	if cfg.SweeperInterval, err = durationVar("SWEEPER_INTERVAL", 30*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.SweeperBatch, err = intVar("SWEEPER_BATCH", 50); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects a configuration the service cannot run with.
func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTPAddress) == "" {
		return apperr.New(apperr.CodeInvalidArgument, Prefix+"HTTP_ADDR must not be empty")
	}
	if strings.TrimSpace(c.DatabasePath) == "" {
		return apperr.New(apperr.CodeInvalidArgument, Prefix+"DB_PATH must not be empty")
	}
	if c.DatabaseMaxOpenConns < 1 {
		return apperr.New(apperr.CodeInvalidArgument, Prefix+"DB_MAX_OPEN_CONNS must be at least 1")
	}
	if c.SessionTTL <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, Prefix+"SESSION_TTL must be positive")
	}
	if c.IdempotencyTTL <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, Prefix+"IDEMPOTENCY_TTL must be positive")
	}
	if c.RequestTimeout <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, Prefix+"REQUEST_TIMEOUT must be positive")
	}
	if c.ShutdownTimeout <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, Prefix+"SHUTDOWN_TIMEOUT must be positive")
	}
	if c.NotificationInterval <= 0 || c.SweeperInterval <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, "worker intervals must be positive")
	}
	if c.NotificationBatch < 1 || c.SweeperBatch < 1 {
		return apperr.New(apperr.CodeInvalidArgument, "worker batch sizes must be at least 1")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return apperr.Newf(apperr.CodeInvalidArgument,
			"%sLOG_LEVEL must be debug, info, warn or error, got %q", Prefix, c.LogLevel)
	}
	if (c.BootstrapAdminEmail == "") != (c.BootstrapAdminPassword == "") {
		return apperr.New(apperr.CodeInvalidArgument,
			"bootstrap admin email and password must be provided together")
	}
	return nil
}

// BootstrapEnabled reports whether an operations account should be seeded.
func (c Config) BootstrapEnabled() bool {
	return c.BootstrapAdminEmail != "" && c.BootstrapAdminPassword != ""
}

// Redacted renders the configuration for logs, leaving credentials out.
func (c Config) Redacted() string {
	bootstrap := "disabled"
	if c.BootstrapEnabled() {
		bootstrap = "enabled"
	}
	return fmt.Sprintf(
		"env=%s addr=%s db=%s session_ttl=%s request_timeout=%s bootstrap_admin=%s log_level=%s",
		c.Environment, c.HTTPAddress, c.DatabasePath, c.SessionTTL, c.RequestTimeout, bootstrap, c.LogLevel,
	)
}

// stringVar reads a prefixed string variable.
func stringVar(name, fallback string) string {
	if value, ok := os.LookupEnv(Prefix + name); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

// intVar reads a prefixed integer variable.
func intVar(name string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(Prefix + name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, apperr.Wrapf(apperr.CodeInvalidArgument, err,
			"%s%s must be an integer", Prefix, name)
	}
	return parsed, nil
}

// durationVar reads a prefixed Go duration variable such as "15s".
func durationVar(name string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(Prefix + name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, apperr.Wrapf(apperr.CodeInvalidArgument, err,
			"%s%s must be a Go duration such as 15s", Prefix, name)
	}
	return parsed, nil
}
