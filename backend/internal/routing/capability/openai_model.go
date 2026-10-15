package capability

import (
	"strings"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// openai_model.go normalizes OpenAI model spellings and delegates recorded
// reasoning-effort normalization.
// Capability classification can inspect the final segment of a
// provider-qualified model name. Alias normalization lowercases and trims the
// full identity while retaining qualification, version separators, and
// suffixes. Recorded reasoning effort describes a value already sent upstream;
// request capability validation belongs to its separate resolver.

// LastOpenAIModelSegment trims a model and returns its final slash-delimited
// segment for classification. A trailing slash returns an empty segment, so
// callers still handle unclassifiable input.
func LastOpenAIModelSegment(model string) string {
	model = strings.TrimSpace(model)
	if index := strings.LastIndexByte(model, '/'); index >= 0 {
		return strings.TrimSpace(model[index+1:])
	}
	return model
}

// CanonicalizeOpenAIModelAliasSpelling returns an ID with outer whitespace
// removed and letters lowercased. Provider qualification and internal spelling
// remain part of the identity; version separators and suffixes are retained.
func CanonicalizeOpenAIModelAliasSpelling(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// NormalizeRecordedOpenAIEffortForModel delegates normalization of an effort
// already sent to the OpenAI protocol helper. Model is ignored because
// recording the observed value does not validate request capabilities.
func NormalizeRecordedOpenAIEffortForModel(raw string, _ string) string {
	return protocolopenai.NormalizeRecordedReasoningEffort(raw)
}
