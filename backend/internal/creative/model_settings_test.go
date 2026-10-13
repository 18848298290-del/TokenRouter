package creative

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Administrator model settings need stable operation ordering and exact
// group/model uniqueness. These fixtures inspect normalized values and
// rejection cases, then use repository counters to distinguish one shared
// group query from repeated catalogue enumeration during a settings save.

// TestNormalizeCreativeModelSettings supplies whitespace, repeated operations,
// and invalid group/model entries. It checks normalized ordering and requires
// errors for the listed malformed or duplicate settings cases.
func TestNormalizeCreativeModelSettings(t *testing.T) {
	settings, err := NormalizeCreativeModelSettings([]CreativeModelSetting{{
		GroupID:    12,
		Model:      " gemini-3.1-flash-image ",
		Operations: []string{"inpaint", "generate", "generate"},
	}})
	require.NoError(t, err)
	require.Equal(t, []CreativeModelSetting{{
		GroupID:    12,
		Model:      "gemini-3.1-flash-image",
		Operations: []string{"generate", "inpaint"},
	}}, settings)

	for name, input := range map[string][]CreativeModelSetting{
		"非正整数分组": {{GroupID: 0, Model: "image", Operations: []string{CreativeOperationGenerate}}},
		"空模型":    {{GroupID: 1, Model: " ", Operations: []string{CreativeOperationGenerate}}},
		"空能力":    {{GroupID: 1, Model: "image", Operations: nil}},
		"非法能力":   {{GroupID: 1, Model: "image", Operations: []string{"upscale"}}},
		"重复模型": {
			{GroupID: 1, Model: "image", Operations: []string{CreativeOperationGenerate}},
			{GroupID: 1, Model: "image", Operations: []string{CreativeOperationEdit}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeCreativeModelSettings(input)
			require.Error(t, err)
		})
	}
}

// TestCreativeSettingsReuseGroupQuery saves two models belonging to one group
// through a counting fixture. Both survive while group and provider reads
// occur once and candidate inventory remains unread.
func TestCreativeSettingsReuseGroupQuery(t *testing.T) {
	fixture := &targetQueryFixture{}
	service := newTargetQueryPublic(fixture)
	result, err := service.NormalizeCreativeModelSettingsForSave(context.Background(), []CreativeModelSetting{
		{GroupID: 1, Model: "gpt-image-1", Operations: []string{CreativeOperationGenerate}},
		{GroupID: 1, Model: "gpt-image-2", Operations: []string{CreativeOperationGenerate}},
	})
	require.NoError(t, err)
	require.Len(t, result, 2)
	require.Equal(t, []string{"gpt-image-1", "gpt-image-2"}, fixture.checked)
	require.Equal(t, 1, fixture.groupReads)
	require.Equal(t, 1, fixture.providerReads)
	require.Zero(t, fixture.inventoryReads)
}
