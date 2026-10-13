package vertex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Vertex batch image results carry a key used to associate each result with
// its submitted item. Input keys and prompts are trimmed before encoding, and
// repeated normalized keys are rejected so two input spellings cannot claim
// the same output identity.
//
// Every item becomes one newline-terminated request containing a user prompt
// followed by its reference images in order. References select either raw
// bytes for inline base64 encoding or a file URI; ambiguous or empty sources
// fail validation. The model is validated here and supplied separately when
// the batch job is created.

// BatchJSONLInput carries the job model and ordered Vertex image requests.
type BatchJSONLInput struct {
	// Model is required for job-level validation. BuildVertexBatchJSONL omits it
	// from each line because the submitter supplies the batch job model.
	Model string           // batch job model
	Items []BatchJSONLItem // ordered batch request items
}

// BatchJSONLItem pairs a result key with its prompt and image references.
type BatchJSONLItem struct {
	CustomID, Prompt string           // CustomID identifies returned results; Prompt supplies user text.
	ReferenceImages  []BatchReference // ordered prompt image inputs
}

// BatchReference describes one inline or URI-backed image in a Vertex request.
type BatchReference struct {
	MimeType string // caller-normalized image MIME type
	// Data and FileURI describe alternative image sources. Exactly one must
	// contain data after FileURI is trimmed; both set or both empty return an
	// input error.
	Data    []byte // raw image bytes
	FileURI string // image object resource URI
}

// BuildVertexBatchJSONL encodes ordered image requests with unique normalized
// result keys into newline-terminated Vertex JSONL. Callers provide a nonnil
// inputError callback for validation failures and a nonblank model, which is
// carried separately by batch submission.
//
// Trimming keys happens before uniqueness checks so padded spellings cannot
// produce duplicate result identities. Prompts are also trimmed and required;
// each reference chooses exactly one source. Any invalid item discards the
// accumulated output and returns its error, preserving the submitted item set
// as one encoded result.
func BuildVertexBatchJSONL(input BatchJSONLInput, inputError func(string, ...any) error) ([]byte, error) {
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
		// Check the normalized key before encoding this item; duplicate keys would
		// make later batch results ambiguous to the task result matcher.
		if _, ok := seen[customID]; ok {
			return nil, inputError("duplicate custom_id %q", customID)
		}
		seen[customID] = struct{}{}
		prompt := strings.TrimSpace(item.Prompt)
		if prompt == "" {
			return nil, inputError("prompt is required for custom_id %q", customID)
		}
		parts, err := vertexBatchImageParts(prompt, item.ReferenceImages, inputError)
		if err != nil {
			return nil, err
		}
		line := map[string]any{
			"key": customID,
			"request": map[string]any{
				"contents": []any{map[string]any{
					"role":  "user",
					"parts": parts,
				}},
				"generationConfig": map[string]any{
					"responseModalities": []string{"TEXT", "IMAGE"},
				},
			},
		}
		if err := enc.Encode(line); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// vertexBatchImageParts returns the prompt followed by encoded references in
// input order. Each reference requires a nonempty MIME type and exactly one of
// nonempty Data or trimmed FileURI; validation uses inputError.
func vertexBatchImageParts(prompt string, refs []BatchReference, inputError func(string, ...any) error) ([]any, error) {
	parts := []any{map[string]any{"text": prompt}}
	for _, ref := range refs {
		mimeType := ref.MimeType
		if mimeType == "" {
			return nil, inputError("reference image mime_type is required")
		}
		fileURI := strings.TrimSpace(ref.FileURI)
		// Select one wire representation per reference so its source is unambiguous:
		// bytes become inlineData, while a trimmed URI becomes fileData.
		switch {
		case len(ref.Data) > 0 && fileURI == "":
			parts = append(parts, map[string]any{
				"inlineData": map[string]any{
					"mimeType": mimeType,
					"data":     base64.StdEncoding.EncodeToString(ref.Data),
				},
			})
		case len(ref.Data) == 0 && fileURI != "":
			parts = append(parts, map[string]any{
				"fileData": map[string]any{
					"mimeType": mimeType,
					"fileUri":  fileURI,
				},
			})
		default:
			return nil, inputError("reference image must contain exactly one of data or file_uri")
		}
	}
	return parts, nil
}
