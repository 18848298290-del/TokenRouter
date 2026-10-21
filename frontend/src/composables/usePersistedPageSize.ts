import { getConfiguredTableDefaultPageSize, normalizeTablePageSize } from '@/utils/tablePreferences'

// usePersistedPageSize.ts exposes the configured default table page size through the
// existing pagination helper API. Runtime table preferences supply the default for every
// page, and the retained setter accepts calls without writing a separate browser
// preference.

// getPersistedPageSize returns the normalized runtime default for table pagination. The
// supplied fallback is used only when the configured default is falsy, and normalization
// enforces the table preference rules.
export function getPersistedPageSize(fallback = getConfiguredTableDefaultPageSize()): number {
  return normalizeTablePageSize(getConfiguredTableDefaultPageSize() || fallback)
}

// setPersistedPageSize accepts the compatibility setter call without persisting its
// argument. Runtime table configuration owns the default page size, while callers may
// still update their current table state separately.
export function setPersistedPageSize(_size: number): void {}
