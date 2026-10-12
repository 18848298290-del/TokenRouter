package localauth

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadLocalAuthFromDisk reads local credentials when a local-auth or real-API test flag is enabled.
func TestReadLocalAuthFromDisk(t *testing.T) {
	if os.Getenv("QODER_RUN_LOCAL_AUTH_TESTS") != "1" && os.Getenv("QODER_RUN_REAL_API_TESTS") != "1" {
		t.Skip("set QODER_RUN_LOCAL_AUTH_TESTS=1 to run local Qoder auth import test")
	}
	authDir := DefaultAuthDir()
	if authDir == "" {
		t.Skip("no home directory")
	}
	if _, err := os.Stat(filepath.Join(authDir, "machine_id")); os.IsNotExist(err) {
		t.Skip("local Qoder auth not found")
	}

	info, err := ReadLocalAuth(authDir)
	if err != nil {
		t.Fatalf("ReadLocalAuth: %v", err)
	}

	if info.UID == "" {
		t.Error("expected non-empty UID")
	}
	if info.AccessToken == "" && info.SecurityOauthToken == "" {
		t.Error("expected non-empty token")
	}
	t.Log("Loaded local Qoder auth metadata")
}
