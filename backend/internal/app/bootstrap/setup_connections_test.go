package bootstrap

import (
	"strings"
	"testing"
)

// Database creation requires a maintenance connection before the target
// exists. This fixture gives setup a distinct target name and checks the two
// DSNs, keeping maintenance access on postgres and the later verification
// connection on the requested database.

// TestBuildDatabaseConnectionDSNsUsesPostgresForBootstrap supplies a target
// database name with shared credentials. The maintenance DSN must name
// postgres, exclude the target name, and leave the target DSN pointing at the
// configured database.
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
