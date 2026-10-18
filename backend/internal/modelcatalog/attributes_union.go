package modelcatalog

import (
	"reflect"
	"slices"
)

// attributes_union.go summarizes capabilities available across marketplace routes.
// A model can expose different modalities and controls through different client
// protocols, and intersecting those declarations can hide every output modality.
// Union advertises choices supported by at least one route and uses the largest
// known limits to describe the available range. Unknown declarations remain
// distinct from explicit rejection when no route supplies positive evidence.
//
// Conditions, reasoning budgets, and interleaved representations describe linked
// choices. Distinct combinations remain in presentation variants so a renderer
// can explain each supported combination without inventing a request state.
// These summaries are display data; Common continues to supply conservative
// attributes for consumers that generate executable client configurations.

// Union summarizes available route attributes and copies every returned mutable
// value. Empty input produces an unknown presentation, and known empty lists
// survive when no route contributes choices.
//
// Positive support wins over unknown and false declarations, numeric ceilings
// use the largest known value, and lists retain first-seen order. Conditions
// survive as a scalar only when every route agrees. Differing linked controls
// also retain deduplicated complete variants, allowing display consumers to
// show alternatives while leaving request resolution to the selected route.
func Union(values []Attributes) Presentation {
	result := Presentation{}
	if len(values) == 0 {
		return result
	}
	first := values[0]
	attrs := first.Clone()
	for _, value := range values[1:] {
		result.RouteDifferences = result.RouteDifferences || !reflect.DeepEqual(first, value)
		if attrs.DisplayName == nil || value.DisplayName == nil || *attrs.DisplayName != *value.DisplayName {
			attrs.DisplayName = nil
		}
		if !reflect.DeepEqual(first.Conditions, value.Conditions) {
			attrs.Conditions = nil
		}
		if !reflect.DeepEqual(first.Interleaved, value.Interleaved) {
			attrs.Interleaved = nil
		}
		attrs.Context = maximum(attrs.Context, value.Context)
		attrs.InputLimit = maximum(attrs.InputLimit, value.InputLimit)
		attrs.OutputLimit = maximum(attrs.OutputLimit, value.OutputLimit)
		attrs.Reasoning = unionBool(attrs.Reasoning, value.Reasoning)
		attrs.ToolCall = unionBool(attrs.ToolCall, value.ToolCall)
		attrs.StructuredOutput = unionBool(attrs.StructuredOutput, value.StructuredOutput)
		attrs.Temperature = unionBool(attrs.Temperature, value.Temperature)
		attrs.TopP = unionBool(attrs.TopP, value.TopP)
		attrs.Attachment = unionBool(attrs.Attachment, value.Attachment)
		attrs.InputModalities = unionModalities(attrs.InputModalities, value.InputModalities)
		attrs.OutputModalities = unionModalities(attrs.OutputModalities, value.OutputModalities)
		attrs.ReasoningOptions = unionReasoning(attrs.ReasoningOptions, value.ReasoningOptions)
		attrs.Media = unionMedia(attrs.Media, value.Media)
	}
	result.Attributes = attrs.Clone()
	// Different route values retain unknown evidence and linked choices together.
	// Renderers can distinguish explicit rejection from missing information without publishing route identities.
	if result.RouteDifferences {
		for _, value := range values {
			if !slices.ContainsFunc(result.Variants, func(prior Attributes) bool { return reflect.DeepEqual(prior, value) }) {
				result.Variants = append(result.Variants, value.Clone())
			}
		}
	}
	return result
}

// unionBool combines support evidence, preferring true over unknown over false.
// Nil represents missing evidence; a returned pointer can borrow either input
// until Union clones the completed presentation. A false declaration describes
// its own route, so another unknown route prevents a universal rejection unless
// some route supplies positive support. Starting with the first route keeps an
// all-false input distinguishable from a set containing no known declarations.
func unionBool(a, b *bool) *bool {
	if a != nil && *a {
		return a
	}
	if b != nil && *b {
		return b
	}
	if a == nil || b == nil {
		return nil
	}
	return a
}

