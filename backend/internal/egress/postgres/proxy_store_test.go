package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSortedUniqueProviderIDs supplies duplicate, sorted, single, empty, and
// nil ID slices. It checks the returned sorted unique prefix and the preserved
// nil/empty forms for those sampled inputs.
func TestSortedUniqueProviderIDs(t *testing.T) {
	tests := []struct {
		name  string  // ID compaction scenario name
		input []int64 // mutable supplied provider identities
		want  []int64 // expected sorted unique identities
	}{
		{name: "unsorted duplicates", input: []int64{12, 3, 12, 8, 3}, want: []int64{3, 8, 12}},
		{name: "already sorted", input: []int64{3, 8, 12}, want: []int64{3, 8, 12}},
		{name: "single", input: []int64{3}, want: []int64{3}},
		{name: "empty", input: []int64{}, want: []int64{}},
		{name: "nil", input: nil, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, SortedUniqueProviderIDs(tt.input))
		})
	}
}
