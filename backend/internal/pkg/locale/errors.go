package locale

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
)

var (
	// errorMessagesJSON contains user-facing errors shared with the frontend.
	//
	//go:embed error_messages.json
	errorMessagesJSON []byte

	// errorMessages indexes messages by language code and error reason.
	errorMessages = loadErrorMessages()
)

// loadErrorMessages parses embedded messages and panics on invalid JSON.
func loadErrorMessages() map[string]map[string]string {
	var messages map[string]map[string]string
	if err := json.Unmarshal(errorMessagesJSON, &messages); err != nil {
		panic(err)
	}
	return messages
}

// ErrorText selects a message by reason, then HTTP status, then generic failure.
// For English, a nonblank original without Han characters takes precedence.
func ErrorText(language, reason string, status int, original string) string {
	code := Negotiate(language, Default())
	hasChinese := strings.ContainsFunc(original, func(char rune) bool { return unicode.Is(unicode.Han, char) })
	if code == "en" && strings.TrimSpace(original) != "" && !hasChinese {
		return original
	}
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
