package pagination

import "testing"

// TestNormalizeSortOrder supplies accepted, mixed-case, padded, and invalid
// sort text with varying defaults. Each result must select the expected
// normalized direction, including desc for an invalid default.
func TestNormalizeSortOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string // subtest display name
		input        string // requested sort direction
		defaultOrder string // direction used for invalid input
		want         string // expected normalized result
	}{
		{name: "asc", input: "asc", defaultOrder: "desc", want: "asc"},
		{name: "uppercase asc", input: "ASC", defaultOrder: "desc", want: "asc"},
		{name: "desc", input: "desc", defaultOrder: "asc", want: "desc"},
		{name: "trim spaces", input: "  desc  ", defaultOrder: "asc", want: "desc"},
		{name: "invalid falls back", input: "sideways", defaultOrder: "asc", want: "asc"},
		{name: "empty falls back", input: "", defaultOrder: "desc", want: "desc"},
		{name: "invalid default falls back to desc", input: "", defaultOrder: "wat", want: "desc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeSortOrder(tt.input, tt.defaultOrder); got != tt.want {
				t.Fatalf("NormalizeSortOrder(%q, %q) = %q, want %q", tt.input, tt.defaultOrder, got, tt.want)
			}
		})
	}
}

// TestPaginationParamsNormalizedSortOrder exercises normalization through the
// parameter receiver rather than the standalone helper. Accepted uppercase text
// must become asc, and invalid text must use the supplied asc default.
func TestPaginationParamsNormalizedSortOrder(t *testing.T) {
	t.Parallel()

	params := PaginationParams{SortOrder: "ASC"}
	if got := params.NormalizedSortOrder("desc"); got != "asc" {
		t.Fatalf("NormalizedSortOrder = %q, want asc", got)
	}

	params = PaginationParams{SortOrder: "bad"}
	if got := params.NormalizedSortOrder("asc"); got != "asc" {
		t.Fatalf("NormalizedSortOrder invalid fallback = %q, want asc", got)
	}
}

// TestPaginationParamsLimit supplies nonpositive, ordinary, maximum, and
// oversized requested sizes. The effective limit must use twenty by default and
// remain at most one thousand rows.
func TestPaginationParamsLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string // subtest display name
		pageSize int    // supplied rows per page
		want     int    // expected normalized result
	}{
		{name: "non-positive falls back to default", pageSize: 0, want: 20},
		{name: "negative falls back to default", pageSize: -1, want: 20},
		{name: "normal value keeps", pageSize: 50, want: 50},
		{name: "max value keeps", pageSize: 1000, want: 1000},
		{name: "beyond max clamps to 1000", pageSize: 1500, want: 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := PaginationParams{PageSize: tt.pageSize}
			if got := p.Limit(); got != tt.want {
				t.Fatalf("Limit() for PageSize=%d = %d, want %d", tt.pageSize, got, tt.want)
			}
		})
	}
}

// TestPaginationParamsOffsetUsesNormalizedLimit combines page numbers with
// invalid and oversized requested sizes. Offset must normalize the page minimum
// and multiply by the effective limit used for querying.
func TestPaginationParamsOffsetUsesNormalizedLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string // subtest display name
		page     int    // supplied one-based page number
		pageSize int    // supplied rows per page
		want     int    // expected normalized result
	}{
		{name: "invalid page uses first page", page: 0, pageSize: 50, want: 0},
		{name: "zero page size uses default", page: 2, pageSize: 0, want: 20},
		{name: "negative page size uses default", page: 2, pageSize: -1, want: 20},
		{name: "normal values", page: 3, pageSize: 50, want: 100},
		{name: "page size beyond max is clamped", page: 2, pageSize: 1500, want: 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			params := PaginationParams{Page: tt.page, PageSize: tt.pageSize}
			if got := params.Offset(); got != tt.want {
				t.Fatalf("Offset() for Page=%d, PageSize=%d = %d, want %d", tt.page, tt.pageSize, got, tt.want)
			}
		})
	}
}
