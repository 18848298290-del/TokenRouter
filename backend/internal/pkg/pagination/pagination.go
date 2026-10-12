package pagination

import (
	"strings"
)

// Pagination consumers need the same effective size when computing offsets and
// page counts. Limit supplies a default for nonpositive sizes and caps large
// sizes, while slice pagination returns a view into the caller's existing
// storage.

// SortOrderAsc and SortOrderDesc are the normalized direction strings accepted
// by sorting consumers after NormalizeSortOrder resolves input and default text.
const (
	// SortOrderAsc selects ascending order.
	SortOrderAsc = "asc"

	// SortOrderDesc selects descending order.
	SortOrderDesc = "desc"
)

// PaginationParams carries the requested page, size, and storage-facing sort
// choices.
type PaginationParams struct {
	Page      int    // requested one-based page number
	PageSize  int    // requested rows per page
	SortBy    string // storage-facing sort field
	SortOrder string // requested sort direction text
}

// PaginationResult describes total rows and the effective page size returned to
// consumers.
type PaginationResult struct {
	Total    int64 // total matching row count
	Page     int   // requested one-based page number
	PageSize int   // effective rows per page
	Pages    int   // computed page count
}

// Offset returns the row offset using the effective limit and a minimum page of
// one. Normalization occurs on the receiver copy, so the caller's Page field
// retains its supplied value.
func (p PaginationParams) Offset() int {
	if p.Page < 1 {
		p.Page = 1
	}
	return (p.Page - 1) * p.Limit()
}

// Limit returns a positive effective size, defaulting to twenty and capping at
// one thousand. The receiver remains unchanged, allowing callers to retain the
// supplied PageSize separately from the query limit.
func (p PaginationParams) Limit() int {
	if p.PageSize < 1 {
		return 20
	}
	if p.PageSize > 1000 {
		// Bound each page to limit query and response size.
		return 1000
	}
	return p.PageSize
}

// NormalizeSortOrder trims and lowercases order and returns an accepted
// direction. Invalid order uses the normalized defaultOrder; any default other
// than asc selects desc before requested-order handling.
func NormalizeSortOrder(order string, defaultOrder string) string {
	switch strings.ToLower(strings.TrimSpace(defaultOrder)) {
	case SortOrderAsc:
		defaultOrder = SortOrderAsc
	default:
		defaultOrder = SortOrderDesc
	}

	switch strings.ToLower(strings.TrimSpace(order)) {
	case SortOrderAsc:
		return SortOrderAsc
	case SortOrderDesc:
		return SortOrderDesc
	default:
		return defaultOrder
	}
}

// NormalizedSortOrder resolves this request's sorting direction through
// NormalizeSortOrder and the supplied default. It uses the same whitespace,
// case, and invalid-default rules as the standalone helper.
func (p PaginationParams) NormalizedSortOrder(defaultOrder string) string {
	return NormalizeSortOrder(p.SortOrder, defaultOrder)
}

// ResultFromTotal computes page metadata from total and the effective limit.
// Page remains params.Page, even when Offset would normalize it; callers supply
// nonnegative totals representable as int for page arithmetic.
func ResultFromTotal(total int64, params PaginationParams) *PaginationResult {
	// Integer division counts full pages; a remainder requires one additional page.
	pages := int(total) / params.Limit()
	if int(total)%params.Limit() > 0 {
		pages++
	}
	return &PaginationResult{
		Total:    total,
		Page:     params.Page,
		PageSize: params.Limit(),
		Pages:    pages,
	}
}

// Slice returns the requested page as a view of items' backing array. Empty
// input or an offset beyond the available items returns a nonnil empty slice;
// callers control mutations to shared elements.
func Slice[T any](items []T, params PaginationParams) []T {
	if len(items) == 0 {
		return []T{}
	}

	offset := params.Offset()
	if offset >= len(items) {
		return []T{}
	}

	limit := params.Limit()
	end := min(offset+limit, len(items))

	return items[offset:end]
}
