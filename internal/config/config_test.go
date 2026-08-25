package config

import (
	"strings"
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// TestLoadAppliesDocumentedDefaults verifies the configuration of a plain process
// without any environment override.
func TestLoadAppliesDocumentedDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if cfg.HTTPAddress != ":8080" {
		t.Fatalf("unexpected default address %q", cfg.HTTPAddress)
	}
	if cfg.DatabasePath != "data/heartbridge.sqlite" {
		t.Fatalf("unexpected default database path %q", cfg.DatabasePath)
	}
	if cfg.SessionTTL != 12*time.Hour {
		t.Fatalf("unexpected default session lifetime %s", cfg.SessionTTL)
	}
	if cfg.RequestTimeout != 15*time.Second {
		t.Fatalf("unexpected default request timeout %s", cfg.RequestTimeout)
	}
	if cfg.DatabaseMaxOpenConns < 2 {
		t.Fatalf("expected the pool to allow real contention, got %d", cfg.DatabaseMaxOpenConns)
	}
	if cfg.BootstrapEnabled() {
		t.Fatal("expected no operations account to be seeded by default")
	}
}

// TestLoadReadsEnvironmentOverrides verifies every parsed variable.
func TestLoadReadsEnvironmentOverrides(t *testing.T) {
	t.Setenv(Prefix+"ENV", "staging")
	t.Setenv(Prefix+"HTTP_ADDR", "127.0.0.1:9090")
	t.Setenv(Prefix+"DB_PATH", "/var/lib/heartbridge/db.sqlite")
	t.Setenv(Prefix+"DB_BUSY_TIMEOUT", "7s")
	t.Setenv(Prefix+"DB_MAX_OPEN_CONNS", "12")
	t.Setenv(Prefix+"SESSION_TTL", "45m")
	t.Setenv(Prefix+"IDEMPOTENCY_TTL", "6h")
	t.Setenv(Prefix+"REQUEST_TIMEOUT", "3s")
	t.Setenv(Prefix+"SHUTDOWN_TIMEOUT", "9s")
	t.Setenv(Prefix+"NOTIFICATION_INTERVAL", "500ms")
	t.Setenv(Prefix+"NOTIFICATION_BATCH", "5")
	t.Setenv(Prefix+"SWEEPER_INTERVAL", "2s")
	t.Setenv(Prefix+"SWEEPER_BATCH", "7")
	t.Setenv(Prefix+"LOG_LEVEL", "DEBUG")
	t.Setenv(Prefix+"BOOTSTRAP_ADMIN_EMAIL", "ops@example.test")
	t.Setenv(Prefix+"BOOTSTRAP_ADMIN_PASSWORD", "opsPassword2026")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if cfg.Environment != "staging" || cfg.HTTPAddress != "127.0.0.1:9090" {
		t.Fatalf("unexpected deployment settings: %+v", cfg)
	}
	if cfg.DatabaseBusyTimeout != 7*time.Second || cfg.DatabaseMaxOpenConns != 12 {
		t.Fatalf("unexpected database settings: %+v", cfg)
	}
	if cfg.SessionTTL != 45*time.Minute || cfg.IdempotencyTTL != 6*time.Hour {
		t.Fatalf("unexpected lifetime settings: %+v", cfg)
	}
	if cfg.NotificationInterval != 500*time.Millisecond || cfg.NotificationBatch != 5 {
		t.Fatalf("unexpected dispatcher settings: %+v", cfg)
	}
	if cfg.SweeperInterval != 2*time.Second || cfg.SweeperBatch != 7 {
		t.Fatalf("unexpected sweeper settings: %+v", cfg)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("expected the log level to be normalised, got %q", cfg.LogLevel)
	}
	if !cfg.BootstrapEnabled() {
		t.Fatal("expected the operations account to be seeded")
	}
}

