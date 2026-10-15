package anthropic

import (
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// metadata_userid.go formats Claude metadata identities according to the
// selected CLI version.
// Claude metadata.user_id holds device, account, and session identifiers in
// either a delimited string or a JSON object encoded as text. Outbound
// formatting selects the shape from the client version so the same components
// can accompany different generations of CLI requests.
//
// The formatter trusts supplied components, including an empty account
// identifier. Incoming parsing delegates to protocol/anthropic, where JSON
// requires nonempty device and session text and the legacy representation
// applies character and length checks. Version comparison for choosing the
// output shape is permissive and differs from strict deployment override
// validation. Callers needing a parseable round trip provide components that
// satisfy the selected representation.

// NewMetadataFormatMinVersion sets the output encoding threshold used when
// writing device, account, and session identity into metadata.user_id.
const NewMetadataFormatMinVersion = "2.1.78"

// Protocol aliases keep incoming identity parsing and outgoing JSON metadata
// encoding on the same device, account, and session representation.
type (
	// ParsedUserID aliases the protocol parser's device, account, and session
	// result.
	ParsedUserID = wire.ParsedMetadataUserID
	// jsonUserID aliases the object encoded inside JSON-formatted metadata.user_id
	// text.
	jsonUserID = wire.MetadataUserID
)

// FormatMetadataUserID selects the encoded identity representation from the
// supplied CLI version. Versions at or above the metadata threshold receive a
// JSON object encoded as text; lower or empty versions receive the legacy
// delimited form.
//
// Device, account, and session occupy their corresponding fields, with an
// empty account supported in both forms. Formatting performs no component
// validation. JSON callers needing matching parsed components supply valid
// UTF-8 text and nonempty device and session values; legacy callers satisfy the
// parser's character and length checks. Output selection uses permissive
// component comparison, while JSON string escaping follows encoding/json.
func FormatMetadataUserID(deviceID, providerUUID, sessionID, uaVersion string) string {
	// Encoding choice follows CLI version so the same three identity components
	// can be written in the format that request fingerprint consumers select for
	// that client.
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

// IsNewMetadataFormatVersion compares version with the JSON metadata threshold,
// returning false for empty input. Other inputs use clientmeta's permissive
// component comparison, rather than the strict deployment override validator.
func IsNewMetadataFormatVersion(version string) bool {
	if version == "" {
		return false
	}
	return clientmeta.CompareVersions(version, NewMetadataFormatMinVersion) >= 0
}

// ExtractCLIVersion returns release components from a leading case-insensitive
// Claude CLI User-Agent. A trailing suffix can remain after the matched
// components, and unmatched input returns an empty string.
func ExtractCLIVersion(ua string) string { return clientmeta.ExtractClaudeCLIVersion(ua) }

// ParseMetadataUserID delegates incoming identity parsing to the protocol
// representation. It trims outer whitespace and returns nil when the selected
// JSON or legacy form lacks the parser's required components.
func ParseMetadataUserID(raw string) *ParsedUserID { return wire.ParseMetadataUserID(raw) }
