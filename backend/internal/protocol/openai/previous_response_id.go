package openai

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	OpenAIPreviousResponseIDKindEmpty      = "empty"
	OpenAIPreviousResponseIDKindResponseID = "response_id"
	OpenAIPreviousResponseIDKindMessageID  = "message_id"
	OpenAIPreviousResponseIDKindUnknown    = "unknown"
)

var (
	openAIResponseIDPattern = regexp.MustCompile(`^resp_[A-Za-z0-9_-]{1,256}$`)
	openAIMessageIDPattern  = regexp.MustCompile(`^(msg|message|item|chatcmpl)_[A-Za-z0-9_-]{1,256}$`)
)

// RemovePreviousResponseIDFromBody removes previous_response_id so callers can rebuild context from full input.
func RemovePreviousResponseIDFromBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	if !gjson.GetBytes(body, "previous_response_id").Exists() {
		return body
	}
	newBody, err := sjson.DeleteBytes(body, "previous_response_id")
	if err != nil {
		return body
	}
	return newBody
}

// ClassifyOpenAIPreviousResponseIDKind classifies response, message, empty, and unknown IDs by their format.
func ClassifyOpenAIPreviousResponseIDKind(id string) string {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return OpenAIPreviousResponseIDKindEmpty
	}
	if openAIResponseIDPattern.MatchString(trimmed) {
		return OpenAIPreviousResponseIDKindResponseID
	}
	if openAIMessageIDPattern.MatchString(strings.ToLower(trimmed)) {
		return OpenAIPreviousResponseIDKindMessageID
	}
	return OpenAIPreviousResponseIDKindUnknown
}

// StatelessResponsesRequest sets store=false and removes previous_response_id when JSON patching succeeds.
func StatelessResponsesRequest(body []byte) []byte {
	normalized, err := sjson.SetBytes(body, "store", false)
	if err != nil {
		return body
	}
	if stripped, err := sjson.DeleteBytes(normalized, "previous_response_id"); err == nil {
		normalized = stripped
	}
	return normalized
}
