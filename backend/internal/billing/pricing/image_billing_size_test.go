package pricing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClassifyImageBillingTier supplies named tiers, pixel dimensions, and
// unresolved size text. Recognized values must return the expected longest-edge
// tier; empty, auto, and malformed values stay unresolved.
func TestClassifyImageBillingTier(t *testing.T) {
	tests := []struct {
		name     string // subtest display case label
		size     string // supplied image-size spelling
		wantTier string // expected normalized billing tier
		wantOK   bool   // expected size recognition flag
	}{
		{name: "explicit 2k square", size: "2048x2048", wantTier: "2K", wantOK: true},
		{name: "explicit 2k landscape", size: "2048x1152", wantTier: "2K", wantOK: true},
		{name: "explicit 4k landscape", size: "3840x2160", wantTier: "4K", wantOK: true},
		{name: "explicit 4k portrait", size: "2160x3840", wantTier: "4K", wantOK: true},
		{name: "long edge 1k", size: "1024X768", wantTier: "1K", wantOK: true},
		{name: "long edge 2k", size: "1280x768", wantTier: "2K", wantOK: true},
		{name: "long edge 4k", size: "2560x1600", wantTier: "4K", wantOK: true},
		{name: "tier string 1k", size: "1k", wantTier: "1K", wantOK: true},
		{name: "empty", size: "", wantOK: false},
		{name: "auto", size: "auto", wantOK: false},
		{name: "invalid", size: "not-a-size", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTier, gotOK := ClassifyImageBillingTier(tt.size)
			require.Equal(t, tt.wantOK, gotOK)
			require.Equal(t, tt.wantTier, gotTier)
		})
	}
}

// TestResolveImageBillingSize combines supplied sizes with output dimensions of
// differing tiers. It checks output precedence, recognized counts, retained
// display text, input selection, and the 2K default.
func TestResolveImageBillingSize(t *testing.T) {
	tests := []struct {
		name          string         // subtest display case label
		inputSize     string         // supplied input-size evidence
		outputSizes   []string       // reported output-size evidence
		wantBilling   string         // expected selected billing tier
		wantOutput    string         // expected first output display
		wantSource    string         // expected selection evidence label
		wantBreakdown map[string]int // expected recognized output-tier counts
	}{
		{
			name:          "output wins over input",
			inputSize:     "1024x1024",
			outputSizes:   []string{"3840x2160"},
			wantBilling:   "4K",
			wantOutput:    "3840x2160",
			wantSource:    ImageSizeSourceOutput,
			wantBreakdown: map[string]int{"4K": 1},
		},
		{
			name:        "input fallback",
			inputSize:   "1024x1024",
			wantBilling: "1K",
			wantSource:  ImageSizeSourceInput,
		},
		{
			name:        "auto defaults",
			inputSize:   "auto",
			wantBilling: "2K",
			wantSource:  ImageSizeSourceDefault,
		},
		{
			name:        "empty defaults",
			inputSize:   "",
			wantBilling: "2K",
			wantSource:  ImageSizeSourceDefault,
		},
		{
			name:        "invalid defaults",
			inputSize:   "largest",
			wantBilling: "2K",
			wantSource:  ImageSizeSourceDefault,
		},
		{
			name:          "mixed output chooses highest tier",
			inputSize:     "1024x1024",
			outputSizes:   []string{"1024x1024", "3840x2160", "1280x720"},
			wantBilling:   "4K",
			wantOutput:    "1024x1024",
			wantSource:    ImageSizeSourceOutput,
			wantBreakdown: map[string]int{"1K": 1, "2K": 1, "4K": 1},
		},
		{
			name:        "unparseable output falls back to parseable input",
			inputSize:   "2048x1152",
			outputSizes: []string{"auto"},
			wantBilling: "2K",
			wantOutput:  "auto",
			wantSource:  ImageSizeSourceInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveImageBillingSize(tt.inputSize, tt.outputSizes)
			require.Equal(t, tt.wantBilling, got.BillingSize)
			require.Equal(t, tt.inputSize, got.InputSize)
			require.Equal(t, tt.wantOutput, got.OutputSize)
			require.Equal(t, tt.wantSource, got.Source)
			require.Equal(t, tt.wantBreakdown, got.Breakdown)
		})
	}
}
