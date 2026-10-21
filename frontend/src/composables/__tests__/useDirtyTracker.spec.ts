import { describe, expect, it } from 'vitest'
import { reactive, ref } from 'vue'
import { useDirtyTracker } from '@/composables/useDirtyTracker'

// useDirtyTracker.spec.ts checks independent JSON baselines for reactive settings sources.
// The cases distinguish uninitialized sources, reversible nested edits, and separately
// captured sources so asynchronously loaded settings cannot erase another source's pending
// changes.

describe('useDirtyTracker', () => {
  // A reactive form changes before any clean snapshot is recorded for its source. The
  // aggregate dirty flag must remain false until a baseline exists.
  it('treats sources without a snapshot as clean', () => {
    const form = reactive({ name: 'a' })
    const { dirty } = useDirtyTracker({ form: () => form })

    form.name = 'b'

    expect(dirty.value).toBe(false)
  })

  // A captured form receives nested edits, reverses them, and later records a new
  // snapshot. Dirty state must follow JSON differences and clear after markClean.
  it('reports changes against the snapshot and clears after markClean', () => {
    const form = reactive({ name: 'a', tags: ['x'] })
    const { dirty, markClean } = useDirtyTracker({ form: () => form })
    markClean()

    form.tags.push('y')
    expect(dirty.value).toBe(true)

    form.tags.pop()
    expect(dirty.value).toBe(false)

    form.name = 'b'
    markClean()
    expect(dirty.value).toBe(false)
  })

  // One form changes after its baseline while another source records its first snapshot.
  // Capturing the second source must leave the first form's edits visible as dirty.
  it('keeps edits on one source when another source records its snapshot', () => {
    const form = reactive({ name: 'a' })
    const extra = ref(1)
    const { dirty, markClean } = useDirtyTracker({
      form: () => form,
      extra: () => extra.value,
    })
    markClean('form')

    form.name = 'b'
    extra.value = 2
    markClean('extra')

    expect(dirty.value).toBe(true)
  })
})
