# Coverage and length examples

Each excerpt illustrates the named requirement under its stated implementation assumptions. Check those assumptions against the code before using similar wording. Comments must meet both the numerical minimum and the required information.

Use [review-examples.md](review-examples.md) during review to compare comments that pass word counts but fail information, paragraph, placement, or accuracy requirements.

## File overviews

Read [file-overviews.md](file-overviews.md) for purpose and design examples organized around domain rules, lifecycle, formats, dependency limitations, and data ownership. Each starts from the particular behavior being explained.

## Definition of a private function

Requirement: at least 8 words and 1 sentence. Assume `label` holds the provider's configured display label.

```go
// name returns the display label configured for this provider.
func (p Provider) name() string { return p.label }
```

This function needs a definition and has no additional calling rule or design relationship. Private visibility leaves the documentation requirement in force.

## Calling rules and ownership

Requirement: at least 25 words and 2 sentences. Assume `Read` returns a shared cached byte slice and returns `ErrNotExist` for a missing file.

```go
// Read returns the cached contents of path, or ErrNotExist when the file is missing.
// The returned bytes are shared. Callers must copy them before modifying them.
```

The comment documents the result, the missing-file error, and the caller's copying obligation. A change to return independent copies would require a matching documentation update.

## Design function

Requirement: at least 60 words and 3 sentences. Assume `Acquire` coordinates queue admission and cancellation, returns an idempotent release function, and uses zero to disable the limit.

```go
// Acquire waits for a slot in the account queue and returns a release function
// after the request reaches the front. A zero limit lets the request proceed
// immediately without occupying a slot.
//
// Cancellation removes a waiting request from the queue. If cancellation races
// with a grant, Acquire returns the unused slot before waking another waiter.
//
// Callers invoke release after the vendor response has been read or closed.
// Repeated release calls have no further effect on the queue or occupied slots.
```

The function owns the interaction between admission and cancellation, which requires a design explanation regardless of implementation length. Its explanation includes the normal path, zero limit, cancellation race, resource lifetime, and repeated cleanup behavior.

## Compact fields and independent constraints

Requirements: at least 8 words for the named type, 3 words for a compact field meaning, and 1 word for an encoding label. Assume the translator consumes base64 image bytes.

```go
// Image is the encoded image sent in an upstream request.
type Image struct {
    MediaType string // image MIME type
    Data      string // base64
}
```

A literal encoding needs no paragraph. An independent ownership or precedence rule uses a preceding block of at least 12 words. Assume the sink reads Fields asynchronously after accepting an event:

```go
// Fields holds event attributes. After enqueueing the event, callers must leave
// this map and its mutable values unchanged until the sink finishes reading them.
Fields map[string]any
```

Verify the consumer's actual ownership rules before making this promise. If only one sink behaves this way, name it.

## Shared measurement rules

Requirement: at least 20 words for the shared rule, plus 3 words for each compact field meaning. Assume both durations start at request arrival and use zero for an absent measurement.

```go
// FirstContent and FirstText measure elapsed time from request arrival to their
// respective events. Zero means the event has not been observed for this request.
FirstContent time.Duration // first text, reasoning, or tool-call content
FirstText    time.Duration // first text content
```

The group states the common start and zero value once. Each field identifies its own endpoint. The group minimum replaces separate 12-word explanations of the same rule.

## Consumer precedence

Requirement: at least 12 words for the field's independent rule. Assume the named sink prefers a nonempty string attribute and defaults to an application component.

```go
// Component names the source. The Ops sink prefers a nonempty Fields["component"]
// string and uses "app" when both sources are empty.
Component string
```

The consumer's precedence is more useful than expanding "component name" into a longer sentence. Confirm conversions and whitespace handling in the consumer before adopting this wording.

## Constant with a failure reason

Requirement: at least 12 words for the constant group. Assume the sender counts dispatches, the receiver counts arrivals, and connection setup delays the first arrival.

```go
const (
    // ArrivalSlack accounts for connection setup delaying the first arrival.
    // Counting dispatch times alone can admit another request inside the receiver's window.
    ArrivalSlack = time.Second
)
```

The comment explains the margin's purpose. It makes no guarantee that one second covers every network delay; such a guarantee would need supporting behavior and evidence.

## Operation that handles a race

Requirement: at least 15 words. Assume the surrounding implementation establishes that a cancelled waiter already received a slot in its account's queue.

```go
// Cancellation arrived after this waiter received a slot. Return its unused slot
// before waking the next request in the same account queue.
lane.busy--
lane.grant()
```

This explains why the decrement belongs here and why it precedes admission of the next waiter. Ordinary increments and assignments elsewhere need no line-by-line narration.

## A fixed storage threshold

Requirement: at least 6 words and 1 sentence. Assume a recorder has already incremented its total count and now caps only the stored intervals.

```go
// Keep counting after interval storage reaches its cap.
if len(intervals) < maxIntervals {
    intervals = append(intervals, next)
}
```

If this branch also coordinates concurrent state or handles a failure, it uses the 15-word operation tier.

## Parameter defaults and repeated facts

