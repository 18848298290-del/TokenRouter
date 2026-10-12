# Comment decisions in practice

These examples illustrate writing choices under the stated assumptions. Verify those assumptions before using similar comments in another codebase. The principles apply across languages; adapt syntax and documentation conventions to the repository.

## Explain ownership at the declaration

Assume `Read` returns a shared cached byte slice and represents a missing file as `ErrNotExist`.

```go
// Read returns the cached contents of path, or ErrNotExist when the file is missing.
// The returned bytes are shared. Callers must copy them before modifying them.
```

The signature alone cannot tell the caller whether the bytes are safe to modify. This information belongs at the function, where a caller will look for it.

If the implementation returns an independent copy, the ownership statement must change with it.

## Use a short qualifier for a field

```go
type Image struct {
    MediaType string
    Data      string // base64-encoded image bytes
}
```

The type and field names establish the subject. The comment supplies the encoding. A full sentence repeating the field name would add little.

## Give similar measurements distinct meanings

Assume both measurements start when the gateway receives the request:

```go
// FirstContent is the elapsed time to the first text, reasoning, or tool-call
// content. Zero means no measurement was recorded.
FirstContent time.Duration

// FirstText is the elapsed time to the first text content.
// Zero means no text measurement was recorded.
FirstText time.Duration
```

The comments define start, endpoint, and missing-value behavior. A comment saying only "latency" would leave those choices unresolved.

## Explain a local race where it is handled

Assume the preceding code establishes that a cancelled waiter has already received a slot:

```go
// Cancellation raced with the grant. Give the unused slot to the next waiter.
lane.busy--
lane.grant()
```

The comment explains why the decrement belongs here. "Decrease busy and grant" would only narrate the statements.

## Keep a reason next to an unusual constant

Assume the sender counts requests at dispatch and the receiver counts arrivals, with a verified delay on a new connection:

```go
// ArrivalSlack accounts for connection setup delaying the first arrival.
// Counting dispatch times alone can admit another request inside the receiver's window.
const ArrivalSlack = time.Second
```

This explains the purpose of the margin. It does not establish that one second covers every network delay. A stronger guarantee would need a stronger mechanism and evidence.

## Explain a short implementation when its rule spans the system

Assume streaming requests each reserve one window and RPCs have a separate allowance:

```go
// streamLimit is the number of streams whose windows fit in the stream budget.
// RPCs use a separate allowance that streaming requests cannot consume.
func streamLimit() int { return streamBudget / streamWindow }
```

The division is obvious. The reservation policy needs explanation even though the implementation is one line.

## Leave routine local code readable on its own

```go
func (w *writer) flush() {
    if w.flusher != nil {
        w.flusher.Flush()
    }
}
```

For an internal helper with no additional guarantees, the name and body are sufficient. Comments such as "check for nil" and "flush the writer" would duplicate the code.

A public wrapper may still benefit from a short purpose statement for documentation readers. Add any distinct flushing or ownership guarantee only when the wrapper actually provides it.

## Explain a test's unusual setup

Assume a test delays the first request to reproduce a difference between dispatch and arrival times:

```go
// Delay the first arrival to model connection setup; later requests reuse the connection.
vendor.firstDelay = 150 * time.Millisecond
```

The comment tells a maintainer why this delay is part of the scenario. Routine setup and self-explanatory assertions can remain uncommented.

## State a guarantee with its exception

Assume a writer normally replaces a file atomically, but writes hard-linked files in place to preserve their shared identity:

```go
// Write replaces path through a temporary file and rename. Hard-linked files
// are written in place to preserve their links; that path can leave a partial
// write after a crash, with the complete new contents retained in a temporary file.
```

An unconditional promise of atomic replacement would mislead callers. Put the exception where they see the guarantee, then explain the mechanics near the special branch if needed.

## Split an overloaded comment by responsibility

When a function comment explains the return value, every branch, a cross-process protocol, and several past fixes:

- Keep the caller-facing behavior and obligations above the function.
- Place branch-specific failure reasons beside the relevant operations.
- Put the shared protocol in its owning design reference and link to it.
- Keep historical evidence only where it explains a constraint that still applies.

An accurate long comment may be necessary. Split it when its topics have different readers or different places where they are maintained.
