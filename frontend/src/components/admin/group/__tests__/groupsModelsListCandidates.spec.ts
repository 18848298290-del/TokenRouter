import { describe, expect, it } from 'vitest'

import { createModelsListCandidatesTracker } from '../groupsModelsListCandidates'

// groupsModelsListCandidates.spec.ts checks latest-request tracking for the create and edit
// model-list forms. Request numbers and group identities reject superseded responses, while
// independent mode slots let both editors load their candidates without invalidating each
// other.

describe('groupsModelsListCandidates', () => {
  // A second candidate request starts before the first request completes. The tracker rejects
  // the earlier request and accepts the latest registration for that form mode.
  it('rejects stale candidate responses after a newer platform request starts', () => {
    const tracker = createModelsListCandidatesTracker()
    const first = {
      mode: 'create' as const,
      groupID: 0,
      platform: 'openai' as const,
    }
    const second = {
      mode: 'create' as const,
      groupID: 0,
      platform: 'anthropic' as const,
    }

    const firstID = tracker.next(first)
    const secondID = tracker.next(second)

    expect(tracker.isCurrent(firstID, first)).toBe(false)
    expect(tracker.isCurrent(secondID, second)).toBe(true)
  })

  // The edit form changes from one group to another while candidate work is pending.
  // Current-request checks reject the previous group identity even within the same form mode.
  it('rejects responses for a previous edit group even with the same platform', () => {
    const tracker = createModelsListCandidatesTracker()
    const first = {
      mode: 'edit' as const,
      groupID: 10,
      platform: 'openai' as const,
    }
    const second = {
      mode: 'edit' as const,
      groupID: 11,
      platform: 'openai' as const,
    }

    const firstID = tracker.next(first)
    tracker.next(second)

    expect(tracker.isCurrent(firstID, first)).toBe(false)
  })

  // Create and edit forms register candidate requests at the same time. Each mode retains its
  // own latest identity so either response can remain current independently.
  it('tracks create and edit requests independently', () => {
    const tracker = createModelsListCandidatesTracker()
    const editRequest = {
      mode: 'edit' as const,
      groupID: 10,
      platform: 'openai' as const,
    }
    const createRequest = {
      mode: 'create' as const,
      groupID: 0,
      platform: 'anthropic' as const,
    }

    const editID = tracker.next(editRequest)
    tracker.next(createRequest)

    expect(tracker.isCurrent(editID, editRequest)).toBe(true)
  })
})
