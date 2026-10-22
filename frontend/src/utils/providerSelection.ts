/**
 * providerSelection.ts collects provider identifiers across filtered list pages for
 * bulk selection. Every page receives the same shallow filter snapshot with lightweight
 * rows enabled and scheduler scores disabled. Page count uses the larger of the
 * reported pages and the total-derived count.
 *
 * The function returns a deduplicated list only after its count matches the first
 * page's total. This rejects count-mismatched results before replacing
 * the caller's existing selection. Pages are requested sequentially, errors propagate
 * to the caller, and duplicate identifiers retain their first encountered position in
 * the result.
 */

/**
 * ProviderIDRow contains the identifier needed from a lightweight provider item.
 */
interface ProviderIDRow {
  id: number // provider numeric identifier
}

/**
 * ProviderListPage supplies identifiers and pagination counts for bulk selection.
 */
interface ProviderListPage {
  /**
   * items contains the current page IDs, while the first page total supplies the
   * expected unique count. The first page pages value and total-derived page
   * count jointly decide how many pages are requested.
   */
  items: ProviderIDRow[] // providers on page
  total: number // expected matching provider count
  pages?: number // reported page count
}

/**
 * ProviderPageFetcher retrieves one numbered provider page using supplied size and
 * filters.
 */
type ProviderPageFetcher = (
  page: number,
  pageSize: number,
  filters: Record<string, unknown>,
) => Promise<ProviderListPage>

/**
 * SELECT_ALL_PAGE_SIZE requests one thousand lightweight providers per page and defines
 * the divisor for total-derived pagination during bulk selection.
 */
const SELECT_ALL_PAGE_SIZE = 1000

/**
 * fetchAllProviderIds returns deduplicated provider IDs gathered from filtered list pages.
 * It copies the outer filters, forces lightweight responses without scheduler scores,
 * and requests all required pages sequentially.
 *
 * The first page defines the expected total and the page count together with the fixed
 * page size. IDs are deduplicated in encounter order before completeness is checked. A
 * fetch failure propagates, and a unique-count mismatch throws, allowing the caller to
 * retain its previous selection rather than accept a partial replacement.
 */
export async function fetchAllProviderIds(
  fetchPage: ProviderPageFetcher,
  filters: Record<string, unknown>,
): Promise<number[]> {
  const requestFilters = {
    ...filters,
    lite: '1',
    include_scheduler_score: '0',
  }
  const firstPage = await fetchPage(1, SELECT_ALL_PAGE_SIZE, requestFilters)
  /**
   * Use both reported pagination and the total-derived lower bound so a smaller pages
   * field cannot truncate bulk selection.
   */
  const pageCount = Math.max(
    firstPage.pages ?? 0,
    Math.ceil(firstPage.total / SELECT_ALL_PAGE_SIZE),
  )
  const ids = firstPage.items.map((provider) => provider.id)

  for (let page = 2; page <= pageCount; page++) {
    const result = await fetchPage(page, SELECT_ALL_PAGE_SIZE, requestFilters)
    ids.push(...result.items.map((provider) => provider.id))
  }

  /**
   * Deduplicate before comparing against the expected total because repeated rows
   * across pages cannot establish that every matching provider was fetched.
   */
  const uniqueIDs = Array.from(new Set(ids))
  if (uniqueIDs.length !== firstPage.total) {
    throw new Error('Provider list is incomplete')
  }
  return uniqueIDs
}
