package pagination

import (
	"strings"
)

const (
	// SortOrderAsc selects ascending order.
	SortOrderAsc = "asc"

	// SortOrderDesc selects descending order.
	SortOrderDesc = "desc"
)

// PaginationParams contains page, size, and sorting options.
type PaginationParams struct {
	Page      int
	PageSize  int
	SortBy    string
	SortOrder string
}

// PaginationResult contains total rows and page metadata.
type PaginationResult struct {
	Total    int64
	Page     int
	PageSize int
	Pages    int
}

// Offset returns the zero-based offset, treating pages below one as the first page.
func (p PaginationParams) Offset() int {
	if p.Page < 1 {
		p.Page = 1
	}
	return (p.Page - 1) * p.Limit()
}

// Limit returns the page size, defaulting to 20 and capping it at 1000.
func (p PaginationParams) Limit() int {
	if p.PageSize < 1 {
		return 20
	}
	if p.PageSize > 1000 {
		return 1000
	}
	return p.PageSize
}

// NormalizeSortOrder returns asc or desc, using defaultOrder for invalid input.
// An invalid defaultOrder selects desc.
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

// NormalizedSortOrder normalizes the requested order with the supplied default.
func (p PaginationParams) NormalizedSortOrder(defaultOrder string) string {
	return NormalizeSortOrder(p.SortOrder, defaultOrder)
}

// ResultFromTotal computes page metadata using the normalized limit.
func ResultFromTotal(total int64, params PaginationParams) *PaginationResult {
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

// Slice returns the requested page, sharing the input's backing array.
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
