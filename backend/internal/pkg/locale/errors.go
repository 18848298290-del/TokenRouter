package locale

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
)

// An English error may already contain a specific public message, while
// historical errors can contain Chinese text. ErrorText keeps a nonblank
// English message with no Han characters, then selects catalog messages by
// normalized language and uppercased reason. That exception lets callers retain
// details that a generic status message would discard.
//
// When the reason is absent from the selected language, the HTTP status entry
// supplies the next choice, followed by REQUEST_FAILED. Missing catalog text
// leaves the supplied original available, and an empty original uses the
// English generic entry. Embedded JSON is parsed at initialization so malformed
// catalogs fail before requests are handled.

var (
	// errorMessagesJSON embeds reason and status messages shared with the frontend
	// so request error rendering uses the same shipped language-specific message data.
	//
	//go:embed error_messages.json
	errorMessagesJSON []byte

	// errorMessages stores startup-decoded text by canonical language and reason,
	// including HTTP status keys and the generic REQUEST_FAILED entry used during selection.
	errorMessages = loadErrorMessages()
)

// loadErrorMessages decodes the embedded message catalog used for error
// rendering. Invalid JSON panics during initialization, so request-time
// selection reads the already parsed map rather than decoding each response.
func loadErrorMessages() map[string]map[string]string {
	var messages map[string]map[string]string
	if err := json.Unmarshal(errorMessagesJSON, &messages); err != nil {
		panic(err)
	}
	return messages
}

// ErrorText chooses a public error message for the negotiated language. A
// nonblank original message with no Han characters takes precedence for
// English, preserving specific caller-provided detail such as validation
// failures.
//
// Other cases select an uppercased reason entry, then an HTTP status entry,
// then REQUEST_FAILED in the chosen language. If those entries are empty, any
// nonempty original is returned as supplied; an empty original uses the English
// REQUEST_FAILED entry. Whitespace is examined for the English exception but is
// retained in returned original text.
func ErrorText(language, reason string, status int, original string) string {
	code := Negotiate(language, Default())
	hasChinese := strings.ContainsFunc(original, func(char rune) bool { return unicode.Is(unicode.Han, char) })
	if code == "en" && strings.TrimSpace(original) != "" && !hasChinese {
		return original
	}
	// Historical or missing messages need progressively broader catalog matches.
	// Try reason, status, and generic text before returning the supplied original.
	messages := errorMessages[code]
	if value := messages[strings.ToUpper(reason)]; value != "" {
		return value
	}
	if value := messages["HTTP_"+strconv.Itoa(status)]; value != "" {
		return value
	}
	if value := messages["REQUEST_FAILED"]; value != "" {
		return value
	}
	if original != "" {
		return original
	}
	return errorMessages["en"]["REQUEST_FAILED"]
}
