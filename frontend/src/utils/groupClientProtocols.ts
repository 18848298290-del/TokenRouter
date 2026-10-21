import type { ProtocolID } from '@/api/protocols.types'
import { protocolCatalog } from '@/api/admin/protocolCapabilities'

/**
 * groupClientProtocols.ts selects group client protocols and sanitizes configured
 * conversion targets against the loaded protocol catalog. Supported and default
 * selections use the first group profile and the catalog's global protocol order.
 * Upstream-only protocols are removed from client selections, and a missing catalog
 * preserves supplied values until catalog information is available.
 *
 * Conversion configuration distinguishes a missing source entry from an explicit empty
 * target list. Sanitization copies the outer mapping, removes sources absent from the
 * profile, and filters each remaining target list to allowed unique targets in its
 * existing order. When no group profile exists, nested arrays remain shared with the
 * input mapping.
 */

/**
 * orderedProtocols returns selected client protocols in catalog order. It removes
 * duplicates through membership lookup and excludes upstream-only entries; a missing
 * protocol catalog yields an empty result.
 */
function orderedProtocols(protocols: Iterable<ProtocolID>): ProtocolID[] {
  const selected = new Set(protocols)
  return (protocolCatalog.value?.protocols ?? [])
    .filter((protocol) => !protocol.upstream_only && selected.has(protocol.id))
    .map((protocol) => protocol.id)
}

/**
 * supportedGroupClientProtocols returns the first group profile's supported client
 * protocols in catalog order. Missing catalog or profile data yields an empty list
 * after the shared upstream-only filter.
 */
export function supportedGroupClientProtocols(): ProtocolID[] {
  return orderedProtocols(protocolCatalog.value?.groups[0]?.protocols ?? [])
}

/**
 * defaultGroupClientProtocols returns the first group profile's default client
 * protocols in catalog order. Missing catalog or profile data yields an empty list
 * after the shared upstream-only filter.
 */
export function defaultGroupClientProtocols(): ProtocolID[] {
  return orderedProtocols(protocolCatalog.value?.groups[0]?.defaults ?? [])
}

/**
 * effectiveGroupClientProtocols resolves supplied selections against the first
 * group profile and the client-facing protocol catalog. While the catalog is
 * absent, it returns an array copy with the supplied order and duplicates
 * intact.
 *
 * Once catalog data exists, profile membership limits the candidate set and
 * catalog order controls the result. Unknown and upstream-only entries are
 * removed by that combined selection. An absent profile within a loaded catalog
 * therefore produces no supported selections, whereas an entirely absent catalog
 * preserves the unresolved input.
 */
export function effectiveGroupClientProtocols(
  protocols: readonly ProtocolID[] | null | undefined,
): ProtocolID[] {
  /**
   * Before catalog loading finishes, retain the supplied selection so an unresolved
   * catalog does not erase form values during normalization.
   */
  if (!protocolCatalog.value) return [...(protocols ?? [])]
  const supported = new Set(protocolCatalog.value.groups[0]?.protocols ?? [])
  return orderedProtocols((protocols ?? []).filter((protocol) => supported.has(protocol)))
}

/**
 * setGroupClientProtocol returns a normalized selection after toggling one protocol.
 * Enabling requires current support; disabling or requesting an unsupported protocol
 * removes it, and the caller's input array is left intact.
 */
export function setGroupClientProtocol(
  protocols: readonly ProtocolID[],
  protocol: ProtocolID,
  enabled: boolean,
): ProtocolID[] {
  const next = new Set(protocols)
  if (enabled && supportedGroupClientProtocols().includes(protocol)) next.add(protocol)
  else next.delete(protocol)
  return effectiveGroupClientProtocols([...next])
}

/**
 * sanitizeGroupProtocolFallbacks returns conversion targets accepted by the first group
 * profile. Sources missing from that profile are removed, while valid sources retain an
 * explicit empty list when no allowed target remains.
 *
 * Targets are filtered before submission using catalog membership and first-occurrence
 * uniqueness, retaining the user's surviving order. The result starts as a shallow
 * mapping copy; when no profile exists it returns that copy with the original nested
 * arrays. With a profile, retained source arrays are replaced by filtered copies.
 */
export function sanitizeGroupProtocolFallbacks(
  fallbacks: Partial<Record<ProtocolID, ProtocolID[]>> | null | undefined,
): Partial<Record<ProtocolID, ProtocolID[]>> {
  const next: Partial<Record<ProtocolID, ProtocolID[]>> = { ...(fallbacks ?? {}) }
  const profile = protocolCatalog.value?.groups[0]
  /**
   * Without a loaded group profile, conversion membership is unknown; return the
   * shallow copy so existing draft targets remain available.
   */
  if (!profile) return next
  const sources = new Set(profile.protocols)
  for (const source of Object.keys(next) as ProtocolID[]) {
    if (!sources.has(source)) {
      delete next[source]
      continue
    }
    const allowed = new Set(profile.fallback_targets[source] ?? [])
    /**
     * Filtering each configured source retains an empty array, which distinguishes a
     * source with no conversion targets from an omitted source.
     */
    next[source] = (next[source] ?? []).filter(
      (target, index, list) => allowed.has(target) && list.indexOf(target) === index,
    )
  }
  return next
}
