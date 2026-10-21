// groupsModelsListCandidates.ts tracks asynchronous candidate loads for the create and edit
// group forms. Each request receives a monotonically increasing identifier, and each form mode
// stores its latest request separately. Requests from one form therefore do not invalidate a
// simultaneous request from the other form.
//
// The tracker copies the mode and group identity at registration so later caller mutation
// cannot rewrite the stored request. A result is current when both its request number and group
// ID match the latest registration for that mode. Consumers use this check before publishing
// candidates, errors, or loading completion.

// ModelsListCandidatesMode identifies the independently tracked create and edit group forms.
export type ModelsListCandidatesMode = 'create' | 'edit'

// ModelsListCandidatesRequest identifies a candidate query by form mode and group.
export interface ModelsListCandidatesRequest {
  mode: ModelsListCandidatesMode // selected editor mode
  // groupID identifies the candidate source group. The create form uses zero, and edit requests
  // use their selected saved group ID.
  groupID: number
}

// ModelsListCandidatesTracker registers queries and checks whether their results remain
// current. next stores the newest request per form mode, and isCurrent checks its sequence
// number and group identity before publication.
export interface ModelsListCandidatesTracker {
  // next registers the newest candidate query for its form mode. It returns a unique request
  // number and stores a copy of the request identity for later completion checks.
  next(request: ModelsListCandidatesRequest): number
  // isCurrent reports whether the request number and group match the latest mode request.
  isCurrent(requestID: number, request: ModelsListCandidatesRequest): boolean
}

// createModelsListCandidatesTracker creates independent latest-request slots for create and
// edit forms. Each registration advances a shared counter and stores a copy of the request in
// its mode slot. Completion checks compare that number and the requested group ID.
//
// Consumers retain the returned tracker across requests and check it before updating form
// state. Registering an edit request does not invalidate a create request, while a newer
// request in the same mode supersedes the earlier one.
export const createModelsListCandidatesTracker = (): ModelsListCandidatesTracker => {
  let currentRequestID = 0
  const currentByMode: Partial<
    Record<
      ModelsListCandidatesMode,
      {
        id: number // latest request sequence number
        request: ModelsListCandidatesRequest // copied candidate query identity
      }
    >
  > = {}

  return {
    // next registers the newest candidate query for its form mode. It returns a unique request
    // number and stores a copy of the request identity for later completion checks.
    next(request) {
      currentRequestID += 1
      // Copy the request identity into its mode slot so later caller mutation cannot change
      // which group the pending response is allowed to update.
      currentByMode[request.mode] = {
        id: currentRequestID,
        request: { ...request },
      }
      return currentRequestID
    },
    // isCurrent reports whether the request number and group match the latest mode request.
    isCurrent(requestID, request) {
      const current = currentByMode[request.mode]
      return current?.id === requestID && current.request.groupID === request.groupID
    },
  }
}
