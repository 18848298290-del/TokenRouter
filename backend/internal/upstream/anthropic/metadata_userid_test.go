package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseMetadataUserID_LegacyFormat_WithoutAccountUUID parses a legacy
// identity with an empty account component. Device and session remain in their
// own fields, account stays empty, and the result records the legacy format.
func TestParseMetadataUserID_LegacyFormat_WithoutAccountUUID(t *testing.T) {
	raw := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_123e4567-e89b-12d3-a456-426614174000"
	parsed := ParseMetadataUserID(raw)
	require.NotNil(t, parsed)
	require.Equal(t, "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", parsed.DeviceID)
	require.Equal(t, "", parsed.AccountUUID)
	require.Equal(t, "123e4567-e89b-12d3-a456-426614174000", parsed.SessionID)
	require.False(t, parsed.IsNewFormat)
}

// TestParseMetadataUserID_LegacyFormat_WithAccountUUID parses a legacy identity
// carrying device, account, and session components. Each supplied value appears
// in its matching field and the result records legacy encoding.
func TestParseMetadataUserID_LegacyFormat_WithAccountUUID(t *testing.T) {
	raw := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account_550e8400-e29b-41d4-a716-446655440000_session_123e4567-e89b-12d3-a456-426614174000"
	parsed := ParseMetadataUserID(raw)
	require.NotNil(t, parsed)
	require.Equal(t, "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", parsed.DeviceID)
	require.Equal(t, "550e8400-e29b-41d4-a716-446655440000", parsed.AccountUUID)
	require.Equal(t, "123e4567-e89b-12d3-a456-426614174000", parsed.SessionID)
	require.False(t, parsed.IsNewFormat)
}

// TestParseMetadataUserID_JSONFormat_WithoutAccountUUID parses JSON-formatted
// identity with an empty account string. Device and session remain intact,
// account stays empty, and IsNewFormat identifies the JSON representation.
func TestParseMetadataUserID_JSONFormat_WithoutAccountUUID(t *testing.T) {
	raw := `{"device_id":"d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677","account_uuid":"","session_id":"c72554f2-1234-5678-abcd-123456789abc"}`
	parsed := ParseMetadataUserID(raw)
	require.NotNil(t, parsed)
	require.Equal(t, "d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677", parsed.DeviceID)
	require.Equal(t, "", parsed.AccountUUID)
	require.Equal(t, "c72554f2-1234-5678-abcd-123456789abc", parsed.SessionID)
	require.True(t, parsed.IsNewFormat)
}

// TestParseMetadataUserID_JSONFormat_WithAccountUUID parses a JSON-formatted
// identity carrying all three components. The result retains their supplied
// values and marks the representation as the newer JSON encoding.
func TestParseMetadataUserID_JSONFormat_WithAccountUUID(t *testing.T) {
	raw := `{"device_id":"d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677","account_uuid":"550e8400-e29b-41d4-a716-446655440000","session_id":"c72554f2-1234-5678-abcd-123456789abc"}`
	parsed := ParseMetadataUserID(raw)
	require.NotNil(t, parsed)
	require.Equal(t, "d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677", parsed.DeviceID)
	require.Equal(t, "550e8400-e29b-41d4-a716-446655440000", parsed.AccountUUID)
	require.Equal(t, "c72554f2-1234-5678-abcd-123456789abc", parsed.SessionID)
	require.True(t, parsed.IsNewFormat)
}

// TestParseMetadataUserID_InvalidInputs supplies absent, malformed, incomplete,
// and invalid legacy identity strings. Parsing returns nil for every listed
// case, so callers do not receive an apparently usable incomplete identity.
func TestParseMetadataUserID_InvalidInputs(t *testing.T) {
	tests := []struct {
		name string // case display name
		raw  string // raw parser input
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"random text", "not-a-valid-user-id"},
		{"partial legacy format", "session_123e4567-e89b-12d3-a456-426614174000"},
		{"invalid JSON", `{"device_id":}`},
		{"JSON missing device_id", `{"account_uuid":"","session_id":"c72554f2-1234-5678-abcd-123456789abc"}`},
		{"JSON missing session_id", `{"device_id":"d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677","account_uuid":""}`},
		{"JSON empty device_id", `{"device_id":"","account_uuid":"","session_id":"c72554f2-1234-5678-abcd-123456789abc"}`},
		{"JSON empty session_id", `{"device_id":"d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677","account_uuid":"","session_id":""}`},
		{"legacy format short hex", "user_a1b2c3d4_account__session_123e4567-e89b-12d3-a456-426614174000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Nil(t, ParseMetadataUserID(tt.raw), "should return nil for: %s", tt.raw)
		})
	}
}

// TestParseMetadataUserID_HexCaseInsensitive parses a legacy device string
// containing uppercase hexadecimal letters. The result is accepted and retains
// that exact casing, keeping matching case tolerance separate from output
// normalization.
func TestParseMetadataUserID_HexCaseInsensitive(t *testing.T) {
	// Legacy format should accept both upper and lower case hex
	rawUpper := "user_A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2_account__session_123e4567-e89b-12d3-a456-426614174000"
	parsed := ParseMetadataUserID(rawUpper)
	require.NotNil(t, parsed, "legacy format should accept uppercase hex")
	require.Equal(t, "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2", parsed.DeviceID)
}

// TestFormatMetadataUserID_LegacyVersion formats supplied components under a
// CLI version below the threshold. The exact legacy delimiters and field order
// remain visible in the resulting identity string.
func TestFormatMetadataUserID_LegacyVersion(t *testing.T) {
	result := FormatMetadataUserID("deadbeef"+"00112233445566778899aabbccddeeff0011223344556677", "acc-uuid", "sess-uuid", "2.1.77")
	require.Equal(t, "user_deadbeef00112233445566778899aabbccddeeff0011223344556677_account_acc-uuid_session_sess-uuid", result)
}

