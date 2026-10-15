package gemini

// batch.go defines Gemini upload and batch-job response records, including
// alternate result field spellings.
// Gemini batch outputs can identify a result file through destination or
// response fields, with camelCase and snake_case variants. The batch-image
// adapter reads these alternatives in precedence order, while inline-response
// fields signal a different result form and job state/error fields describe
// the provider's current observation.

// GeminiUploadedFile describes a file resource returned by Gemini uploads.
type GeminiUploadedFile struct {
	Name        string `json:"name"`        // uploaded resource name
	DisplayName string `json:"displayName"` // uploaded display name
	URI         string `json:"uri"`         // uploaded resource URI
	MimeType    string `json:"mimeType"`    // uploaded MIME type
}

// GeminiBatchJob holds the provider's state and output references for a batch.
type GeminiBatchJob struct {
	Name  string `json:"name"`  // provider batch name
	State string `json:"state"` // provider batch state
	// Dest and Response can both identify output files. GeminiBatchOutputRef
	// tries destination fields first, then response fields, choosing the first
	// nonblank reference; either optional object can be nil in an observation.
	Dest     *GeminiBatchDest     `json:"dest"`     // optional output destination
	Response *GeminiBatchResponse `json:"response"` // optional output response
	Error    *GeminiBatchError    `json:"error"`    // optional failure detail
	// Raw holds auxiliary caller data and is excluded from JSON. The current
	// GeminiBatchHTTPClient initializes this map empty after decoding a job.
	Raw map[string]any `json:"-"`
}

// GeminiBatchDest holds alternate spellings of a batch output-file reference.
type GeminiBatchDest struct {
	// FileName and FileNameSnake decode alternate provider field spellings.
	// GeminiBatchOutputRef prefers a nonblank FileName and tries FileNameSnake
	// when the camelCase observation is absent or blank.
	FileName      string `json:"fileName"`  // camelCase output-file reference
	FileNameSnake string `json:"file_name"` // snake_case output-file reference
}

// GeminiBatchResponse holds file-backed and inline result forms returned by Gemini.
type GeminiBatchResponse struct {
	// ResponsesFile and ResponsesFileSnake name file-backed results.
	// GeminiBatchOutputRef tries the nonblank camelCase field before snake_case
	// after neither destination spelling provided an output reference.
	ResponsesFile      string `json:"responsesFile"`  // camelCase result-file reference
	ResponsesFileSnake string `json:"responses_file"` // snake_case result-file reference
	// InlinedResponses and InlinedResponsesAlt decode alternate inline result
	// spellings. GeminiBatchHasInlineResults considers either nonempty slice
	// sufficient to report this result form, independently of file references.
	InlinedResponses    []any `json:"inlinedResponses"`  // camelCase inline results
	InlinedResponsesAlt []any `json:"inlined_responses"` // snake_case inline results
}

// GeminiBatchError carries the provider's code, status, and batch failure message.
type GeminiBatchError struct {
	Code    string `json:"code"`    // provider error code
	Message string `json:"message"` // provider error message
	Status  string `json:"status"`  // provider error status
}
