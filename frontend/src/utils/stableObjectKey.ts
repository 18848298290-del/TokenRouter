/**
 * stableObjectKey.ts assigns stable string keys to object identities within each
 * resolver. A WeakMap remembers each resolver's keys, while a module-wide counter
 * prevents repeated numeric suffixes across resolvers. Structurally equal objects still
 * receive separate identity keys.
 *
 * A resolver can be retained by a component to keep an object keyed across renders.
 * Recreating the resolver starts a new WeakMap, so the same object then receives a
 * fresh suffix. The shared counter keeps suffixes distinct across those resolver
 * instances within the loaded module. WeakMap keys avoid retaining objects merely
 * because they once received a key, and the object's own fields remain untouched.
 */

/**
 * globalStableObjectKeySeed supplies monotonically increasing suffixes shared by every
 * resolver created from this module instance during its lifetime.
 */
let globalStableObjectKeySeed = 0

/**
 * createStableObjectKeyResolver creates a key lookup scoped to object identity, using
 * item as the default prefix. Each resolver owns a WeakMap and returns its cached
 * string for repeated calls with the same object.
 *
 * New identities receive the next module-wide counter value, so separate resolvers with
 * the same prefix still produce different keys. Objects are stored as weak keys and are
 * not mutated. Callers retain a resolver across renders when they need the same object
 * to keep its assigned key.
 */
export function createStableObjectKeyResolver<T extends object>(prefix = 'item') {
  const keyMap = new WeakMap<T, string>()

  return (item: T): string => {
    const cached = keyMap.get(item)
    if (cached) {
      return cached
    }

    /**
     * Allocate from the shared counter before caching the identity so separate resolver
     * instances cannot reuse the same prefixed numeric suffix.
     */
    const key = `${prefix}-${++globalStableObjectKeySeed}`
    keyMap.set(item, key)
    return key
  }
}
