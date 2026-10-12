# Reviewing information and placement

Each pair uses the stated implementation assumptions. Verify those assumptions before adopting its wording. Excerpts illustrate the named problem; surrounding declarations still need their required coverage. Keep the existing numerical minima while correcting the information or its location.

Start by reading the whole explanation. Identify the reader's question, the facts needed to answer it, and the decision the reader should understand afterward. A paragraph can meet every local requirement and still bury that decision in secondary facts.

## A paragraph with no main question

Rejected overview excerpt:

```go
// Timings use interval unions and additive counts. Context helpers are optional.
// The collector uses a mutex and copies snapshots. Headers have a size limit,
// and cache statuses have defaults. Dependency names are sorted before output.
```

These statements have no explanatory order. Adding sentences about more helpers would increase the reader's work. Under the assumptions in [the elapsed-work example](file-overviews.md#design-overview-elapsed-work-and-operation-counts), start with overlapping calls and what their measured duration means, then explain taking a snapshot while recording continues. Defaults and sorting belong at the relevant entry point unless they help answer that design question.

## A wrapper expanded into a design essay

Assume a buffer wrapper delegates to a library pool. Repeating that callers borrow arrays, finish scanning, return arrays, and stop accessing them in the overview, array type, getter, setter, and beside Get spreads one calling rule across the file.

Use the purpose overview to explain why these callers reuse storage and, when applicable, how initial storage differs from a scanner's token limit. Give the array type a compact definition. Put the borrowing obligation at the public methods, and reserve the local fallback explanation for why an unusable pool entry can occur. The [pool wrapper example](examples.md#a-wrapper-around-a-library-pool) meets the calling-rule minimum without reproducing the pool's implementation details.

## A local explanation that translates a condition

Assume a header builder has already added fixed metrics and an aggregate dependency metric. It then appends dependency details in sorted order until the size limit would be exceeded.

Rejected:

```go
// Stop before the next dependency exceeds the header limit.
if len(candidate) > maxHeaderLength {
    break
}
```

This passes the short operation minimum but repeats the comparison and break. It leaves the effect on the reported metrics unexplained.

Accepted:

```go
// Fixed metrics and the dependency total remain; this dependency and later
// details are omitted when the next entry would exceed the header limit.
if len(candidate) > maxHeaderLength {
    break
}
```

The added fact is which data survives partial output. A threshold comment can be longer than its minimum when that fact needs more words.

## Complete information crowded into one paragraph

Assume Acquire coordinates admission and cancellation in a queue, returns an idempotent release function, and uses zero to disable its limit.

Rejected:

```go
// Acquire waits for a slot in the account queue and returns a release function
// after the request reaches the front. A zero limit lets the request proceed
// immediately without occupying a slot. Cancellation removes a waiting request
// from the queue. If cancellation races with a grant, Acquire returns the unused
// slot before waking another waiter. Callers invoke release after the vendor
// response has been read or closed. Repeated release calls have no further effect
// on the queue or occupied slots.
```

The explanation reaches the design function minimum but mixes normal results, cancellation, and cleanup in one paragraph.

Accepted:

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

The information stays the same. Paragraphs let readers locate the rule for the situation they are handling.

## A long field explanation that misses the consumer's rule

Assume an event producer fills Component, and the Ops sink prefers a nonempty string in Fields["component"], then Component, then "app".

Rejected:

```go
// Component contains the event's source component name, which accompanies its
// message and other attributes when the event reaches the logging sink.
Component string
```

The explanation exceeds the field minimum but does not tell a producer which value the sink will use.

Accepted:

```go
// Component names the source. The Ops sink prefers a nonempty Fields["component"]
// string and uses "app" when both sources are empty.
Component string
```

Name the consumer to keep its precedence rule from becoming a promise about every sink. A zero value with special behavior likewise needs its actual effect, such as disabling a timeout or selecting a constructor default, wherever that rule is owned.

## An ownership promise wider than the copy

Assume CopyRecord copies the record's slices and map but retains its FinishedAt pointer. The function delegates the copying and needs to describe ownership to its callers.

Rejected:

```go
// CopyRecord returns an independent copy of the record and all of its data.
// Callers can freely modify the result without changing anything held by the original.
```

The word count passes. The ownership promise is false for the shared pointer.

Accepted:

```go
// CopyRecord copies the record's slices and timings map so callers can modify
// those collections independently. FinishedAt remains shared; callers must treat
// the pointed-to time as immutable.
```

Check each mutable part of a value before promising independence. A successful test of a copied slice provides no evidence about the retained pointer.

## The same explanation at two levels

Assume a catalog refresh loads and validates replacement entries, retaining the previous catalog on failure. Snapshot returns shared entries that callers must leave unchanged.

Rejected overview excerpt:

```go
// Refresh loads replacement entries and validates them. Snapshot returns the
// current entries. A failed refresh leaves the current entries unchanged.
```

Repeating these same sentences at Refresh and Snapshot would make the overview an inventory of declaration summaries.

Accepted purpose file overview:

```go
// Catalog readers keep using the published entries while replacement entries
// are loaded and validated. A refresh failure leaves the published catalog
// available, so concurrent lookups continue using the last successfully loaded
// set of entries.
```

Accepted Snapshot documentation, assuming it delegates storage access and has the stated calling rules:

```go
// Snapshot returns the currently published catalog entries, or an empty slice
// before the first successful load. The returned entries are shared, and callers
// must leave them unchanged.
```

The overview explains availability during replacement. The declaration explains the returned value, its empty case, and the caller's ownership obligation. These passages answer different questions without repeating the load-and-validate sequence.
