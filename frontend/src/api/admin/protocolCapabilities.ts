import { shallowRef } from 'vue'
import apiClient from '@/api/client'
import type { ProtocolID } from '@/api/protocols.types'

/**
 * protocolCapabilities.ts loads the administrator protocol catalog and derives
 * provider protocol choices from its shared snapshot. The catalog describes
 * capability profiles independently of individual provider settings, so
 * consumers reuse one successful response for the lifetime of this module.
 * Loading and error refs let each consumer render the shared request state.
 *
 * Concurrent loads share the pending promise, and settlement releases that
 * promise so a failed load can be retried. Exact authentication modes select one
 * matching profile after normalization. A wildcard mode intersects all matching
 * profiles, retaining only protocols usable across those modes and preserving
 * the first profile's order. Before a catalog is available, selectors return
 * empty lists.
 */

/**
 * ProtocolDefinition describes a protocol ID, endpoint and platform availability
 * in the shared capability catalog.
 */
export interface ProtocolDefinition {
  id: ProtocolID // Record identifier value.
  name: string // Human-readable display name.
  endpoint: string // Protocol endpoint path.
  upstream_only: boolean // Upstream protocol restriction.
  platforms: string[] // Supported provider platforms.
}

/**
 * ProtocolCatalog connects protocol definitions with provider profiles and group
 * admission choices. Its arrays are shared through protocolCatalog, so consumers
 * treat loaded catalog records as read-only selection data.
 */
export interface ProtocolCatalog {
  auxiliary_operations: {
    operation: string // Auxiliary operation identifier.
    protocol?: ProtocolID // Selected protocol identifier.
    authorization: string // Required authorization scheme.
  }[] // Auxiliary endpoint operations.
  /**
   * Protocol definitions supply the IDs referenced by provider profiles and
   * group choices, allowing selectors to resolve their labels and endpoints.
   */
  protocols: ProtocolDefinition[] // Protocol definition entries.
  /**
   * Provider profiles are selected by platform, type and auth_mode; wildcard
   * mode intersects their protocol lists before offering shared choices.
   */
  providers: {
    platform: string // Provider platform identifier.
    type: string // Represented record category.
    auth_mode: string // Provider authentication mode.
    protocols: ProtocolID[] // Protocol identifier list.
  }[] // Provider capability profiles.
  /**
   * Group capability entries describe permitted protocols and default
   * conversions, separate from individual administrator-configured group
   * records.
   */
  groups: {
    protocols: ProtocolID[] // Protocol identifier list.
    defaults: ProtocolID[] // Default protocol choices.
    fallback_targets: Partial<Record<ProtocolID, ProtocolID[]>> // Permitted target protocol map.
    default_fallbacks: Partial<Record<ProtocolID, ProtocolID[]>> // Default target protocol map.
  }[] // Group capability profiles.
}

/**
 * protocolCatalog holds the last successful capability response shared by
 * administrator protocol selectors until this module is reloaded.
 */
export const protocolCatalog = shallowRef<ProtocolCatalog | null>(null)

/**
 * protocolCatalogLoading reports whether the shared catalog request is pending
 * so subscribing selectors can display loading state.
 */
export const protocolCatalogLoading = shallowRef(false)

/**
 * protocolCatalogError records a failed catalog load and clears when a later
 * request begins or consumers reset the ref.
 */
export const protocolCatalogError = shallowRef(false)

/**
 * pending shares the catalog fetch among concurrent callers and is released by
 * its finalizer after either result.
 */
let pending: Promise<ProtocolCatalog> | undefined
/**
 * loadProtocolCatalog returns the cached catalog or shares one pending fetch
 * across callers. Starting a fetch sets loading and clears the previous error;
 * success publishes its data, while failure marks the shared error state and
 * propagates rejection. The finalizer releases pending on either outcome so
 * later calls can retry a failure. The module retains successful data, and
 * callers observe the same catalog object rather than independent copies.
 */
export function loadProtocolCatalog(): Promise<ProtocolCatalog> {
  if (protocolCatalog.value) return Promise.resolve(protocolCatalog.value)
  if (!pending) {
    protocolCatalogLoading.value = true
    protocolCatalogError.value = false
    /**
     * Publish one shared request promise while loading so concurrent consumers
     * observe the same response and cannot create duplicate catalog reads.
     */
    pending = apiClient
      .get<ProtocolCatalog>('/admin/protocol-capabilities')
      .then(({ data }) => {
        protocolCatalog.value = data
        return data
      })
      .catch((error: unknown) => {
        protocolCatalogError.value = true
        throw error
      })
      .finally(() => {
        /**
         * Release the settled request on success or failure so an unsuccessful
         * catalog load can be attempted again by a later caller.
         */
        pending = undefined
        protocolCatalogLoading.value = false
      })
  }
  return pending
}

/**
 * nativeProtocolOptions selects protocol IDs for a platform, provider type and
 * authentication mode. A wildcard intersects all matching profiles so the
 * returned choices work across every represented mode, retaining the first
 * profile's order. Other modes are trimmed and normalized to the supported PAT
 * or agent-identity spellings before selecting one profile. Missing catalogs or
 * profiles yield an empty list, and exact-mode results reference the catalog's
 * stored protocol array.
 */
export function nativeProtocolOptions(platform: string, type: string, authMode = ''): ProtocolID[] {
  /**
   * A wildcard represents several authentication profiles, so intersect their
   * protocol sets to offer choices supported by every matching mode.
   */
  if (authMode === '*') {
    const profiles =
      protocolCatalog.value?.providers.filter(
        (profile) => profile.platform === platform && profile.type === type,
      ) ?? []
    return (profiles[0]?.protocols ?? []).filter((id) =>
      profiles.every((profile) => profile.protocols.includes(id)),
    )
  }
  /**
   * Normalize accepted authentication spellings before profile lookup because
   * the catalog uses canonical PAT and agent-identity mode names.
   */
  const normalized = authMode.trim().toLowerCase()
  const mode = ['personalaccesstoken', 'personal_access_token'].includes(normalized)
    ? 'personalAccessToken'
    : normalized === 'agentidentity'
      ? 'agentIdentity'
      : ''
  return (
    protocolCatalog.value?.providers.find(
      (profile) =>
        profile.platform === platform && profile.type === type && profile.auth_mode === mode,
    )?.protocols ?? []
  )
}
