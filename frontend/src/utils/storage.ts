/**
 * storage.ts reads browser preferences with a fallback to their former storage
 * keys. The current key takes precedence even when its stored string is empty,
 * so a deliberate new value can suppress older preferences.
 *
 * Migration reads the legacy value before attempting to copy it, and leaves
 * the legacy key intact. A failed copy still returns the value already read,
 * allowing restricted browser storage to preserve preferences for the current
 * operation. A failed read instead returns null because no reliable value was
 * obtained. This helper does not perform atomic migration or synchronize
 * writes from other tabs.
 */

/**
 * readStorageWithLegacyKey returns the current value or migrates a missing
 * value from the legacy key. A non-null current string takes precedence,
 * including an empty string.
 *
 * The legacy read happens before its best-effort copy, so a write failure
 * still returns the retrieved legacy value. The old key remains untouched.
 * Read errors return null, as does an absent storage object; callers decide
 * which application default applies when no value can be recovered.
 */
export function readStorageWithLegacyKey(
  storage: Storage | null,
  key: string,
  legacyKey: string,
): string | null {
  if (!storage) return null
  try {
    const current = storage.getItem(key)
    if (current !== null) return current
    const legacy = storage.getItem(legacyKey)
    if (legacy !== null) {
      try {
        /**
         * Copy only after the legacy read succeeds, so failure to persist the new key
         * does not discard the value already available to this caller.
         */
        storage.setItem(key, legacy)
      } catch {
        /**
         * A failed migration write leaves the previously read legacy value usable for
         * this operation, so return it without claiming persistence succeeded.
         */
      }
    }
    return legacy
  } catch {
    /**
     * A read failure provides no reliable preference value; expose null so the
     * caller can choose its own default.
     */
    return null
  }
}
