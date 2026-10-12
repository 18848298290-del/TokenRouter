package modelcatalog

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttributesCloneOwnsNestedValues(t *testing.T) {
	var source Attributes
	require.NoError(t, json.Unmarshal([]byte(`{"display_name":"sample","display_name_localization":{"source":"sample","source_locale":"en","translations":{"zh":{"value":"示例","source_revision":1}},"reviewed_locales":["zh"]},"input_modalities":["text"],"reasoning_options":[{"type":"effort","values":[null,"high"]},{"type":"budget_tokens","min":1,"max":100}],"conditions":{"temperature":{"thinking":false,"efforts":["none"]}},"media":{"image_sizes":[],"max_reference_images":3,"input_fidelity":false}}`), &source))
	before, err := json.Marshal(source)
	require.NoError(t, err)
	clone := source.Clone()
	*clone.DisplayName = "other"
	*clone.DisplayNameLocalization.SourceLocale = "zh"
	delete(clone.DisplayNameLocalization.Translations, "zh")
	clone.DisplayNameLocalization.ReviewedLocales[0] = "en"
	(*clone.InputModalities)[0] = "image"
	*(*clone.ReasoningOptions)[0].Values[1] = "low"
	*(*clone.ReasoningOptions)[1].Max = 2
	condition := (*clone.Conditions)["temperature"]
	*condition.Thinking = true
	condition.Efforts[0] = "high"
	*clone.Media.MaxReferenceImages = 9
	*clone.Media.InputFidelity = true
	clone.Media.ImageSizes = append(clone.Media.ImageSizes, "4K")
	after, err := json.Marshal(source)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	require.NotNil(t, source.Media.ImageSizes)
	require.Nil(t, source.Media.OutputFormats)
}
