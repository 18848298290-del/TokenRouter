package anthropic

import (
	"encoding/json"
	"regexp"
	"strings"
)

// metadata_user_id.go extracts device, account, and session identities from
// Claude metadata user IDs.
// Claude metadata carries device, account, and conversation identifiers in
// either a delimited string or a JSON object encoded inside user_id. Session
// hashing and outgoing fingerprint rewriting need the same components from
// both forms, including the distinction between an omitted account identifier
// and a missing conversation identifier.
//
// Legacy strings constrain the device to hexadecimal and the session to a
// fixed-length hexadecimal-and-hyphen sequence. JSON objects accept arbitrary
// nonempty device and session strings. Parsing records the supplied format so
// later writers can choose the corresponding representation without deriving
// an account identity from the conversation identifier.

// legacyMetadataUserIDRegex captures device, optional account, and session
// identifiers from the anchored legacy form used by client metadata parsers:
//
//	user_{64hex}_account_{optional_hex_or_hyphens}_session_{36hex_or_hyphens}
var legacyMetadataUserIDRegex = regexp.MustCompile(`^user_([a-fA-F0-9]{64})_account_([a-fA-F0-9-]*)_session_([a-fA-F0-9-]{36})$`)

// ParsedMetadataUserID represents the components extracted from a metadata.user_id value.
type ParsedMetadataUserID struct {
	// DeviceID and SessionID are required by ParseMetadataUserID, while
	// AccountUUID may be empty. The parser retains their supplied casing and
	// leaves JSON identifiers free of the legacy format's character restrictions.
	DeviceID    string // originating client identifier
	AccountUUID string // optional account identifier
	SessionID   string // client conversation identifier
	IsNewFormat bool   // parsed from JSON
}

// MetadataUserID is the JSON object encoded inside Claude's metadata.user_id.
type MetadataUserID struct {
	// DeviceID and SessionID must be nonempty for ParseMetadataUserID to accept
	// the object. AccountUUID can be empty, and the JSON parser retains all three
	// strings without validating their UUID shape.
	DeviceID    string `json:"device_id"`    // originating client identifier
	AccountUUID string `json:"account_uuid"` // optional account identifier
	SessionID   string `json:"session_id"`   // client conversation identifier
}

// ParseMetadataUserID extracts Claude metadata identifiers from JSON or the
// legacy delimited form after trimming outer whitespace. It returns nil when
// parsing fails or required device and session identifiers are absent.
//
// A leading brace selects JSON parsing, where identifiers need only be
// nonempty; the legacy expression applies its own character and length checks.
// AccountUUID can be empty in either form. IsNewFormat records which format
// succeeded so consumers can retain the client's encoding choice.
func ParseMetadataUserID(raw string) *ParsedMetadataUserID {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	// A leading brace selects the object representation, whose identifiers are
	// retained verbatim for consumers that accept non-UUID conversation names.
	if raw[0] == '{' {
		var j MetadataUserID
		if err := json.Unmarshal([]byte(raw), &j); err != nil {
			return nil
		}
		if j.DeviceID == "" || j.SessionID == "" {
			return nil
		}
		return &ParsedMetadataUserID{
			DeviceID:    j.DeviceID,
			AccountUUID: j.AccountUUID,
			SessionID:   j.SessionID,
			IsNewFormat: true,
		}
	}

	// The delimited representation validates each capture separately so account
	// omission cannot shift a conversation identifier into the account position.
	matches := legacyMetadataUserIDRegex.FindStringSubmatch(raw)
	if matches == nil {
		return nil
	}
	return &ParsedMetadataUserID{
		DeviceID:    matches[1],
		AccountUUID: matches[2],
		SessionID:   matches[3],
		IsNewFormat: false,
	}
}
