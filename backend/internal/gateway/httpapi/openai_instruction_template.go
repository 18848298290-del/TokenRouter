package httpapi

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// openai_instruction_template.go renders forced Codex instructions and applies
// nonempty template output to requests.
// Forced Codex instructions are rendered with request model names and existing
// instructions supplied as template data. Empty output leaves the request
// untouched, and trimmed matching instructions avoid an unnecessary
// replacement; parse and execution failures remain available to the caller.

// forcedCodexInstructionsTemplateData supplies request context to configured
// Codex instruction templates.
type forcedCodexInstructionsTemplateData struct {
	ExistingInstructions string // incoming request instruction text
	OriginalModel        string // original client model name
	NormalizedModel      string // normalized request model name
	BillingModel         string // model selected for billing
	UpstreamModel        string // model sent to upstream
}

// applyForcedCodexInstructionsTemplate renders and conditionally replaces
// instructions in reqBody. It returns true only for an assigned replacement.
// Empty rendered output or an identical trimmed value returns false; rendering
// errors leave the map unchanged.
func applyForcedCodexInstructionsTemplate(
	reqBody map[string]any,
	templateText string,
	data forcedCodexInstructionsTemplateData,
) (bool, error) {
	rendered, err := renderForcedCodexInstructionsTemplate(templateText, data)
	if err != nil {
		return false, err
	}
	if rendered == "" {
		return false, nil
	}

	existing, _ := reqBody["instructions"].(string)
	if strings.TrimSpace(existing) == rendered {
		return false, nil
	}

	reqBody["instructions"] = rendered
	return true, nil
}

// renderForcedCodexInstructionsTemplate evaluates configured text against the
// supplied request data. It trims surrounding whitespace, uses zero values for
// missing map keys, and returns contextual errors for template parsing or
// execution failures.
func renderForcedCodexInstructionsTemplate(
	templateText string,
	data forcedCodexInstructionsTemplateData,
) (string, error) {
	tmpl, err := template.New("forced_codex_instructions").Option("missingkey=zero").Parse(templateText)
	if err != nil {
		return "", fmt.Errorf("parse forced codex instructions template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render forced codex instructions template: %w", err)
	}

	return strings.TrimSpace(buf.String()), nil
}
