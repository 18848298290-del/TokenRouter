package localauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// Local Qoder import reads an encrypted user record beside the machine ID that
// supplies its AES key. Trailing CR and LF bytes are removed from both files
// before key derivation or base64 decoding, so storage line endings do not
// become part of the encrypted representation.
//
// The decryption key uses the first sixteen machine-ID bytes, padded with zero
// bytes when shorter. The full trimmed machine ID is then attached to decoded
// AuthInfo for request headers, retaining the identifier beyond the key prefix.
// Read, decode, padding, and JSON failures return to the manual importer. The
// normal service uses provider credentials rather than invoking this local-file
// path.

// DefaultAuthDir returns the user's .qoder/.auth directory for manual credential
// import. Home-directory lookup failure returns empty text; ReadLocalAuth uses
// this result when no explicit authentication directory is supplied.
func DefaultAuthDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".qoder", ".auth")
}

// ReadLocalAuth reads and decrypts one local user record with its machine
// identity. An empty directory selects DefaultAuthDir, and filesystem, base64,
// decryption, or JSON failures return annotated errors to the importer.
//
// The key uses the first sixteen machine-ID bytes, with zero padding for shorter
// IDs. CR and LF endings are removed before key derivation and base64 parsing,
// while the full trimmed machine ID is retained in AuthInfo for later request
// headers.
func ReadLocalAuth(authDir string) (*qoder.AuthInfo, error) {
	if authDir == "" {
		authDir = DefaultAuthDir()
	}

	machineID, err := os.ReadFile(filepath.Join(authDir, "machine_id"))
	if err != nil {
		return nil, fmt.Errorf("qoder: read machine_id: %w", err)
	}
	machineID = []byte(string(machineID))[:len(machineID)]
	// The machine file may end with CR or LF. Remove those bytes before deriving
	// the AES key so line endings cannot alter the decryption identity.
	for len(machineID) > 0 && (machineID[len(machineID)-1] == '\n' || machineID[len(machineID)-1] == '\r') {
		machineID = machineID[:len(machineID)-1]
	}

	userB64, err := os.ReadFile(filepath.Join(authDir, "user"))
	if err != nil {
		return nil, fmt.Errorf("qoder: read user: %w", err)
	}
	// The user file can carry terminal line endings. Remove them before base64
	// decoding so storage formatting does not become part of the encrypted record.
	for len(userB64) > 0 && (userB64[len(userB64)-1] == '\n' || userB64[len(userB64)-1] == '\r') {
		userB64 = userB64[:len(userB64)-1]
	}

	ciphertext, err := base64.StdEncoding.DecodeString(string(userB64))
	if err != nil {
		return nil, fmt.Errorf("qoder: decode user base64: %w", err)
	}

	// Local encryption derives its sixteen-byte key from the machine ID prefix.
	// Copy into zeroed storage so shorter IDs retain the same zero-padding
	// convention.
	key := make([]byte, 16)
	copy(key, machineID)

	plaintext, err := qoder.AESDecrypt(ciphertext, key)
	if err != nil {
		return nil, fmt.Errorf("qoder: decrypt user: %w", err)
	}

	var info qoder.AuthInfo
	if err := json.Unmarshal(plaintext, &info); err != nil {
		return nil, fmt.Errorf("qoder: parse user JSON: %w", err)
	}
	info.MachineID = string(machineID)

	return &info, nil
}
