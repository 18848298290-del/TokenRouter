// common.types.ts defines response envelopes, pagination metadata, and request
// cancellation options shared by API modules. Endpoint-specific records occupy the
// generic data or items property, while list consumers use server totals and page counts
// to navigate results.

// BasePaginationResponse wraps API records with their current page and total counts.
export interface BasePaginationResponse<T> {
  items: T[] // Current result page.
  total: number // Total matching records.
  page: number // Current page number.
  page_size: number // Records per page.
  pages: number // Total page count.
}

// FetchOptions passes request cancellation from callers to API helpers.
export interface FetchOptions {
  signal?: AbortSignal // Caller cancellation signal.
}

// ApiResponse wraps endpoint data with the server result code and message.
export interface ApiResponse<T = unknown> {
  code: number // Server result code.
  message: string // Human-readable result message.
  data: T // Endpoint response payload.
}

// PaginatedResponse describes a page of records returned by list endpoints.
export interface PaginatedResponse<T> {
  items: T[] // Current result page.
  total: number // Total matching records.
  page: number // Current page number.
  page_size: number // Records per page.
  pages: number // Total page count.
}