// TestFormatMetadataUserID_NewVersion formats supplied components at the JSON
// threshold version. The resulting text is the expected JSON object with
// device, account, and session values in their corresponding fields.
func TestFormatMetadataUserID_NewVersion(t *testing.T) {
	result := FormatMetadataUserID("deadbeef"+"00112233445566778899aabbccddeeff0011223344556677", "acc-uuid", "sess-uuid", "2.1.78")
	require.Equal(t, `{"device_id":"deadbeef00112233445566778899aabbccddeeff0011223344556677","account_uuid":"acc-uuid","session_id":"sess-uuid"}`, result)
}

// TestFormatMetadataUserID_EmptyVersion_Legacy formats a device and session
// with an empty version and empty account. The result uses legacy encoding and
// retains the account delimiter position.
func TestFormatMetadataUserID_EmptyVersion_Legacy(t *testing.T) {
	result := FormatMetadataUserID("deadbeef"+"00112233445566778899aabbccddeeff0011223344556677", "", "sess-uuid", "")
	require.Equal(t, "user_deadbeef00112233445566778899aabbccddeeff0011223344556677_account__session_sess-uuid", result)
}

// TestFormatMetadataUserID_EmptyAccountUUID formats empty-account identity
// under both legacy and threshold versions. The legacy form retains its empty
// delimiter slot, while JSON carries an empty account string.
func TestFormatMetadataUserID_EmptyAccountUUID(t *testing.T) {
	// Legacy format with empty provider UUID → double underscore
	result := FormatMetadataUserID("deadbeef"+"00112233445566778899aabbccddeeff0011223344556677", "", "sess-uuid", "2.1.22")
	require.Contains(t, result, "_account__session_")

	// New format with empty provider UUID → empty string in JSON
	result = FormatMetadataUserID("deadbeef"+"00112233445566778899aabbccddeeff0011223344556677", "", "sess-uuid", "2.1.78")
	require.Contains(t, result, `"account_uuid":""`)
}

// TestIsNewMetadataFormatVersion compares empty, older, threshold, and newer
// CLI versions with the format threshold. Older or absent values select legacy,
// while the threshold and newer versions select JSON.
func TestIsNewMetadataFormatVersion(t *testing.T) {
	tests := []struct {
		version string // candidate CLI version
		want    bool   // expected comparison value
	}{
		{"", false},
		{"2.1.77", false},
		{"2.1.78", true},
		{"2.1.79", true},
		{"2.2.0", true},
		{"3.0.0", true},
		{"2.0.100", false},
		{"1.9.99", false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			require.Equal(t, tt.want, IsNewMetadataFormatVersion(tt.version))
		})
	}
}

// TestParseFormat_RoundTrip_Legacy formats valid legacy components and parses
// the returned text. Device, account, and session survive in their respective
// fields, and parsing reports the legacy format.
func TestParseFormat_RoundTrip_Legacy(t *testing.T) {
	deviceID := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	providerUUID := "550e8400-e29b-41d4-a716-446655440000"
	sessionID := "123e4567-e89b-12d3-a456-426614174000"

	formatted := FormatMetadataUserID(deviceID, providerUUID, sessionID, "2.1.22")
	parsed := ParseMetadataUserID(formatted)
	require.NotNil(t, parsed)
	require.Equal(t, deviceID, parsed.DeviceID)
	require.Equal(t, providerUUID, parsed.AccountUUID)
	require.Equal(t, sessionID, parsed.SessionID)
	require.False(t, parsed.IsNewFormat)
}

// TestParseFormat_RoundTrip_JSON formats components using the threshold version
// and parses the result. All three values survive in their matching fields, and
// parsing reports JSON encoding.
func TestParseFormat_RoundTrip_JSON(t *testing.T) {
	deviceID := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	providerUUID := "550e8400-e29b-41d4-a716-446655440000"
	sessionID := "123e4567-e89b-12d3-a456-426614174000"

	formatted := FormatMetadataUserID(deviceID, providerUUID, sessionID, "2.1.78")
	parsed := ParseMetadataUserID(formatted)
	require.NotNil(t, parsed)
	require.Equal(t, deviceID, parsed.DeviceID)
	require.Equal(t, providerUUID, parsed.AccountUUID)
	require.Equal(t, sessionID, parsed.SessionID)
	require.True(t, parsed.IsNewFormat)
}

// TestParseFormat_RoundTrip_EmptyAccountUUID round-trips an empty account
// through both supported identity representations. Device and session remain
// intact and the empty account stays distinct from either required component.
func TestParseFormat_RoundTrip_EmptyAccountUUID(t *testing.T) {
	deviceID := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	sessionID := "123e4567-e89b-12d3-a456-426614174000"

	// Legacy round-trip with empty provider UUID
	formatted := FormatMetadataUserID(deviceID, "", sessionID, "2.1.22")
	parsed := ParseMetadataUserID(formatted)
	require.NotNil(t, parsed)
	require.Equal(t, deviceID, parsed.DeviceID)
	require.Equal(t, "", parsed.AccountUUID)
	require.Equal(t, sessionID, parsed.SessionID)

	// JSON round-trip with empty provider UUID
	formatted = FormatMetadataUserID(deviceID, "", sessionID, "2.1.78")
	parsed = ParseMetadataUserID(formatted)
	require.NotNil(t, parsed)
	require.Equal(t, deviceID, parsed.DeviceID)
	require.Equal(t, "", parsed.AccountUUID)
	require.Equal(t, sessionID, parsed.SessionID)
}
