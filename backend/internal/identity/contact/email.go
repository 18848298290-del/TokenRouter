package contact

import (
	"encoding/json"
	"strings"
)

// email.go decodes notification recipients from current records or legacy
// address lists.
// Notification recipients are stored as JSON, with both legacy address arrays
// and records carrying verification and delivery flags still accepted. Object
// records keep their supplied addresses and flags. Legacy strings are trimmed and
// converted into enabled, unverified records, so callers can apply the same
// recipient-selection rules after either representation is read.
//
// Empty input and undecodable representations yield no recipients. MarshalNotifyEmails
// writes records, including their flags, and represents an empty collection as an
// empty array. Consumers such as balance notifications decide whether a parsed
// record is eligible for delivery; parsing itself performs no address validation.

// Entry holds a saved notification address and its delivery eligibility flags.
type Entry struct {
	Email    string `json:"email"`    // supplied notification email address
	Disabled bool   `json:"disabled"` // recipient disabled by user
	Verified bool   `json:"verified"` // address verification recorded by caller
}

// ParseNotifyEmails converts saved JSON into notification recipient records.
// Object arrays retain addresses and flags as decoded, while legacy string arrays
// trim addresses, omit empty strings, and initialize both flags to false.
//
// The object representation is attempted first so saved verification decisions
// survive reads. The legacy check examines the first raw element, and the string
// decoder still needs to accept the complete array. Empty input returns nil; JSON
// null reaches the legacy decoder and produces an empty allocated result. Invalid
// representations return nil.
func ParseNotifyEmails(raw string) []Entry {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}

	// Decode records first so successful reads retain Disabled and Verified, which
	// the legacy address representation cannot express for recipient selection.
	var entries []Entry
	if err := json.Unmarshal([]byte(raw), &entries); err == nil && len(entries) > 0 {
		// Inspect the first raw element before returning records because historical
		// address arrays require trimming and false flags during their conversion.
		if !isOldStringArrayFormat(raw) {
			return entries
		}
	}

	// When record decoding is unsuitable, accept the historical address list so
	// saved recipients remain readable before their next write uses record JSON.
	var emails []string
	if err := json.Unmarshal([]byte(raw), &emails); err == nil {
		result := make([]Entry, 0, len(emails))
		for _, e := range emails {
			e = strings.TrimSpace(e)
			if e != "" {
				result = append(result, Entry{
					Email:    e,
					Disabled: false,
					Verified: false, // legacy records carry no verification evidence
				})
			}
		}
		return result
	}

	return nil
}

// isOldStringArrayFormat reports whether the first decoded array element starts
// with a JSON string delimiter. Invalid or empty arrays return false; later elements
// are left to the caller's full representation decoder.
func isOldStringArrayFormat(raw string) bool {
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &arr); err != nil || len(arr) == 0 {
		// There is no first-element format hint after invalid or empty input, so
		// the caller continues to its full decoder instead of accepting record entries.
		return false
	}

	first := strings.TrimSpace(string(arr[0]))
	return first != "" && first[0] == '"'
}

// MarshalNotifyEmails encodes recipient records for settings and user storage.
// An empty collection becomes []; a marshaling failure also selects that value,
// so callers receive JSON text without a separate error result.
func MarshalNotifyEmails(entries []Entry) string {
	if len(entries) == 0 {
		return "[]"
	}
	data, err := json.Marshal(entries)
	if err != nil {
		// Settings writers receive a usable empty-array value after encoding fails;
		// this API cannot communicate the marshaling error separately to those callers.
		return "[]"
	}
	return string(data)
}
