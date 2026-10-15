package gemini

import "github.com/tidwall/gjson"

// observation.go extracts independent usage, content, and completion evidence
// from Gemini response payloads.
// Gemini execution can report usage without content, and candidate output can
// arrive before a finish reason. Independent observations let completion code
// distinguish those cases: explicit zero counters establish usage presence,
// while content requires a recognized nonempty part value and termination
// requires a candidate's nonempty finish reason.

// Observation records independent usage, output, and termination evidence.
type Observation struct {
	// HasUsage, Semantic, and Terminal describe independent fields in one
	// payload. A candidate finish reason can appear without content or usage,
	// and reported usage does not imply a completed candidate.
	//
	// HasUsage records any recognized nonnull usage field, including an explicit
	// zero count. The extractor does not require the field to contain a valid number.
	//
	// Semantic records recognized nonempty candidate parts. Text, tool names,
	// inline image data, and file URIs can establish it independently of a finish reason.
	//
	// Terminal records a candidate's nonempty finish reason. It establishes
	// payload-level completion evidence; delivery and error handling belong to the caller.
	HasUsage, Semantic, Terminal bool /* HasUsage: nonnull counter presence;
	Semantic: recognized content presence; Terminal: candidate finish evidence */
}

// ObservePayload reads usage and candidate evidence from a top-level Gemini
// response payload. Each result describes this payload alone; callers accumulate
// observations across stream events when they need evidence for a whole attempt.
// This path-based extractor can read fields from malformed JSON. Callers remove
// internal Google wrappers to expose the expected top-level fields and validate
// the complete document separately when their completion rules require it.
func ObservePayload(payload []byte) Observation {
	value := gjson.ParseBytes(payload)
	observed := Observation{}
	usage := value.Get("usageMetadata")
	for _, field := range []string{"promptTokenCount", "candidatesTokenCount", "totalTokenCount", "cachedContentTokenCount", "thoughtsTokenCount"} {
		v := usage.Get(field)
		if v.Exists() && v.Type != gjson.Null {
			observed.HasUsage = true
		}
	}
	value.Get("candidates").ForEach(func(_, candidate gjson.Result) bool {
		if candidate.Get("finishReason").String() != "" {
			observed.Terminal = true
		}
		candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("text").String() != "" || part.Get("functionCall.name").String() != "" || part.Get("inlineData.data").String() != "" || part.Get("inline_data.data").String() != "" || part.Get("fileData.fileUri").String() != "" {
				observed.Semantic = true
			}
			return true
		})
		return true
	})
	return observed
}
