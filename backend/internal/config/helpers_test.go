package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// resetViperWithJWTSecret clears process Viper state and the selected
// configuration-path variables, then supplies a valid test JWT secret. Test
// cleanup resets Viper again and restores environment writes through t.Setenv;
// callers keep these fixtures serial.
func resetViperWithJWTSecret(t *testing.T) {
	t.Helper()
	// Each fixture shares the process Viper singleton. Reset before loading and
	// again at cleanup so serial tests do not inherit a preceding fixture's merged configuration.
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("CONFIG_FILE", "")
	t.Setenv("DATA_DIR", "")
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
}

// prepareLegacyConfigTest selects a temporary YAML file after resetting the
// process configuration fixture. It assigns empty text to both environment
// names for every known alias and the isolation key, leaving normal Viper
// empty-environment handling to the loader. The returned path lets tests
// inspect the unchanged mounted input.
func prepareLegacyConfigTest(t *testing.T, body string) string {
	t.Helper()
	resetViperWithJWTSecret(t)
	for _, item := range legacyConfigKeys {
		for _, key := range []string{item.oldKey, item.newKey} {
			t.Setenv(strings.ToUpper(strings.ReplaceAll(key, ".", "_")), "")
		}
	}
	t.Setenv("GATEWAY_CONNECTION_POOL_ISOLATION", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	t.Setenv("CONFIG_FILE", path)
	return path
}