Assume the constructor uses a fixed default for nonpositive durations. Document the default at the constructor and the field's role beside the field:

```go
// NewCache constructs an empty cache using ttl as the lifetime of each stored
// entry. Nonpositive durations select thirty seconds. Expiration is checked
// during cache reads.
func NewCache(ttl time.Duration) *Cache {
    if ttl <= 0 {
        ttl = 30 * time.Second
    }
    return &Cache{ttl: ttl}
}
```

```go
ttl time.Duration // stored entry lifetime
```

The default branch needs no local paraphrase. An error fallback or concurrency mechanism would require its own explanation at the operation.

## A private helper with a branch

Requirement: the calling-rule function minimum of 25 words and 2 sentences. Assume the writer has an optional HTTP flusher.

```go
// flush sends pending output through the writer's optional flusher.
// Writers without a flusher keep their existing buffering behavior, so callers
// can invoke this helper for either kind of writer.
func (w *writer) flush() {
    if w.flusher != nil {
        w.flusher.Flush()
    }
}
```

The optional flusher changes the caller-visible effect, so the calling-rule tier applies. The branch count has no role in this classification. The function has documentation; its nil check and method call remain free of inline narration.

## A wrapper around a library pool

Requirement: the calling-rule minimum of 25 words and 2 sentences. Assume Borrow delegates synchronization to a library pool and replaces unusable entries with newly allocated buffers.

```go
// Borrow returns a reusable buffer whose contents may come from an earlier call.
// Return it with Release after its consumer finishes, then stop accessing its
// storage. An unusable pool entry produces a fresh buffer.
```

Borrow has an ownership obligation but implements no custom coordination. A type or file description can explain why buffers are pooled without retelling this borrowing contract. A local note about the unusable-entry branch can identify who can put foreign values into the pool, when verified; the Get call itself needs no synchronization tutorial.

## A decision that cleanup must preserve

Requirement: at least 15 words for the local resource-handling explanation. Assume processes lock an opened file and release the OS lock by closing its descriptor; the path remains after release.

```go
// Keep the lock file: removing it lets another process create and lock a new
// file while an existing holder still has the old one open.
file.Close()
```

The explanation identifies the consequence of adding a seemingly harmless removal. A comment saying only that Close releases a descriptor would miss the decision a future edit could break. The explanation requires no invented incident or historical claim.

## Test entry point and unusual setup

Requirement: at least 20 words and 2 sentences for the test entry. Assume the test delays the first arrival and checks the receiver's request spacing.

```go
// TestArrivalWindow models a slow first connection followed by reused connections.
// The vendor must still receive no more than its limit in a rolling window,
// even when the first request arrives later than its dispatch time suggests.
```

The setup explanation immediately before the injected delay names the condition being reproduced:

```go
// Delay the first arrival to model connection setup while the later requests
// reuse an established connection and reach the receiver sooner.
vendor.firstDelay = 150 * time.Millisecond
```

The test name does not replace its required explanation. Routine setup can remain uncommented.

## A guarantee with an exception

Requirement: at least 25 words and 2 sentences for a calling-rule function. Assume `Write` delegates replacement and recovery to another component. That component atomically replaces ordinary files, but writes hard-linked files in place after preserving the new contents in a temporary file.

```go
// Write replaces path through a temporary file and rename. Hard-linked files
// are written in place to preserve their links. That path can leave a partial
// write after a crash, with the complete new contents retained in a temporary file.
```

The exception belongs with the guarantee callers read. The component that implements the hard-link handling owns its design explanation.

## Failing examples

Each of these fails a specific requirement:

- An uncommented private getter fails declaration coverage.
- `// Read reads the file.` fails the calling-rule function minimum and leaves ownership and failure behavior unexplained.
- `// This file contains helpers.` fails the file minimum and says nothing about the helpers' shared purpose.
- An overview beginning `This file parses ...` fails the opening rule even if it meets the length minimum. Starting it with `This module provides ...` or substituting the filename leaves the same problem.
- A 100-word overview written as one paragraph fails the design overview's paragraph minimum.
- Splitting eight words across four lines still counts as eight words.
- A comment repeating "safe and reliable" until it reaches the word minimum fails the information and accuracy checks.
- A `// state fields` label supplies neither a shared rule nor individual field meanings.
- Separate 12-word comments that repeat one publication rule for every result field fail grouping and repetition checks.
- Adding a paragraph to `Data string // base64` without another verified rule fails compact-field placement.
- Saying "preserves concrete types" after checking only an ordinary pointer leaves named pointer and interface cases unverified.
- Calling an optional port "required" contradicts code that checks it only when supplied.
- A file overview moved into a `const` block by declaration sorting fails placement.

## Checking the scope of an existing-file edit

When a task changes one function in an existing production file, classify the whole file and check its overview. Classify the changed function, document its calling rules, and explain the specified operations inside it. Update any caller documentation made stale by the change.

Other functions with unchanged behavior stay outside that task's comment work. A dedicated comment audit can select the whole file. Review concurrent edits against the task's own changes so another agent's work is not mistaken for missing work in this task.
