package schema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUsageLogVideoResolutionCountsCharacters checks the Ent validator against
// PostgreSQL character capacity. Fifty multibyte characters fit the video field,
// while the fifty-first character fails before an insert is attempted.
func TestUsageLogVideoResolutionCountsCharacters(t *testing.T) {
	for _, field := range (UsageLog{}).Fields() {
		descriptor := field.Descriptor()
		if descriptor.Name != "video_resolution" {
			continue
		}
		require.Equal(t, 50, descriptor.Size)
		require.NotEmpty(t, descriptor.Validators)
		for _, validator := range descriptor.Validators {
			validate, ok := validator.(func(string) error)
			require.True(t, ok)
			require.NoError(t, validate(strings.Repeat("界", 50)))
			require.Error(t, validate(strings.Repeat("界", 51)))
		}
		return
	}
	t.Fatal("video_resolution field missing")
}
