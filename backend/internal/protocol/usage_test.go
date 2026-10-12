package protocol

import (
	"testing"
)

// TestApplyCacheTTLOverrideContract checks TTL buckets and excludes aggregate-to-detail filling from the changed flag.
func TestApplyCacheTTLOverrideContract(t *testing.T) {
	tests := []struct {
		name, target          string
		aggregate, five, hour int
		wantFive, wantHour    int
		changed               bool
	}{
		{name: "empty", target: "1h"},
		{name: "aggregate fills 5m without reporting override", target: "5m", aggregate: 9, wantFive: 9},
		{name: "aggregate reclassified to 1h", target: "1h", aggregate: 9, wantHour: 9, changed: true},
		{name: "details remain authoritative", target: "1h", aggregate: 99, five: 3, hour: 4, wantHour: 7, changed: true},
		{name: "mixed to 5m", target: "5m", five: 3, hour: 4, wantFive: 7, changed: true},
		{name: "already 1h", target: "1h", hour: 7, wantHour: 7},
		{name: "already 5m", target: "5m", five: 7, wantFive: 7},
		{name: "unknown target uses 5m", target: "other", hour: 7, wantFive: 7, changed: true},
		{name: "target remains case sensitive", target: "1H", hour: 7, wantFive: 7, changed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := TokenUsage{InputTokens: 13, OutputTokens: 17, CacheReadInputTokens: 19, CacheCreationInputTokens: tt.aggregate, CacheCreation5mTokens: tt.five, CacheCreation1hTokens: tt.hour, ImageOutputTokens: 23, Speed: "fast"}
			original := value
			if got := ApplyCacheTTLOverride(&value, tt.target); got != tt.changed {
				t.Fatalf("changed=%v, want %v", got, tt.changed)
			}
			if value.CacheCreation5mTokens != tt.wantFive || value.CacheCreation1hTokens != tt.wantHour {
				t.Fatalf("buckets=(%d,%d), want (%d,%d)", value.CacheCreation5mTokens, value.CacheCreation1hTokens, tt.wantFive, tt.wantHour)
			}
			value.CacheCreation5mTokens = original.CacheCreation5mTokens
			value.CacheCreation1hTokens = original.CacheCreation1hTokens
			if value != original {
				t.Fatal("reclassification changed unrelated usage fields")
			}
		})
	}
}

// TestIncludeIndependentReasoningTokens checks output totals with separately reported reasoning tokens.
func TestIncludeIndependentReasoningTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                         string
		input, output, total, reason int64
		want                         int64
	}{
		{name: "xAI chat example", input: 32, output: 9, total: 135, reason: 94, want: 103},
		{name: "xAI responses example", input: 32, output: 9, total: 151, reason: 110, want: 119},
		{name: "OpenAI inclusive output", input: 32, output: 103, total: 135, reason: 94, want: 103},
		{name: "gap smaller than reasoning", input: 10, output: 20, total: 33, reason: 5, want: 23},
		{name: "absent total", input: 10, output: 20, total: 0, reason: 5, want: 20},
		{name: "total already complete", input: 10, output: 20, total: 30, reason: 5, want: 20},
		{name: "inconsistent undershoot", input: 10, output: 20, total: 25, reason: 5, want: 20},
		{name: "zero reasoning", input: 10, output: 20, total: 35, reason: 0, want: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := IncludeIndependentReasoningTokens(tt.input, tt.output, tt.total, tt.reason)
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}
