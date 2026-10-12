package anthropic

import (
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// NewMetadataFormatMinVersion is the minimum Claude Code version that uses
// JSON-formatted metadata.user_id instead of the legacy concatenated string.
const NewMetadataFormatMinVersion = "2.1.78"

// These aliases share metadata representations and parsing with the protocol package.
type (
	ParsedUserID = wire.ParsedMetadataUserID
	jsonUserID   = wire.MetadataUserID
)

// FormatMetadataUserID encodes the supplied identity components for the CLI version.
func FormatMetadataUserID(deviceID, providerUUID, sessionID, uaVersion string) string {
	if IsNewMetadataFormatVersion(uaVersion) {
		b, _ := json.Marshal(jsonUserID{
			DeviceID:    deviceID,
			AccountUUID: providerUUID,
			SessionID:   sessionID,
		})
		return string(b)
	}
	// Legacy format
	return "user_" + deviceID + "_account_" + providerUUID + "_session_" + sessionID
}

// IsNewMetadataFormatVersion reports whether the version uses JSON metadata.user_id.
func IsNewMetadataFormatVersion(version string) bool {
	if version == "" {
		return false
	}
	return clientmeta.CompareVersions(version, NewMetadataFormatMinVersion) >= 0
}

// ExtractCLIVersion extracts the Claude Code version from a User-Agent string.
// Returns "" if the UA doesn't match the expected pattern.
func ExtractCLIVersion(ua string) string { return clientmeta.ExtractClaudeCLIVersion(ua) }

func ParseMetadataUserID(raw string) *ParsedUserID { return wire.ParseMetadataUserID(raw) }
