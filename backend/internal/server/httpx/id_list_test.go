package httpx

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNormalizeInt64IDList
// exercises empty, duplicated, invalid, and unordered collections.
// Its cases must produce sorted unique positive identifiers while distinguishing
// nil results for empty input from an allocated empty result for invalid supplied entries.
func TestNormalizeInt64IDList(t *testing.T) {
	tests := []struct {
		name string  // normalization case for subtest output
		in   []int64 // supplied resource ID collection
		want []int64 // expected normalized result collection
	}{
		{"nil input", nil, nil},
		{"empty input", []int64{}, nil},
		{"single element", []int64{5}, []int64{5}},
		{"already sorted unique", []int64{1, 2, 3}, []int64{1, 2, 3}},
		{"duplicates removed", []int64{3, 1, 3, 2, 1}, []int64{1, 2, 3}},
		{"zero filtered", []int64{0, 1, 2}, []int64{1, 2}},
		{"negative filtered", []int64{-5, -1, 3}, []int64{3}},
		{"all invalid", []int64{0, -1, -2}, []int64{}},
		{"sorted output", []int64{9, 3, 7, 1}, []int64{1, 3, 7, 9}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeInt64IDList(tc.in)
			if tc.want == nil {
				require.Nil(t, got)
			} else {
				require.Equal(t, tc.want, got)
			}
		})
	}
}
