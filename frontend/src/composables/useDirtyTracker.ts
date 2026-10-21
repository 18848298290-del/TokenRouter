import { computed, reactive } from 'vue'

// useDirtyTracker.ts compares independent form sources against their last recorded JSON
// snapshots. A source has no dirty state until its baseline is defined, allowing
// independently loaded settings to establish their own baselines. Capturing one source
// does not clear changes in other sources.
//
// Equality uses serialized output rather than object identity, so reactive nested edits
// are observed when serialization reads them. Callers supply JSON-representable values and
// choose the moment that counts as clean, such as successful loading or saving.
// Serialization errors propagate, and undefined serialized results remain
// indistinguishable from missing baselines.

// useDirtyTracker returns per-source and aggregate dirty checks against JSON baselines.
// Sources are captured by key at construction; callers record clean snapshots after the
// corresponding data has loaded or saved. Sources with undefined baselines remain clean.
//
// Each baseline is updated independently, so loading one settings group cannot hide edits
// in another. JSON serialization defines equality and can throw for unsupported data.
// markClean captures current source values at invocation, so callers own synchronization
// with in-flight saves.
export function useDirtyTracker<K extends string>(sources: Record<K, () => unknown>) {
  // keys fixes the source names at construction, while baselines independently stores
  // their last defined serialized snapshots for reactive comparison.
  const keys = Object.keys(sources) as K[]
  const baselines = reactive({}) as Partial<Record<K, string>>

  // serialize reads one source and returns its JSON comparison value. Sources must be
  // JSON-serializable; errors propagate, and an undefined result cannot establish the
  // defined baseline used by isDirty.
  const serialize = (key: K) => JSON.stringify(sources[key]())

  // markClean records current serialized values for the requested source keys. Omitting
  // keys records every construction-time source; earlier keys remain updated if a later
  // source throws during serialization.
  function markClean(...targets: K[]) {
    // Snapshot each requested source separately so loading or saving one group leaves
    // other groups' existing comparison baselines intact.
    for (const key of targets.length > 0 ? targets : keys) {
      baselines[key] = serialize(key)
    }
  }

  // isDirty compares one current source with its recorded JSON baseline. It returns false
  // until that source has a defined serialized baseline, including when serialization
  // previously yielded undefined.
  function isDirty(key: K) {
    return baselines[key] !== undefined && baselines[key] !== serialize(key)
  }

  const dirty = computed(() => keys.some(isDirty))

  return { dirty, isDirty, markClean }
}