// unionChoices returns unique known choices in their first-seen order.
// Nil means unknown, while a known empty input keeps the result explicitly
// empty even when the other input is unknown. Result storage is newly allocated,
// including the known empty case, so JSON consumers can retain a declared empty
// set. This helper compares values directly; reasoning effort pointers require
// unionReasoning's content comparison to avoid duplicate spellings.
func unionChoices[T comparable](a, b []T) []T {
	if a == nil && b == nil {
		return nil
	}
	result := make([]T, 0, len(a)+len(b))
	for _, values := range [][]T{a, b} {
		for _, value := range values {
			if !slices.Contains(result, value) {
				result = append(result, value)
			}
		}
	}
	return result
}

// unionModalities combines optional modality declarations for the display summary.
// Missing pointers remain unknown only when both declarations are missing;
// populated empty lists retain their explicit absence of known choices. A single
// known input can be borrowed, and Union clones the completed attributes before
// exposing the presentation to callers that may mutate it.
func unionModalities(a, b *[]string) *[]string {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return valuePointer(unionChoices(*a, *b))
}

// unionReasoning combines known controls and retains distinct token-budget ranges.
// Effort values compare their contents, including a nil vendor-default entry;
// separate budget records avoid advertising values inside an unsupported gap.
//
// Controls retain first-seen order, and effort records of the same type combine
// their accepted values. Budget records are deduplicated only when both endpoints
// match. The result may borrow nested endpoint pointers until Union clones its
// completed attributes, so callers use this helper only during aggregation.
func unionReasoning(a, b *[]ReasoningOption) *[]ReasoningOption {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	result := slices.Clone(*a)
	for _, option := range *b {
		index := slices.IndexFunc(result, func(prior ReasoningOption) bool { return prior.Type == "effort" && option.Type == "effort" })
		if index >= 0 {
			result[index].Values = slices.Clone(result[index].Values)
			for _, effort := range option.Values {
				if !slices.ContainsFunc(result[index].Values, func(prior *string) bool { return reflect.DeepEqual(prior, effort) }) {
					result[index].Values = append(result[index].Values, effort)
				}
			}
		} else if !slices.ContainsFunc(result, func(prior ReasoningOption) bool { return reflect.DeepEqual(prior, option) }) {
			result = append(result, option)
		}
	}
	return &result
}

// unionMedia summarizes available media choices and maximum known item counts.
// An absent media declaration contributes no known choices; populated fields
// use the same support and unknown rules as the enclosing attribute summary.
//
// A missing record is represented by empty optional fields while combining one
// known record. This lets unknown input-fidelity evidence override a lone false
// declaration, while known lists and numeric ceilings remain available. Both
// absent records stay absent, and Union detaches borrowed values before returning.
func unionMedia(a, b *MediaCapabilities) *MediaCapabilities {
	if a == nil && b == nil {
		return nil
	}
	// Missing media still contributes unknown boolean evidence. Treat it as an
	// empty record so a false declaration on another route cannot imply universal rejection.
	if a == nil {
		a = &MediaCapabilities{}
	}
	if b == nil {
		b = &MediaCapabilities{}
	}
	return &MediaCapabilities{
		VideoOperations:    unionChoices(a.VideoOperations, b.VideoOperations),
		VideoParameters:    unionChoices(a.VideoParameters, b.VideoParameters),
		ImageSizes:         unionChoices(a.ImageSizes, b.ImageSizes),
		AspectRatios:       unionChoices(a.AspectRatios, b.AspectRatios),
		Qualities:          unionChoices(a.Qualities, b.Qualities),
		BackgroundOptions:  unionChoices(a.BackgroundOptions, b.BackgroundOptions),
		OutputFormats:      unionChoices(a.OutputFormats, b.OutputFormats),
		MaxReferenceImages: maximum(a.MaxReferenceImages, b.MaxReferenceImages),
		MaxOutputCount:     maximum(a.MaxOutputCount, b.MaxOutputCount),
		InputFidelity:      unionBool(a.InputFidelity, b.InputFidelity),
	}
}
