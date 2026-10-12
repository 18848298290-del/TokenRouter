package protocol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNormalizeClaudeOutputEffort supplies recognized, differently cased,
// padded, empty, and unknown effort labels. Normalization must retain xhigh
// separately from max and return nil for unsupported or absent choices.
func TestNormalizeClaudeOutputEffort(t *testing.T) {
	tests := []struct {
		input string  // supplied effort label
		want  *string // expected normalized effort
	}{
		{"low", thinkingStringPointer("low")},
		{"medium", thinkingStringPointer("medium")},
		{"high", thinkingStringPointer("high")},
		{"max", thinkingStringPointer("max")},
		{"LOW", thinkingStringPointer("low")},
		{"Max", thinkingStringPointer("max")},
		{" medium ", thinkingStringPointer("medium")},
		{"xhigh", thinkingStringPointer("xhigh")},
		{"XHIGH", thinkingStringPointer("xhigh")},
		{"", nil},
		{"unknown", nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeClaudeOutputEffort(tt.input)
			if tt.want == nil {
				require.Nil(t, got)
			} else {
				require.NotNil(t, got)
				require.Equal(t, *tt.want, *got)
			}
		})
	}
}

// thinkingStringPointer returns a pointer to an effort string.
func thinkingStringPointer(value string) *string { return &value }
