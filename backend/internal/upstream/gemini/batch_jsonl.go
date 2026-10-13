package gemini

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
)

// Gemini batch results use an item key to identify the input request that
// produced them. Trimming occurs before key uniqueness checks, so padded
// spellings cannot create competing identities when task results are matched
// back to submitted items.
//
// Each JSONL line contains prompt text followed by its image references and
// requests both text and image output. A reference chooses exactly one of
// inline bytes or an uploaded file URI; input errors discard the accumulated
// output. The validated model is supplied separately to the batch action,
// while protocol-owned types define the serialized line and part shapes.

// BatchJSONLInput carries the selected model and ordered Gemini batch image
// requests.
type BatchJSONLInput struct {
	// Model is required for input validation and passed separately to batch
	// creation; each JSONL line contains only its item key and request.
	Model string           // batch action model identifier
	Items []BatchJSONLItem // ordered batch request items
}

// BatchJSONLItem pairs a result identifier with its prompt and image
// references.
type BatchJSONLItem struct {
	CustomID, Prompt string           // CustomID identifies returned results; Prompt supplies user text.
	ReferenceImages  []BatchReference // ordered prompt image inputs
}

// BatchReference describes one inline or uploaded reference image for a batch
// item.
type BatchReference struct {
	MimeType string // caller-normalized image MIME type
	// Data and FileURI are alternative image sources. Exactly one contains data
	// after FileURI is trimmed; both set or both empty produce an input error.
	Data    []byte // raw inline image bytes
	FileURI string // uploaded image resource URI
}

// geminiJSONLLine binds an item key to the protocol generation request.
type geminiJSONLLine = wire.BatchJSONLLine

// geminiGenerateRequest holds protocol content and configuration for one batch
// item.
type geminiGenerateRequest = wire.BatchGenerateRequest

// geminiContent holds the ordered input parts of a protocol batch item.
type geminiContent = wire.BatchContent

// geminiPart represents prompt text or one reference-image source in the
// protocol.
type geminiPart = wire.BatchPart

// geminiInlineData stores the MIME label and base64 bytes of an inline image.
type geminiInlineData = wire.BatchInlineData

// geminiFileData stores the MIME label and uploaded resource URI of an image.
type geminiFileData = wire.BatchFileData

// geminiGenerationConfig identifies the output modalities requested for a
// protocol batch item.
type geminiGenerationConfig = wire.BatchGenerationConfig

// BuildGeminiBatchJSONL returns newline-terminated image requests with unique
// normalized result keys. The caller supplies a nonnil inputError callback and
// a nonblank model, which is used separately when submitting the batch.
//
// Keys are trimmed before duplicate detection to keep result matching
// unambiguous. Prompts are also trimmed and required, while references choose
// one wire source and keep their order. A failed item returns its error and
// discards previously encoded lines so the caller receives one complete
// encoding of the accepted input set.
func BuildGeminiBatchJSONL(input BatchJSONLInput, inputError func(string, ...any) error) ([]byte, error) {
	if strings.TrimSpace(input.Model) == "" {
		return nil, inputError("model is required")
	}
	if len(input.Items) == 0 {
		return nil, inputError("at least one item is required")
	}

	seen := make(map[string]struct{}, len(input.Items))
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, item := range input.Items {
		customID := strings.TrimSpace(item.CustomID)
		if customID == "" {
			return nil, inputError("custom_id is required")
		}
		// Check the normalized result key before encoding the item so whitespace
		// variants cannot create two submitted requests with the same output
		// identity.
		if _, ok := seen[customID]; ok {
			return nil, inputError("duplicate custom_id %q", customID)
		}
		seen[customID] = struct{}{}

		prompt := strings.TrimSpace(item.Prompt)
		if prompt == "" {
			return nil, inputError("prompt is required for custom_id %q", customID)
		}
		parts, err := batchImageGeminiParts(prompt, item.ReferenceImages, inputError)
		if err != nil {
			return nil, err
		}

		// TODO(batch-image): Add response_mime_type, aspect_ratio, and image_size
		// after the batch provider validates their serialized request representation.
		line := geminiJSONLLine{
			Key: customID,
			Request: geminiGenerateRequest{
				Contents: []geminiContent{{
					Parts: parts,
				}},
				GenerationConfig: geminiGenerationConfig{
					ResponseModalities: []string{"TEXT", "IMAGE"},
				},
			},
		}
		if err := enc.Encode(line); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// batchImageGeminiParts returns prompt text followed by encoded references in
// source order. Each reference requires nonempty MIME text and exactly one of
// bytes or a trimmed file URI; inputError reports invalid sources.
func batchImageGeminiParts(prompt string, refs []BatchReference, inputError func(string, ...any) error) ([]geminiPart, error) {
	parts := []geminiPart{{Text: prompt}}
	for _, ref := range refs {
		mimeType := ref.MimeType
		if mimeType == "" {
			return nil, inputError("reference image mime_type is required")
		}
		fileURI := strings.TrimSpace(ref.FileURI)
		// Choose one protocol image representation per reference: raw bytes become
		// base64 inlineData, while an uploaded URI becomes fileData with its MIME
		// label.
		switch {
		case len(ref.Data) > 0 && fileURI == "":
			parts = append(parts, geminiPart{InlineData: &geminiInlineData{
				MimeType: mimeType,
				Data:     base64.StdEncoding.EncodeToString(ref.Data),
			}})
		case len(ref.Data) == 0 && fileURI != "":
			parts = append(parts, geminiPart{FileData: &geminiFileData{
				MimeType: mimeType,
				FileURI:  fileURI,
			}})
		default:
			return nil, inputError("reference image must contain exactly one of data or file_uri")
		}
	}
	return parts, nil
}
