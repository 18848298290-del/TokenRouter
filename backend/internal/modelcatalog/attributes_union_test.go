package modelcatalog

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUnionAvailableRange combines routes with disjoint modalities, unknown
// fields, explicit rejections, and different ceilings. Positive choices and
// maximum known limits must remain visible, while unknown evidence remains
// distinct from a universally unsupported capability.
func TestUnionAvailableRange(t *testing.T) {
	values := []Attributes{
		{InputModalities: valuePointer([]string{"text"}), OutputModalities: valuePointer([]string{"text"}), Context: valuePointer(100), ToolCall: valuePointer(false), Temperature: valuePointer(false), Media: &MediaCapabilities{ImageSizes: []string{}, InputFidelity: valuePointer(false)}},
		{InputModalities: valuePointer([]string{"text", "image"}), OutputModalities: valuePointer([]string{"image"}), Context: valuePointer(200), ToolCall: valuePointer(true), Attachment: valuePointer(false), Media: &MediaCapabilities{ImageSizes: []string{"1K", "2K"}, MaxOutputCount: valuePointer(4)}},
		{OutputModalities: valuePointer([]string{}), Temperature: valuePointer(false)},
	}
	got := Union(values)
	require.Equal(t, []string{"text", "image"}, *got.InputModalities)
	require.Equal(t, []string{"text", "image"}, *got.OutputModalities)
	require.Equal(t, 200, *got.Context)
	require.True(t, *got.ToolCall)
	require.Nil(t, got.Attachment)
	require.Nil(t, got.Temperature)
	require.Nil(t, got.Media.InputFidelity)
	require.Equal(t, []string{"1K", "2K"}, got.Media.ImageSizes)
	require.Equal(t, 4, *got.Media.MaxOutputCount)
	require.True(t, got.RouteDifferences)
	common, _ := Common(values)
	require.Empty(t, *common.OutputModalities)
	require.Equal(t, 100, *common.Context)
	require.False(t, *common.ToolCall)
}

// TestUnionPresenceAndOwnership checks unknown and explicitly empty fields,
// then mutates nested results. Public summaries and variants must own their
// storage independently of source values and each other.
func TestUnionPresenceAndOwnership(t *testing.T) {
	require.Empty(t, Union(nil))
	empty := Attributes{InputModalities: valuePointer([]string{}), ReasoningOptions: valuePointer([]ReasoningOption{})}
	got := Union([]Attributes{{}, empty})
	require.NotNil(t, got.InputModalities)
	require.Empty(t, *got.InputModalities)
	require.NotNil(t, got.ReasoningOptions)
	body, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(body), `"input_modalities":[]`)
	input := Attributes{InputModalities: valuePointer([]string{"text"}), Media: &MediaCapabilities{ImageSizes: []string{"1K"}}, Conditions: valuePointer(ParameterConditions{"temperature": {Thinking: valuePointer(true)}})}
	got = Union([]Attributes{input, {Temperature: valuePointer(false)}})
	(*got.InputModalities)[0] = "audio"
	got.Media.ImageSizes[0] = "4K"
	(*got.Variants[0].InputModalities)[0] = "video"
	require.Equal(t, []string{"text"}, *input.InputModalities)
	require.Equal(t, []string{"1K"}, input.Media.ImageSizes)
	*(*got.Variants[0].Conditions)["temperature"].Thinking = false
	require.True(t, *(*input.Conditions)["temperature"].Thinking)
}

// TestUnionLinkedAlternatives supplies opposite thinking requirements and
// disjoint budget ranges. The summary must retain exact alternatives and
// deduplicate repeated routes without creating a continuous unsupported budget.
func TestUnionLinkedAlternatives(t *testing.T) {
	left := Attributes{Temperature: valuePointer(true), Conditions: valuePointer(ParameterConditions{"temperature": {Thinking: valuePointer(true)}}), ReasoningOptions: valuePointer([]ReasoningOption{{Type: "budget_tokens", Min: valuePointer(1), Max: valuePointer(10)}, {Type: "effort", Values: []*string{valuePointer("low"), nil}}}), Interleaved: &Interleaved{Field: "reasoning_content"}}
	right := Attributes{Temperature: valuePointer(true), Conditions: valuePointer(ParameterConditions{"temperature": {Thinking: valuePointer(false)}}), ReasoningOptions: valuePointer([]ReasoningOption{{Type: "budget_tokens", Min: valuePointer(100), Max: valuePointer(200)}, {Type: "effort", Values: []*string{nil, valuePointer("high")}}}), Interleaved: &Interleaved{Field: "reasoning_details"}}
	got := Union([]Attributes{left, right, left})
	require.True(t, *got.Temperature)
	require.Nil(t, got.Conditions)
	require.Nil(t, got.Interleaved)
	require.Len(t, got.Variants, 2)
	require.Len(t, *got.ReasoningOptions, 3)
	require.Equal(t, []string{"low", "high"}, got.Efforts())
	require.Equal(t, left, got.Variants[0])
	require.Equal(t, right, got.Variants[1])
}
