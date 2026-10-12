package contact

import (
	"encoding/json"
	"strings"
)

// Entry contains a notification email address and its delivery and verification flags.
type Entry struct {
	Email    string `json:"email"`
	Disabled bool   `json:"disabled"`
	Verified bool   `json:"verified"`
}

// ParseNotifyEmails parses notification email JSON into entries.
// Legacy string arrays produce enabled, unverified entries; object arrays retain their flags.
// Empty or invalid input returns nil.
func ParseNotifyEmails(raw string) []Entry {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}

	// Try the object-array representation first.
	var entries []Entry
	if err := json.Unmarshal([]byte(raw), &entries); err == nil && len(entries) > 0 {
		// Check the representation before accepting object entries.
		if !isOldStringArrayFormat(raw) {
			return entries
		}
	}

	// Accept legacy string arrays.
	var emails []string
	if err := json.Unmarshal([]byte(raw), &emails); err == nil {
		result := make([]Entry, 0, len(emails))
		for _, e := range emails {
			e = strings.TrimSpace(e)
			if e != "" {
				result = append(result, Entry{
					Email:    e,
					Disabled: false,
					Verified: false, // Old format emails default to unverified
				})
			}
		}
		return result
	}

	return nil
}

// isOldStringArrayFormat checks if the JSON is a string array like ["email1","email2"].
func isOldStringArrayFormat(raw string) bool {
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &arr); err != nil || len(arr) == 0 {
		return false
	}

	first := strings.TrimSpace(string(arr[0]))
	return first != "" && first[0] == '"'
}

// MarshalNotifyEmails encodes notification entries as JSON.
func MarshalNotifyEmails(entries []Entry) string {
	if len(entries) == 0 {
		return "[]"
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return "[]"
	}
	return string(data)
}
