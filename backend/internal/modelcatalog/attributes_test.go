package modelcatalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Supplements and route aggregation treat explicit false values and empty
// lists as meaningful capabilities. The fixtures compare those values with
// missing fields, conflicting route conditions, and nested copies so display
// consumers receive the intended restrictions.

// TestAttributeSupplementPreservesCatalog supplies attributes that conflict
// with catalog values and adds one missing modality. Catalog false values,
// empty lists, names, and prices must remain authoritative after supplementation.
func TestAttributeSupplementPreservesCatalog(t *testing.T) {
	catalog, err := Parse([]byte(`{"providers":{"typesafe":{"models":{"jev-preview":{"name":"Catalog preview","modalities":{"input":[]},"structured_output":false,"cost":{"input":0,"output":0}}}}}}`))
	require.NoError(t, err)
	supplement := []byte(`{"provider":"typesafe","input_cost_per_token":0.5,"attributes":{"display_name":"Supplement preview","input_modalities":["text"],"output_modalities":["text"],"structured_output":true}}`)
	require.NoError(t, catalog.ApplyAttributeSupplement("jev-preview", supplement))
	attrs, found := catalog.LookupAttributes("jev-preview", nil)
	require.True(t, found)
	require.Empty(t, *attrs.InputModalities)
	require.False(t, *attrs.StructuredOutput)
	require.Equal(t, "Catalog preview", *attrs.DisplayName)
	require.Equal(t, []string{"text"}, *attrs.OutputModalities)
	require.Zero(t, *catalog.Entries["jev-preview"].Cost.Input)
}

// TestAttributeSupplementRejectsInvalidData supplies another provider's
// metadata to a relay entry and then submits an invalid modality. The relay's
// attributes remain intact, and invalid input adds no catalog entry.
func TestAttributeSupplementRejectsInvalidData(t *testing.T) {
	catalog, err := Parse([]byte(`{"providers":{"relay":{"models":{"jev-preview":{"modalities":{"input":["image"]}}}}}}`))
	require.NoError(t, err)
	require.NoError(t, catalog.ApplyAttributeSupplement("relay/jev-preview", []byte(`{"provider":"typesafe","attributes":{"input_modalities":["text"],"output_modalities":["text"]}}`)))
	attrs, found := catalog.LookupAttributes("relay/jev-preview", nil)
	require.True(t, found)
	require.Equal(t, []string{"image"}, *attrs.InputModalities)
	require.Nil(t, attrs.OutputModalities)
	count := len(catalog.Entries)
	require.Error(t, catalog.ApplyAttributeSupplement("bad-model", []byte(`{"attributes":{"input_modalities":["invalid"]}}`)))
	require.Len(t, catalog.Entries, count)
}

// TestAttributeMergeAndCommon combines explicit clears with a smaller limit
// and an unknown modality list. Merge must retain cleared values, while Common
// returns the restrictive capability, smallest known limit, and unknown modalities.
func TestAttributeMergeAndCommon(t *testing.T) {
	yes, no := true, false
	context, smaller := 100, 50
	modalities, empty := []string{"text", "image", "pdf"}, []string{}
	base := Attributes{Context: &context, Reasoning: &yes, InputModalities: &modalities}
	patched := Merge(base, Attributes{Reasoning: &no, InputModalities: &empty})
	require.Equal(t, 100, *patched.Context)
	require.False(t, *patched.Reasoning)
	require.NotNil(t, patched.InputModalities)
	require.Empty(t, *patched.InputModalities)
	common, different := Common([]Attributes{base, {Context: &smaller, Reasoning: &no}})
	require.True(t, different)
	require.Equal(t, 50, *common.Context)
	require.False(t, *common.Reasoning)
	require.Nil(t, common.InputModalities)
}

// TestCommonContradictoryConditions combines routes with incompatible effort
// or thinking requirements for temperature. The aggregate must disable
// temperature and remove its unsatisfiable condition from the returned attributes.
func TestCommonContradictoryConditions(t *testing.T) {
	for name, conditions := range map[string][2]ParameterCondition{
		"effort":   {{Efforts: []string{"none"}}, {Efforts: []string{"high"}}},
		"thinking": {{Thinking: valuePointer(false)}, {Thinking: valuePointer(true)}},
	} {
		t.Run(name, func(t *testing.T) {
			left := Attributes{Temperature: valuePointer(true), Conditions: valuePointer(ParameterConditions{"temperature": conditions[0]})}
			right := Attributes{Temperature: valuePointer(true), Conditions: valuePointer(ParameterConditions{"temperature": conditions[1]})}
			common, different := Common([]Attributes{left, right})
			require.True(t, different)
			require.Equal(t, valuePointer(false), common.Temperature)
			require.NotContains(t, *common.Conditions, "temperature")
		})
	}
}

// TestMergeMediaEmptyOverrides clears image sizes while retaining the base
// reference limit. The result must keep an explicit empty list and leave the
// base sizes available to subsequent callers.
func TestMergeMediaEmptyOverrides(t *testing.T) {
	base := Attributes{Media: &MediaCapabilities{ImageSizes: []string{"1K", "2K"}, MaxReferenceImages: valuePointer(3)}}
	result := Merge(base, Attributes{Media: &MediaCapabilities{ImageSizes: []string{}}})
	require.NotNil(t, result.Media.ImageSizes)
	require.Empty(t, result.Media.ImageSizes)
	require.Equal(t, valuePointer(3), result.Media.MaxReferenceImages)
	require.Equal(t, []string{"1K", "2K"}, base.Media.ImageSizes)
}

// TestCommonOwnsNestedValues aggregates one route and two equal routes before
// mutating the result's nested values. Both cases must leave their input
// attributes unchanged and retain nil effort choices and explicit empty lists.
func TestCommonOwnsNestedValues(t *testing.T) {
	for _, count := range []int{1, 2} {
		source := Attributes{
			Context: valuePointer(100),
			ReasoningOptions: valuePointer([]ReasoningOption{
				{Type: "effort", Values: []*string{nil, valuePointer("high")}},
			}),
			Conditions: valuePointer(ParameterConditions{"temperature": {Thinking: valuePointer(false), Efforts: []string{"none"}}}),
			Media:      &MediaCapabilities{ImageSizes: []string{}, MaxReferenceImages: valuePointer(3)},
		}
		values := []Attributes{source}
		if count == 2 {
			values = append(values, source.Clone())
		}
		got, different := Common(values)
		require.False(t, different)
		require.Nil(t, got.InputModalities)
		require.NotNil(t, got.Media.ImageSizes)
		require.Nil(t, (*got.ReasoningOptions)[0].Values[0])
		*got.Context = 50
		*(*got.ReasoningOptions)[0].Values[1] = "low"
		condition := (*got.Conditions)["temperature"]
		*condition.Thinking = true
		condition.Efforts[0] = "high"
		*got.Media.MaxReferenceImages = 9
		for _, value := range values {
			require.Equal(t, 100, *value.Context)
			require.Equal(t, "high", *(*value.ReasoningOptions)[0].Values[1])
			require.False(t, *(*value.Conditions)["temperature"].Thinking)
			require.Equal(t, []string{"none"}, (*value.Conditions)["temperature"].Efforts)
			require.Equal(t, 3, *value.Media.MaxReferenceImages)
		}
	}
}
