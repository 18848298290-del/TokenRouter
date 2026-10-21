import { ref, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { adminAPI } from '@/api/admin'
import { isUpstreamUsageQueryEnabled } from '@/utils/upstreamUsage'
import type {
  Provider,
  UpstreamUsageQueryError,
  UpstreamUsageQueryResult,
} from '@/api/admin/providers.types'

// useProviderUpstreamUsage.ts queries upstream balances and quotas for the
// administrator provider table. Successful observations are stored under the
// administrator identity and provider configuration, so changing the account,
// adapter, endpoint, proxy revision, or provider revision changes which
// snapshot can be restored. Stored JSON is checked before it becomes visible in
// a usage cell.
//
// Each provider has a request token shared by single queries, bulk queries, and
// invalidation. A response updates the display and session storage only while
// its captured token remains current. Bulk work resolves off-page selections,
// filters eligible providers, and processes bounded chunks while retaining
// per-provider success and error information.

// useProviderUpstreamUsage owns cached upstream observations and request
// ownership for one mounted provider page. Pass the live provider rows and
// selected IDs; the returned maps are consumed by table cells, and update
// handlers invalidate observations through the returned methods.
//
// Session-cache identities incorporate the current administrator and provider
// settings. Single and bulk responses compare per-provider tokens before
// publishing, so an invalidation or newer query can discard an earlier response
// even when its network request finishes successfully.
export function useProviderUpstreamUsage(
  providers: Ref<Provider[]>,
  selIds: Readonly<Ref<number[]>>,
) {
  // The translator and application store supply messages for this controller
  // while its request and selection state belongs to this mounted page.
  const { t } = useI18n()
  const appStore = useAppStore()
  const authStore = useAuthStore()
  // The result, error, and loading maps publish each provider independently;
  // upstreamUsageRequestTokenByProviderId records which request can update those
  // maps and session storage.
  const upstreamUsageByProviderId = ref<Record<string, UpstreamUsageQueryResult | null>>({})
  const upstreamUsageErrorByProviderId = ref<Record<string, UpstreamUsageQueryError | null>>({})
  const upstreamUsageLoadingByProviderId = ref<Record<string, boolean>>({})
  const upstreamUsageRequestTokenByProviderId = ref<Record<string, number>>({})

  // upstreamUsageBulkLoading prevents another bulk action while metadata
  // resolution and all admitted chunk workers are still running.
  const upstreamUsageBulkLoading = ref(false)

  // UPSTREAM_USAGE_CACHE_TTL limits browser snapshots to five minutes; batch size
  // and concurrency separately bound API payloads and simultaneous chunk
  // requests.
  const UPSTREAM_USAGE_CACHE_TTL = 5 * 60 * 1000
  const UPSTREAM_USAGE_BATCH_SIZE = 100
  const UPSTREAM_USAGE_BATCH_CONCURRENCY = 4

  // upstreamUsageRequestToken allocates ownership values, while
  // hydratedUpstreamUsageAdminID detects account changes before any stored
  // observation is restored into visible state.
  let upstreamUsageRequestToken = 0
  let hydratedUpstreamUsageAdminID: number | null | undefined

  // UPSTREAM_USAGE_CACHE_PREFIX namespaces browser snapshots by cache format
  // before administrator, provider, and configuration identity are appended to
  // each key.
  const UPSTREAM_USAGE_CACHE_PREFIX = 'tokenrouter:admin:upstream-usage:v1:'

  // effectiveUpstreamUsageAdapter resolves the adapter name expected by stored
  // observations. CN billing modes choose fixed adapter names; supported relay
  // choices retain their configured name, and other relay configurations use
  // sub2api.
  const effectiveUpstreamUsageAdapter = (provider: Provider) => {
    if (provider.platform === 'kimi') {
      return provider.credentials?.provider_mode === 'coding' ? 'kimi_coding' : 'kimi_balance'
    }
    if (provider.platform === 'zhipu') {
      return provider.credentials?.provider_mode === 'coding' ? 'zhipu_coding' : ''
    }
    if (provider.platform === 'deepseek') return 'deepseek_balance'
    const rawConfig = provider.extra?.upstream_usage_query as Record<string, unknown> | undefined
    return rawConfig?.adapter === 'new_api' ||
      rawConfig?.adapter === 'zivv' ||
      rawConfig?.adapter === 'derouter'
      ? rawConfig.adapter
      : 'sub2api'
  }

  // upstreamUsageCacheIdentity hashes an endpoint string into a compact cache-key
  // segment. The browser can distinguish configured addresses while
  // session-storage keys contain the unsigned hexadecimal hash instead of the
  // address text.
  const upstreamUsageCacheIdentity = (value: string) => {
    // The initial accumulator and multiplier keep address hashing deterministic
    // across repeated cache reads.
    let hash = 2166136261
    for (let index = 0; index < value.length; index += 1) {
      hash ^= value.charCodeAt(index)
      hash = Math.imul(hash, 16777619)
    }
    return (hash >>> 0).toString(16)
  }

  // upstreamUsageCacheKey identifies an observation by administrator, provider
  // revision, and query configuration. It returns an empty key when the current
  // user lacks a positive safe-integer ID, preventing anonymous observations from
  // sharing a session-storage namespace.
  //
  // The key combines the effective adapter with normalized query and forwarding
  // addresses, provider type and platform, and proxy identity and revision.
  // Hashing the addresses keeps their text out of the key while the remaining
  // fields separate revisions that require a fresh upstream observation.
  const upstreamUsageCacheKey = (provider: Provider) => {
    const adminID =
      typeof authStore.user?.id === 'number' &&
      Number.isSafeInteger(authStore.user.id) &&
      authStore.user.id > 0
        ? String(authStore.user.id)
        : null
    if (!adminID) return ''
    const rawConfig = provider.extra?.upstream_usage_query as Record<string, unknown> | undefined
    const config = {
      enabled: rawConfig?.enabled !== false,
      adapter:
        effectiveUpstreamUsageAdapter(provider) ||
        (typeof rawConfig?.adapter === 'string' && rawConfig.adapter.trim()
          ? rawConfig.adapter.trim()
          : 'sub2api'),
      base_url: upstreamUsageCacheIdentity(
        typeof rawConfig?.base_url === 'string' ? rawConfig.base_url.trim() : '',
      ),
    }
    const providerBaseURL =
      typeof provider.credentials?.base_url === 'string' ? provider.credentials.base_url.trim() : ''
    const proxyUpdatedAt = provider.proxy?.updated_at ?? ''
    return `${UPSTREAM_USAGE_CACHE_PREFIX}${adminID}:${provider.id}:${provider.platform}:${provider.type}:${provider.updated_at}:${provider.proxy_id ?? ''}:${proxyUpdatedAt}:${upstreamUsageCacheIdentity(providerBaseURL)}:${JSON.stringify(config)}`
  }

  // isFiniteOptionalNumber accepts omitted values or finite numbers satisfying
  // the requested sign restriction. The nonNegative flag applies to used and
  // total amounts; remaining balances may legitimately be negative.
  const isFiniteOptionalNumber = (value: unknown, nonNegative = false) =>
    value == null ||
    (typeof value === 'number' && Number.isFinite(value) && (!nonNegative || value >= 0))

  // isValidUpstreamUsageAmount validates a stored balance or quota amount before
  // restoration. At least one numeric measurement is required, and supplied used
  // or total values must be finite and nonnegative.
  const isValidUpstreamUsageAmount = (value: unknown) => {
    if (!value || typeof value !== 'object') return false
    const amount = value as Record<string, unknown>
    return (
      isFiniteOptionalNumber(amount.used, true) &&
      isFiniteOptionalNumber(amount.total, true) &&
      isFiniteOptionalNumber(amount.remaining) &&
      [amount.used, amount.total, amount.remaining].some((item) => typeof item === 'number')
    )
  }

  // isValidUpstreamUsageLimit validates one named quota limit from a stored
  // response. It requires a numeric measurement, validates any supplied reset
  // timestamp, and permits negative remaining values while rejecting negative
  // usage limits.
  const isValidUpstreamUsageLimit = (value: unknown) => {
    if (!value || typeof value !== 'object') return false
    const limit = value as Record<string, unknown>
    const resetAt = limit.reset_at
    return (
      typeof limit.name === 'string' &&
      limit.name.trim() !== '' &&
      isFiniteOptionalNumber(limit.used, true) &&
      isFiniteOptionalNumber(limit.limit, true) &&
      isFiniteOptionalNumber(limit.remaining) &&
      [limit.used, limit.limit, limit.remaining].some((item) => typeof item === 'number') &&
      (resetAt == null || (typeof resetAt === 'string' && Number.isFinite(Date.parse(resetAt))))
    )
  }

  // isValidUpstreamUsageInfo checks whether a stored usage object has a usable
  // representation for its declared mode. Optional amounts and dates are
  // validated before mode-specific requirements are applied. Balance and quota
  // modes require an amount, while limits and subscription modes require their
  // corresponding details.
  //
  // An unlimited subscription cannot simultaneously advertise a numeric remainder
  // or nonempty limits. Finite subscriptions need a remainder or limits, so
  // corrupted or incomplete snapshots cannot appear as meaningful quota
  // observations in the table.
  const isValidUpstreamUsageInfo = (value: unknown) => {
    if (!value || typeof value !== 'object') return false
    const usage = value as Record<string, unknown>
    const unit = usage.unit
    const mode = usage.mode
    const limits = usage.limits
    const subscription = usage.subscription
    if (
      typeof usage.provider !== 'string' ||
      usage.provider.trim() === '' ||
      typeof mode !== 'string' ||
      !['balance', 'quota', 'limits', 'subscription'].includes(mode) ||
      (unit != null && !['USD', 'CNY', 'TOKENS', 'PERCENT'].includes(String(unit))) ||
      (usage.expires_at != null &&
        (typeof usage.expires_at !== 'string' || !Number.isFinite(Date.parse(usage.expires_at)))) ||
      (usage.balance != null && !isValidUpstreamUsageAmount(usage.balance)) ||
      (usage.balances != null &&
        (!Array.isArray(usage.balances) ||
          !usage.balances.every((item) => {
            if (!item || typeof item !== 'object') return false
            const balance = item as Record<string, unknown>
            return (
              typeof balance.currency === 'string' &&
              balance.currency.trim() !== '' &&
              typeof balance.remaining === 'number' &&
              Number.isFinite(balance.remaining)
            )
          }))) ||
      (limits != null && (!Array.isArray(limits) || !limits.every(isValidUpstreamUsageLimit)))
    )
      return false
    if (subscription != null) {
      if (typeof subscription !== 'object') return false
      const item = subscription as Record<string, unknown>
      if (
        typeof item.plan_name !== 'string' ||
        item.plan_name.trim() === '' ||
        (item.unlimited != null && typeof item.unlimited !== 'boolean') ||
        !isFiniteOptionalNumber(item.remaining) ||
        (item.expires_at != null &&
          (typeof item.expires_at !== 'string' || !Number.isFinite(Date.parse(item.expires_at)))) ||
        (item.limits != null &&
          (!Array.isArray(item.limits) || !item.limits.every(isValidUpstreamUsageLimit)))
      )
        return false
      if (
        item.unlimited === true &&
        (typeof item.remaining === 'number' ||
          (Array.isArray(item.limits) && item.limits.length > 0))
      )
        return false
      if (
        item.unlimited !== true &&
        typeof item.remaining !== 'number' &&
        (!Array.isArray(item.limits) || item.limits.length === 0)
      )
        return false
    }
    if ((mode === 'balance' || mode === 'quota') && !isValidUpstreamUsageAmount(usage.balance))
      return false
    if (
      mode === 'limits' &&
      (!Array.isArray(limits) || limits.length === 0) &&
      (!subscription ||
        typeof subscription !== 'object' ||
        !Array.isArray((subscription as Record<string, unknown>).limits) ||
        ((subscription as Record<string, unknown>).limits as unknown[]).length === 0)
    )
      return false
    if (mode === 'subscription' && (!subscription || typeof subscription !== 'object')) return false
    return true
  }

  // isValidUpstreamUsageResult verifies that a response belongs to the requested
  // provider and active adapter before accepting its usage data. Disabled query
  // configuration, mismatched IDs, adapter changes, and invalid observation
  // timestamps reject the response.
  //
  // The API can supply usage in a nested object or in the older top-level
  // representation. Both forms pass through the same usage validation, retaining
  // their representation while preventing cached observations from being
  // associated with a different provider or query format.
  const isValidUpstreamUsageResult = (
    provider: Provider,
    data: unknown,
  ): data is UpstreamUsageQueryResult => {
    if (!data || typeof data !== 'object') return false
    const result = data as UpstreamUsageQueryResult
    const rawConfig = provider.extra?.upstream_usage_query as Record<string, unknown> | undefined
    const expectedAdapter = effectiveUpstreamUsageAdapter(provider)
    if (
      result.provider_id !== provider.id ||
      rawConfig?.enabled === false ||
      result.adapter !== expectedAdapter ||
      typeof result.observed_at !== 'string' ||
      !Number.isFinite(Date.parse(result.observed_at))
    )
      return false
    return result.usage
      ? isValidUpstreamUsageInfo(result.usage)
      : isValidUpstreamUsageInfo({
          provider: result.provider,
          mode: result.mode,
          unit: result.unit,
          balance: result.balance,
          balances: result.balances,
          available: result.available,
          limits: result.limits,
          subscription: result.subscription,
          expires_at: result.expires_at,
        })
  }

  // readUpstreamUsageCache restores a validated observation for the provider's
  // current cache identity. Missing identity or storage entries return null;
  // malformed, expired, or excessively future-dated entries are removed when
  // storage permits.
  const readUpstreamUsageCache = (provider: Provider): UpstreamUsageQueryResult | null => {
    const key = upstreamUsageCacheKey(provider)
    if (!key) return null
    try {
      const raw = sessionStorage.getItem(key)
      if (!raw) return null
      const parsed = JSON.parse(raw) as {
        // data and ts are restored together from the current administrator's
        // provider revision entry; validation and timestamp TTL jointly permit reuse.
        data?: UpstreamUsageQueryResult // Stored upstream observation.
        ts?: number // Epoch milliseconds for TTL.
      }
      // Snapshot timestamps permit one minute of forward clock skew before the
      // cached entry is treated as invalid.
      const now = Date.now()
      if (
        !isValidUpstreamUsageResult(provider, parsed.data) ||
        typeof parsed.ts !== 'number' ||
        !Number.isFinite(parsed.ts) ||
        parsed.ts > now + 60 * 1000 ||
        now - parsed.ts >= UPSTREAM_USAGE_CACHE_TTL
      ) {
        sessionStorage.removeItem(key)
        return null
      }
      return parsed.data
    } catch {
      try {
        sessionStorage.removeItem(key)
      } catch {
        // Storage cleanup may also fail after an invalid read; returning a cache miss
        // still prevents the unusable observation from being displayed.
      }
      return null
    }
  }

  // invalidateUpstreamUsageCache clears one provider's visible observation and
  // stored revisions for the current administrator. It advances that provider's
  // request token before clearing state, so a pending single or bulk response
  // cannot restore the invalidated result.
  //
  // Storage cleanup scans the administrator-and-provider prefix because earlier
  // revisions have different complete keys. Browser storage failures leave the
  // in-memory invalidation in force. Callers invoke this when provider
  // configuration or credentials change, and before actions that make earlier
  // observations obsolete.
  const invalidateUpstreamUsageCache = (providerID: number) => {
    const key = String(providerID)
    // Advance ownership before clearing the display so already-running single
    // and bulk requests cannot restore the invalidated observation.
    upstreamUsageRequestTokenByProviderId.value = {
      ...upstreamUsageRequestTokenByProviderId.value,
      [key]: ++upstreamUsageRequestToken,
    }
    setUpstreamUsageState(providerID, null, null, false)
    try {
      const adminID =
        typeof authStore.user?.id === 'number' &&
        Number.isSafeInteger(authStore.user.id) &&
        authStore.user.id > 0
          ? String(authStore.user.id)
          : null
      if (!adminID) return
      const prefix = `${UPSTREAM_USAGE_CACHE_PREFIX}${adminID}:${providerID}:`
      for (let index = sessionStorage.length - 1; index >= 0; index -= 1) {
        const storageKey = sessionStorage.key(index)
        if (storageKey?.startsWith(prefix)) sessionStorage.removeItem(storageKey)
      }
    } catch {
      // Storage may reject removal, but token and display invalidation have already
      // completed so pending requests cannot republish the cleared observation.
    }
  }

  // invalidateAllUpstreamUsageCache discards visible upstream observations and
  // removes stored snapshots for the current administrator before a full refresh.
  // It clears request-token entries along with result, error, and loading maps,
  // so responses that already captured those tokens lose permission to publish.
  //
  // Storage removal follows in-memory invalidation and scans every revision under
  // that administrator's prefix. A missing administrator identity skips storage
  // cleanup; a storage exception can leave some entries present, while the
  // in-memory invalidation remains effective for requests already in flight.
  const invalidateAllUpstreamUsageCache = () => {
    // Clear display state and captured request ownership before touching storage;
    // responses already in flight then fail their token comparison if removal fails.
    upstreamUsageRequestToken++
    upstreamUsageByProviderId.value = {}
    upstreamUsageErrorByProviderId.value = {}
    upstreamUsageLoadingByProviderId.value = {}
    upstreamUsageRequestTokenByProviderId.value = {}
    try {
      const adminID =
        typeof authStore.user?.id === 'number' &&
        Number.isSafeInteger(authStore.user.id) &&
        authStore.user.id > 0
          ? String(authStore.user.id)
          : null
      if (!adminID) return
      const prefix = `${UPSTREAM_USAGE_CACHE_PREFIX}${adminID}:`
      for (let index = sessionStorage.length - 1; index >= 0; index -= 1) {
        const storageKey = sessionStorage.key(index)
        if (storageKey?.startsWith(prefix)) sessionStorage.removeItem(storageKey)
      }
    } catch {
      // Storage may reject removal, but token and display invalidation have already
      // completed so pending requests cannot republish the cleared observation.
    }
  }

  // writeUpstreamUsageCache stores a valid response under the provider's current
  // cache identity with the current timestamp. Invalid results and missing
  // identities are skipped; storage failures leave the page's in-memory result
  // available.
  const writeUpstreamUsageCache = (provider: Provider, data: UpstreamUsageQueryResult) => {
    try {
      if (!isValidUpstreamUsageResult(provider, data)) return
      const key = upstreamUsageCacheKey(provider)
      if (!key) return
      sessionStorage.setItem(key, JSON.stringify({ data, ts: Date.now() }))
    } catch {
      // Storage failures leave the accepted observation available to the current
      // page; caching remains optional for displaying the completed request.
    }
  }

  // normalizeUpstreamUsageError extracts the error fields displayed by upstream
  // usage cells. A string reason takes precedence over a string code; unsupported
  // message or status values remain undefined in the result.
  const normalizeUpstreamUsageError = (error: unknown): UpstreamUsageQueryError => {
    const value = error && typeof error === 'object' ? (error as Record<string, unknown>) : {}
    return {
      code:
        typeof value.reason === 'string'
          ? value.reason
          : typeof value.code === 'string'
            ? value.code
            : undefined,
      message: typeof value.message === 'string' ? value.message : undefined,
      status: typeof value.status === 'number' ? value.status : undefined,
    }
  }

  // setUpstreamUsageState publishes the result, error, and loading flag for one
  // provider. Each map receives a replacement object so consumers observe the
  // update while other providers retain their entries.
  const setUpstreamUsageState = (
    providerID: number,
    result: UpstreamUsageQueryResult | null,
    error: UpstreamUsageQueryError | null,
    loadingState: boolean,
  ) => {
    const key = String(providerID)
    upstreamUsageByProviderId.value = { ...upstreamUsageByProviderId.value, [key]: result }
    upstreamUsageErrorByProviderId.value = { ...upstreamUsageErrorByProviderId.value, [key]: error }
    upstreamUsageLoadingByProviderId.value = {
      ...upstreamUsageLoadingByProviderId.value,
      [key]: loadingState,
    }
  }

  // hydrateUpstreamUsageCache restores observations for the current visible
  // providers and administrator. An administrator change clears all in-memory
  // results and token entries before any snapshots are read. Enabled providers
  // restore valid cached observations; cache misses clear idle rows while an
  // active request keeps its current loading state.
  //
  // This ordering prevents account changes from displaying a previous
  // administrator's observations. The page invokes hydration after list loads and
  // incremental refreshes, when the provider revisions and configuration needed
  // for cache identity are available.
  const hydrateUpstreamUsageCache = () => {
    const adminID = authStore.user?.id ?? null
    if (hydratedUpstreamUsageAdminID !== adminID) {
      // Clear the previous administrator's display and request ownership before
      // hydrating rows, so account changes cannot reuse those observations in memory.
      upstreamUsageByProviderId.value = {}
      upstreamUsageErrorByProviderId.value = {}
      upstreamUsageLoadingByProviderId.value = {}
      upstreamUsageRequestTokenByProviderId.value = {}
      upstreamUsageRequestToken++
      hydratedUpstreamUsageAdminID = adminID
    }
    for (const provider of providers.value) {
      if (!isUpstreamUsageQueryEnabled(provider)) continue
      const cached = readUpstreamUsageCache(provider)
      if (cached) {
        setUpstreamUsageState(provider.id, cached, null, false)
      } else if (!upstreamUsageLoadingByProviderId.value[String(provider.id)]) {
        setUpstreamUsageState(provider.id, null, null, false)
      }
    }
  }

  // requestUpstreamUsage fetches one enabled provider's observation, reusing a
  // valid cache entry unless force is true. A network request receives a new
  // provider token before loading state is published. Completion validates both
  // that token and the response before writing session storage or updating the
  // cell.
  //
  // A newer query or invalidation makes an earlier response inert. Current
  // failures become normalized cell errors, and invalid successful responses
  // receive the dedicated invalid-response code rather than being cached as
  // usable observations.
  const requestUpstreamUsage = async (
    provider: Provider,
    options?: {
      // force defaults to false; true bypasses the stored snapshot and starts a
      // new request while leaving the existing session entry until a valid replacement.
      force?: boolean
    },
  ) => {
    if (!isUpstreamUsageQueryEnabled(provider)) return
    const key = String(provider.id)
    const force = options?.force === true
    if (!force) {
      const cached = readUpstreamUsageCache(provider)
      if (cached) {
        setUpstreamUsageState(provider.id, cached, null, false)
        return
      }
    }
    // Assign ownership before publishing loading state so a later single query
    // or invalidation can supersede this request independently.
    const requestToken = ++upstreamUsageRequestToken
    upstreamUsageRequestTokenByProviderId.value = {
      ...upstreamUsageRequestTokenByProviderId.value,
      [key]: requestToken,
    }
    setUpstreamUsageState(provider.id, null, null, true)
    try {
      const result = await adminAPI.providers.queryUpstreamUsage(provider.id)
      // A newer query or provider edit has taken ownership; discard this
      // response before either storage or the displayed observation changes.
      if (upstreamUsageRequestTokenByProviderId.value[key] !== requestToken) return
      if (!isValidUpstreamUsageResult(provider, result)) {
        setUpstreamUsageState(provider.id, null, { code: 'UPSTREAM_USAGE_INVALID_RESPONSE' }, false)
        return
      }
      writeUpstreamUsageCache(provider, result)
      setUpstreamUsageState(provider.id, result, null, false)
    } catch (error) {
      // A failed older query must not overwrite the error or loading state
      // belonging to the current provider request.
      if (upstreamUsageRequestTokenByProviderId.value[key] !== requestToken) return
      setUpstreamUsageState(provider.id, null, normalizeUpstreamUsageError(error), false)
    }
  }

  // queryUpstreamUsageChunk requests one group of providers and reports accepted
  // success and failure counts. It captures each provider's existing request
  // token before the network call, because bulk admission has already assigned
  // ownership. A changed token causes that provider's completion to be skipped.
  //
  // Transport failures are applied only to providers whose tokens still match.
  // Successful responses are validated individually before caching; missing usage
  // entries use their returned error or the generic request-failed code,
  // retaining independent outcomes within one batch.
  const queryUpstreamUsageChunk = async (selectedProviders: Provider[]) => {
    const ids = selectedProviders.map((provider) => provider.id)
    if (ids.length === 0) return { success: 0, failed: 0 }
    // Capture ownership at chunk admission because another query can replace
    // individual provider tokens while this batch is still pending.
    const tokens = new Map(
      selectedProviders.map((provider) => [
        provider.id,
        upstreamUsageRequestTokenByProviderId.value[String(provider.id)] ?? 0,
      ]),
    )
    let success = 0
    let failed = 0
    let result: Awaited<ReturnType<typeof adminAPI.providers.queryBatchUpstreamUsage>>
    try {
      result = await adminAPI.providers.queryBatchUpstreamUsage(ids)
    } catch (error) {
      // Apply a transport failure only to still-owned providers so another
      // request can retain its newer result or loading state.
      // Validate each batch entry independently; mismatched tokens belong to
      // newer work and must not affect totals or cached observations.
      for (const provider of selectedProviders) {
        const key = String(provider.id)
        if (upstreamUsageRequestTokenByProviderId.value[key] !== tokens.get(provider.id)) continue
        setUpstreamUsageState(provider.id, null, normalizeUpstreamUsageError(error), false)
        failed++
      }
      return { success, failed }
    }
    for (const provider of selectedProviders) {
      const key = String(provider.id)
      if (upstreamUsageRequestTokenByProviderId.value[key] !== tokens.get(provider.id)) continue
      const data = result.usage?.[key]
      const queryError = result.errors?.[key]
      if (data) {
        if (!isValidUpstreamUsageResult(provider, data)) {
          setUpstreamUsageState(
            provider.id,
            null,
            { code: 'UPSTREAM_USAGE_INVALID_RESPONSE' },
            false,
          )
          failed++
          continue
        }
        writeUpstreamUsageCache(provider, data)
        setUpstreamUsageState(provider.id, data, null, false)
        success++
      } else {
        setUpstreamUsageState(
          provider.id,
          null,
          queryError ?? { code: 'UPSTREAM_USAGE_REQUEST_FAILED' },
          false,
        )
        failed++
      }
    }
    return { success, failed }
  }

  // queryUpstreamUsageChunks divides eligible providers into bounded API requests
  // and totals their outcomes. Workers claim the next chunk index synchronously
  // before awaiting its request, so each chunk is assigned once. The number of
  // workers is capped by the configured concurrency and the number of chunks.
  //
  // Each chunk retains per-provider token checks through queryUpstreamUsageChunk.
  // The controller waits for every worker before returning totals, allowing the
  // bulk action to show one completion message after all admitted requests
  // settle.
  const queryUpstreamUsageChunks = async (selectedProviders: Provider[]) => {
    const chunks: Provider[][] = []
    for (let index = 0; index < selectedProviders.length; index += UPSTREAM_USAGE_BATCH_SIZE) {
      chunks.push(selectedProviders.slice(index, index + UPSTREAM_USAGE_BATCH_SIZE))
    }
    let nextChunk = 0
    let success = 0
    let failed = 0
    // worker consumes chunks from the shared nextChunk index until every group
    // has been claimed. Each invocation reads and advances that index before its
    // first await, so another worker resumes with the following group even when
    // earlier transport requests remain pending.
    //
    // After a chunk settles, its accepted success and failure counts are added
    // synchronously to the shared totals before claiming again. The enclosing
    // function waits for all workers, making those totals complete when it returns.
    const worker = async () => {
      while (nextChunk < chunks.length) {
        const chunkIndex = nextChunk
        // Claim the chunk synchronously before awaiting transport so concurrent
        // workers cannot submit the same provider group twice.
        nextChunk += 1
        const chunkResult = await queryUpstreamUsageChunk(chunks[chunkIndex])
        success += chunkResult.success
        failed += chunkResult.failed
      }
    }
    const workers = Array.from(
      { length: Math.min(UPSTREAM_USAGE_BATCH_CONCURRENCY, chunks.length) },
      () => worker(),
    )
    await Promise.all(workers)
    return { success, failed }
  }

  // handleBulkQueryUpstreamUsage queries the current selection and reports
  // combined outcomes to the administrator. It ignores duplicate starts and empty
  // selections, resolves provider details for selected IDs outside the current
  // page, then filters providers by query eligibility before assigning request
  // tokens.
  //
  // Each selected provider receives its own loading state and token before chunk
  // workers start. Metadata failures contribute to the failure total when
  // supported providers remain. The bulk loading flag is released in finally,
  // including unsupported selections and request failures.
  const handleBulkQueryUpstreamUsage = async () => {
    if (upstreamUsageBulkLoading.value || selIds.value.length === 0) return
    upstreamUsageBulkLoading.value = true
    try {
      const currentProvidersById = new Map(
        providers.value.map((provider) => [provider.id, provider]),
      )
      const selectedProviderResults = await Promise.allSettled(
        selIds.value.map((id) =>
          currentProvidersById.get(id)
            ? Promise.resolve(currentProvidersById.get(id)!)
            : adminAPI.providers.getById(id),
        ),
      )
      const selectedProviders = selectedProviderResults
        .filter((item): item is PromiseFulfilledResult<Provider> => item.status === 'fulfilled')
        .map((item) => item.value)
        .filter(isUpstreamUsageQueryEnabled)
      if (selectedProviders.length === 0) {
        appStore.showWarning(t('admin.providers.upstreamUsage.noSupportedSelection'))
        return
      }
      for (const provider of selectedProviders) {
        const key = String(provider.id)
        upstreamUsageRequestTokenByProviderId.value = {
          ...upstreamUsageRequestTokenByProviderId.value,
          [key]: ++upstreamUsageRequestToken,
        }
        setUpstreamUsageState(provider.id, null, null, true)
      }
      let failed = selectedProviderResults.filter((item) => item.status === 'rejected').length
      const chunkResult = await queryUpstreamUsageChunks(selectedProviders)
      const success = chunkResult.success
      failed += chunkResult.failed
      if (failed > 0) {
        appStore.showError(t('admin.providers.upstreamUsage.partialSuccess', { success, failed }))
      } else {
        appStore.showSuccess(t('admin.providers.upstreamUsage.success', { count: success }))
      }
    } catch (error) {
      console.error('Failed to bulk query upstream usage:', error)
      for (const providerID of selIds.value) {
        if (upstreamUsageLoadingByProviderId.value[String(providerID)]) {
          setUpstreamUsageState(providerID, null, normalizeUpstreamUsageError(error), false)
        }
      }
      appStore.showError(t('admin.providers.upstreamUsage.queryFailed'))
    } finally {
      upstreamUsageBulkLoading.value = false
    }
  }
  return {
    upstreamUsageByProviderId,
    upstreamUsageErrorByProviderId,
    upstreamUsageLoadingByProviderId,
    upstreamUsageBulkLoading,
    upstreamUsageCacheKey,
    invalidateUpstreamUsageCache,
    invalidateAllUpstreamUsageCache,
    hydrateUpstreamUsageCache,
    requestUpstreamUsage,
    handleBulkQueryUpstreamUsage,
  }
}
