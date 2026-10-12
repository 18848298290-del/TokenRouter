package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// TestDecideAdminBootstrap supplies empty, administrator-containing, and
// user-only database counts to the identity decision helper. Only the empty
// case must permit creation, with each case returning its expected reason.
func TestDecideAdminBootstrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string // subtest display name
		totalUsers int64  // total stored user count
		adminUsers int64  // stored administrator user count
		should     bool   // expected creation admission flag
		reason     string // expected bootstrap decision reason
	}{
		{
			name:       "empty database should create admin",
			totalUsers: 0,
			adminUsers: 0,
			should:     true,
			reason:     "empty_database",
		},
		{
			name:       "admin exists should skip",
			totalUsers: 10,
			adminUsers: 1,
			should:     false,
			reason:     adminBootstrapReasonAdminExists,
		},
		{
			name:       "users exist without admin should skip",
			totalUsers: 5,
			adminUsers: 0,
			should:     false,
			reason:     adminBootstrapReasonUsersExistWithoutAdmin,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			shouldCreate, reason := identity.DecideAdminBootstrap(tc.totalUsers, tc.adminUsers)
			if shouldCreate != tc.should {
				t.Fatalf("shouldCreate=%v, want %v", shouldCreate, tc.should)
			}
			if reason != tc.reason {
				t.Fatalf("reason=%q, want %q", reason, tc.reason)
			}
		})
	}
}

// TestSetupDefaultAdminConcurrency supplies both retired RUN_MODE values before
// reading the initial administrator limit. Neither value must change the fixed
// default used by setup administrator creation.
func TestSetupDefaultAdminConcurrency(t *testing.T) {
	t.Run("旧配置不改变初始并发", func(t *testing.T) {
		t.Setenv("RUN_MODE", "simple")
		if got := setupDefaultAdminConcurrency(); got != defaultUserConcurrency {
			t.Fatalf("setupDefaultAdminConcurrency()=%d, want %d", got, defaultUserConcurrency)
		}
	})

	t.Run("初始管理员使用默认并发", func(t *testing.T) {
		t.Setenv("RUN_MODE", "standard")
		if got := setupDefaultAdminConcurrency(); got != defaultUserConcurrency {
			t.Fatalf("setupDefaultAdminConcurrency()=%d, want %d", got, defaultUserConcurrency)
		}
	})
}

// TestNeedsSetupSkipsWhenSkipSetupIsEnabled supplies the supported skip values
// including trimmed and mixed-case text. Setup must remain closed in each empty
// temporary data directory when the deployment skip switch is enabled.
func TestNeedsSetupSkipsWhenSkipSetupIsEnabled(t *testing.T) {
	tests := []struct {
		name  string // subtest display name
		value string // supplied skip environment text
	}{
		{name: "true", value: "true"},
		{name: "one", value: "1"},
		{name: "yes", value: "yes"},
		{name: "trimmed mixed case true", value: "  TrUe  "},
		{name: "trimmed mixed case yes", value: "  YeS  "},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATA_DIR", t.TempDir())
			t.Setenv("SKIP_SETUP", tc.value)

			if NeedsSetup() {
				t.Fatalf("NeedsSetup() = true, want false when SKIP_SETUP is enabled")
			}
		})
	}
}

