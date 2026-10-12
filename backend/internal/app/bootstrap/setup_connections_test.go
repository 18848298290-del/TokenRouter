package bootstrap

import (
	"strings"
	"testing"
)

// TestBuildDatabaseConnectionDSNsUsesPostgresForBootstrap checks that creation uses the postgres maintenance
// database.
func TestBuildDatabaseConnectionDSNsUsesPostgresForBootstrap(t *testing.T) {
	cfg := &SetupDatabaseConfig{
		Host:     "db",
		Port:     5432,
		User:     "tokenrouter",
		Password: "secret",
		DBName:   "tokenrouter",
		SSLMode:  "disable",
	}

	bootstrapDSN, targetDSN := BuildDatabaseConnectionDSNs(cfg)

	if !strings.Contains(bootstrapDSN, "dbname=postgres") {
		t.Fatalf("bootstrap DSN = %q, want default postgres database", bootstrapDSN)
	}
	if strings.Contains(bootstrapDSN, "dbname=tokenrouter") {
		t.Fatalf("bootstrap DSN = %q, should not connect to target database before checking/creating it", bootstrapDSN)
	}
	if !strings.Contains(targetDSN, "dbname=tokenrouter") {
		t.Fatalf("target DSN = %q, want configured database", targetDSN)
	}
}
