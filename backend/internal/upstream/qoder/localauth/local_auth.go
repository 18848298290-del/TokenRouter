package localauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// DefaultAuthDir returns the local Qoder authentication directory, or empty if the home directory is unavailable.
func DefaultAuthDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".qoder", ".auth")
}

// ReadLocalAuth reads and decrypts local credentials, using DefaultAuthDir when authDir is empty.
func ReadLocalAuth(authDir string) (*qoder.AuthInfo, error) {
	if authDir == "" {
		authDir = DefaultAuthDir()
	}

	machineID, err := os.ReadFile(filepath.Join(authDir, "machine_id"))
	if err != nil {
		return nil, fmt.Errorf("qoder: read machine_id: %w", err)
	}
	machineID = []byte(string(machineID))[:len(machineID)]
	// Strip trailing CR and LF bytes from the machine ID.
	for len(machineID) > 0 && (machineID[len(machineID)-1] == '\n' || machineID[len(machineID)-1] == '\r') {
		machineID = machineID[:len(machineID)-1]
	}

	userB64, err := os.ReadFile(filepath.Join(authDir, "user"))
	if err != nil {
		return nil, fmt.Errorf("qoder: read user: %w", err)
	}
	// Strip trailing CR and LF bytes before base64 decoding.
	for len(userB64) > 0 && (userB64[len(userB64)-1] == '\n' || userB64[len(userB64)-1] == '\r') {
		userB64 = userB64[:len(userB64)-1]
	}

	ciphertext, err := base64.StdEncoding.DecodeString(string(userB64))
	if err != nil {
		return nil, fmt.Errorf("qoder: decode user base64: %w", err)
	}

	// Use the first 16 machine ID bytes, zero-padding a shorter ID.
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
