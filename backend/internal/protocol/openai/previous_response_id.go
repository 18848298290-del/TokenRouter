package openai

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Responses continuation accepts a response identifier, while message/item
// identifiers describe different objects in conversation history. Format
// classification distinguishes their prefixes after trimming input; it does
// not establish that the upstream owns or can retrieve the named object.
//
// Stateless preparation changes storage and continuation fields through separate
// JSON patches. Removing an identifier lets a caller send the context it rebuilt
// from full input, but this helper does not rebuild that history. Setting
// store=false can succeed while identifier removal fails, so callers needing
// stronger postconditions inspect the returned request. Byte patching leaves
// complete-document validation to the request path that owns the payload.

// These classification labels let continuation callers distinguish empty input,
// recognizable response or history-object identifiers, and formats they cannot identify.
const (
	OpenAIPreviousResponseIDKindEmpty      = "empty"       // absent or blank identifier
	OpenAIPreviousResponseIDKindResponseID = "response_id" // response-shaped continuation identifier
	OpenAIPreviousResponseIDKindMessageID  = "message_id"  // message/item-shaped history identifier
	OpenAIPreviousResponseIDKindUnknown    = "unknown"     // unrecognized identifier format
)

// These anchored patterns bound recognized suffixes to 1-256 ASCII characters;
// the classifier lowercases message-form input while retaining response-prefix
// case sensitivity.
var (
	openAIResponseIDPattern = regexp.MustCompile(`^resp_[A-Za-z0-9_-]{1,256}$`)
	openAIMessageIDPattern  = regexp.MustCompile(`^(msg|message|item|chatcmpl)_[A-Za-z0-9_-]{1,256}$`)
)

// RemovePreviousResponseIDFromBody removes a present top-level continuation
// identifier. Empty bodies, missing fields, or failed deletion return the
// supplied bytes; callers rebuild any required history separately and validate
// complete request JSON before forwarding it.
func RemovePreviousResponseIDFromBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	if !gjson.GetBytes(body, "previous_response_id").Exists() {
		return body
	}
	newBody, err := sjson.DeleteBytes(body, "previous_response_id")
	if err != nil {
		// Failed deletion leaves the caller's continuation reference available.
		// Return its supplied body so recovery can still choose between
		// continuation and full-history replay.
		return body
	}
	return newBody
}

// ClassifyOpenAIPreviousResponseIDKind classifies a trimmed identifier by its
// recognized prefix and bounded suffix. Response matching keeps prefix case;
// message, message-item, and chat-completion matching lowercases the input.
// Unmatched nonblank input returns unknown rather than confirming upstream identity.
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

// StatelessResponsesRequest sets store=false before attempting to remove the
// top-level previous_response_id. Failure of the first patch returns the supplied
// body; failure of the second retains the store patch and the remaining identifier.
//
// The caller prepares any history needed after continuation removal and
// validates the resulting request's required fields. The patches are separate
// best-effort operations, so this helper returns bytes rather than a success
// flag and does not promise both edits completed together. Unrelated request
// data stays outside a full-object decode and reserialization.
func StatelessResponsesRequest(body []byte) []byte {
	normalized, err := sjson.SetBytes(body, "store", false)
	if err != nil {
		// Without the storage patch this preparation has no first completed
		// edit to return; retain the supplied request for the caller's subsequent
		// recovery decision.
		return body
	}
	// Removing continuation follows the completed storage edit. A failed
	// removal keeps that edit, making the caller responsible for inspecting
	// any remaining continuation field.
	if stripped, err := sjson.DeleteBytes(normalized, "previous_response_id"); err == nil {
		normalized = stripped
	}
	return normalized
}