// TestLoadRejectsMalformedValues verifies that a typo fails fast instead of
// silently falling back to a default.
func TestLoadRejectsMalformedValues(t *testing.T) {
	t.Setenv(Prefix+"DB_MAX_OPEN_CONNS", "many")
	if _, err := Load(); err == nil {
		t.Fatal("expected a non numeric pool size to be refused")
	} else if code := apperr.CodeOf(err); code != apperr.CodeInvalidArgument {
		t.Fatalf("expected code %s, got %s", apperr.CodeInvalidArgument, code)
	}
	t.Setenv(Prefix+"DB_MAX_OPEN_CONNS", "8")

	t.Setenv(Prefix+"SESSION_TTL", "half an hour")
	if _, err := Load(); err == nil {
		t.Fatal("expected an unparsable duration to be refused")
	}
	t.Setenv(Prefix+"SESSION_TTL", "1h")

	t.Setenv(Prefix+"LOG_LEVEL", "chatty")
	if _, err := Load(); err == nil {
		t.Fatal("expected an unknown log level to be refused")
	}
	t.Setenv(Prefix+"LOG_LEVEL", "info")

	t.Setenv(Prefix+"BOOTSTRAP_ADMIN_EMAIL", "ops@example.test")
	if _, err := Load(); err == nil {
		t.Fatal("expected a bootstrap email without a password to be refused")
	}
}

// TestValidateRejectsImpossibleConfigurations covers the validation rules.
func TestValidateRejectsImpossibleConfigurations(t *testing.T) {
	valid := Config{
		Environment: "test", HTTPAddress: ":8080", DatabasePath: "data/db.sqlite",
		DatabaseBusyTimeout: time.Second, DatabaseMaxOpenConns: 4,
		SessionTTL: time.Hour, IdempotencyTTL: time.Hour,
		RequestTimeout: time.Second, ShutdownTimeout: time.Second,
		NotificationInterval: time.Second, NotificationBatch: 1,
		SweeperInterval: time.Second, SweeperBatch: 1, LogLevel: "info",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("expected the baseline configuration to validate: %v", err)
	}
	cases := map[string]func(Config) Config{
		"blank address":     func(c Config) Config { c.HTTPAddress = " "; return c },
		"blank db path":     func(c Config) Config { c.DatabasePath = ""; return c },
		"no connections":    func(c Config) Config { c.DatabaseMaxOpenConns = 0; return c },
		"zero session":      func(c Config) Config { c.SessionTTL = 0; return c },
		"zero idempotency":  func(c Config) Config { c.IdempotencyTTL = 0; return c },
		"zero request":      func(c Config) Config { c.RequestTimeout = 0; return c },
		"zero shutdown":     func(c Config) Config { c.ShutdownTimeout = 0; return c },
		"zero interval":     func(c Config) Config { c.SweeperInterval = 0; return c },
		"zero batch":        func(c Config) Config { c.NotificationBatch = 0; return c },
		"unknown log level": func(c Config) Config { c.LogLevel = "loud"; return c },
		"half bootstrap":    func(c Config) Config { c.BootstrapAdminEmail = "ops@example.test"; return c },
	}
	for name, mutate := range cases {
		if err := mutate(valid).Validate(); err == nil {
			t.Fatalf("expected %s to be refused", name)
		}
	}
}

// TestRedactedNeverExposesCredentials verifies the log rendering of the config.
func TestRedactedNeverExposesCredentials(t *testing.T) {
	cfg := Config{
		Environment: "staging", HTTPAddress: ":8080", DatabasePath: "data/db.sqlite",
		DatabaseMaxOpenConns: 4, SessionTTL: time.Hour, IdempotencyTTL: time.Hour,
		RequestTimeout: time.Second, ShutdownTimeout: time.Second,
		NotificationInterval: time.Second, NotificationBatch: 1,
		SweeperInterval: time.Second, SweeperBatch: 1, LogLevel: "info",
		BootstrapAdminEmail:    "ops@example.test",
		BootstrapAdminPassword: "superSecret2026",
	}
	rendered := cfg.Redacted()
	if strings.Contains(rendered, "superSecret2026") {
		t.Fatalf("the redacted configuration leaked the password: %s", rendered)
	}
	if strings.Contains(rendered, "ops@example.test") {
		t.Fatalf("the redacted configuration leaked the operations address: %s", rendered)
	}
	if !strings.Contains(rendered, "bootstrap_admin=enabled") {
		t.Fatalf("expected the bootstrap state to be reported: %s", rendered)
	}
	cfg.BootstrapAdminEmail = ""
	cfg.BootstrapAdminPassword = ""
	if !strings.Contains(cfg.Redacted(), "bootstrap_admin=disabled") {
		t.Fatalf("expected the disabled bootstrap to be reported: %s", cfg.Redacted())
	}
}