// TestNeedsSetupFallsBackToFileDetectionWhenSkipSetupIsDisabled combines unset,
// false, and invalid skip values with individual installation files. Empty
// directories must permit setup, and either persisted evidence file must close
// the gate.
func TestNeedsSetupFallsBackToFileDetectionWhenSkipSetupIsDisabled(t *testing.T) {
	tests := []struct {
		name         string // subtest display name
		skipSetupSet bool   // skip variable presence flag
		skipSetup    string // supplied skip environment text
		markerFile   string // created installation evidence filename
		want         bool   // expected setup admission result
	}{
		{
			name: "unset without installation files",
			want: true,
		},
		{
			name:         "false without installation files",
			skipSetupSet: true,
			skipSetup:    " false ",
			want:         true,
		},
		{
			name:         "invalid value without installation files",
			skipSetupSet: true,
			skipSetup:    "enabled",
			want:         true,
		},
		{
			name:         "config file exists",
			skipSetupSet: true,
			skipSetup:    "false",
			markerFile:   ConfigFileName,
			want:         false,
		},
		{
			name:         "install lock file exists",
			skipSetupSet: true,
			skipSetup:    "invalid",
			markerFile:   InstallLockFile,
			want:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			t.Setenv("DATA_DIR", dataDir)
			if tc.skipSetupSet {
				t.Setenv("SKIP_SETUP", tc.skipSetup)
			} else {
				// Model true absence while retaining the process's prior environment value.
				// Cleanup attempts to restore that presence state and discards restoration errors
				// after the serial setup-gate assertion completes.
				originalValue, wasSet := os.LookupEnv("SKIP_SETUP")
				if err := os.Unsetenv("SKIP_SETUP"); err != nil {
					t.Fatalf("Unsetenv(SKIP_SETUP) error = %v", err)
				}
				t.Cleanup(func() {
					if wasSet {
						_ = os.Setenv("SKIP_SETUP", originalValue)
						return
					}
					_ = os.Unsetenv("SKIP_SETUP")
				})
			}

			if tc.markerFile != "" {
				if err := os.WriteFile(filepath.Join(dataDir, tc.markerFile), nil, 0o600); err != nil {
					t.Fatalf("WriteFile(%s) error = %v", tc.markerFile, err)
				}
			}

			if got := NeedsSetup(); got != tc.want {
				t.Fatalf("NeedsSetup() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSetupMigrationTimeout supplies nil, zero, negative, and positive
// migration-budget configurations. The first three cases must use the default
// duration, while positive seconds must become the configured timeout.
func TestSetupMigrationTimeout(t *testing.T) {
	tests := []struct {
		name string        // subtest display name
		cfg  *SetupConfig  // supplied migration timeout configuration
		want time.Duration // expected migration budget duration
	}{
		{name: "nil config uses default", cfg: nil, want: defaultMigrationTimeout},
		{name: "zero uses default", cfg: &SetupConfig{}, want: defaultMigrationTimeout},
		{name: "negative uses default", cfg: &SetupConfig{MigrationTimeoutSeconds: -1}, want: defaultMigrationTimeout},
		{name: "positive uses configured value", cfg: &SetupConfig{MigrationTimeoutSeconds: 300}, want: 300 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.migrationTimeout(); got != tt.want {
				t.Fatalf("migrationTimeout()=%s, want %s", got, tt.want)
			}
		})
	}
}

// TestWriteConfigFileKeepsDefaultUserConcurrency writes setup YAML while the
// retired simple run mode is set. Reading the file must show the fixed
// five-request user concurrency default.
func TestWriteConfigFileKeepsDefaultUserConcurrency(t *testing.T) {
	t.Setenv("RUN_MODE", "simple")
	t.Setenv("DATA_DIR", t.TempDir())

	if err := writeConfigFile(&SetupConfig{}); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}

	data, err := os.ReadFile(GetConfigFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !strings.Contains(string(data), "user_concurrency: 5") {
		t.Fatalf("config missing default user concurrency, got:\n%s", string(data))
	}
}

// TestWriteConfigFileIncludesRedisUsername writes configuration with a Redis
// authentication username in a temporary directory. The emitted YAML must
// contain that username for normal startup configuration loading.
func TestWriteConfigFileIncludesRedisUsername(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())

	if err := writeConfigFile(&SetupConfig{
		Redis: RedisConfig{
			Host:     "redis",
			Port:     6379,
			Username: "app-user",
		},
	}); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}

	data, err := os.ReadFile(GetConfigFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !strings.Contains(string(data), "username: app-user") {
		t.Fatalf("config missing Redis username, got:\n%s", string(data))
	}
}
