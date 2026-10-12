package gemini

// A Gemini batch JSONL line pairs the caller's custom item identifier with
// one generation request. Image-batch construction emits prompt text followed
// by inline or uploaded reference-image parts, requesting both TEXT and IMAGE
// output modalities through the request's generation configuration.

// BatchJSONLLine binds one batch item key to its generation request.
type BatchJSONLLine struct {
	Key     string               `json:"key"`     // caller item identifier
	Request BatchGenerateRequest `json:"request"` // item generation request
}

// BatchGenerateRequest holds content and output configuration for one batch item.
type BatchGenerateRequest struct {
	Contents         []BatchContent        `json:"contents"`         // ordered input content
	GenerationConfig BatchGenerationConfig `json:"generationConfig"` // requested output configuration
}

// BatchContent holds the ordered input parts for a generation item.
type BatchContent struct {
	Parts []BatchPart `json:"parts"` // ordered input parts
}

// BatchPart represents text, inline image data, or an uploaded-file reference.
type BatchPart struct {
	// Text supplies the prompt, while InlineData and FileData supply reference
	// images. BuildGeminiBatchJSONL starts with a text part and creates each image
	// part from exactly one of inline bytes or a file URI.
	Text       string           `json:"text,omitempty"`       // generation prompt text
	InlineData *BatchInlineData `json:"inlineData,omitempty"` // inline image source
	FileData   *BatchFileData   `json:"fileData,omitempty"`   // uploaded image reference
}

// BatchInlineData holds MIME-identified image bytes encoded for a batch request.
type BatchInlineData struct {
	MimeType string `json:"mimeType"` // image MIME type
	Data     string `json:"data"`     // base64
}

// BatchFileData names an uploaded image used by a batch request.
type BatchFileData struct {
	MimeType string `json:"mimeType"` // image MIME type
	FileURI  string `json:"fileUri"`  // uploaded image URI
}

// BatchGenerationConfig identifies the output modalities requested for a batch item.
type BatchGenerationConfig struct {
	ResponseModalities []string `json:"responseModalities"` // requested output modalities
}
