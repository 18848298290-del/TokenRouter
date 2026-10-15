package bridge

// request_options.go defines caller-selected sampling and effort options for
// request conversion.
// Conversion options apply target-specific sampling and effort decisions
// after the caller resolves its model policy. The converters read these flags
// directly, so an opaque model name receives the same field treatment as a
// recognizable name when the supplied options are equal.

// RequestOptions supplies target sampling and effort choices to Messages request converters.
type RequestOptions struct {
	// DropSampling, DropTemperature, and DropTopP control field omission.
	// DropSampling suppresses both temperature and top-p; otherwise their
	// individual flags decide whether those supplied values are forwarded.
	DropSampling    bool // suppress both sampling fields
	DropTemperature bool // suppress temperature field
	DropTopP        bool // suppress top-p field
	// SupportsMaxEffort lets AnthropicToResponses and AnthropicToChatCompletionsRequest
	// retain max effort; false converts that effort to xhigh under their mapping.
	SupportsMaxEffort bool
}
