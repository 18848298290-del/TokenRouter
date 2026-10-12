# File overview examples

Read these before drafting an overview. Each begins with a specific problem or rule and develops its consequence. Verify the stated assumptions; use the purpose or design role and minima from SKILL.md.

Reject introductions such as "This file parses ...", "The IP utilities provide ...", or a catalog of helpers. A renamed container or a rotation of stock opening phrases still fails. Start from the behavior, format, dependency, or failure that makes the implementation worth explaining. The first two sentences must establish a file-specific fact.

## Purpose overview: a refresh failure

Assume a failed refresh retains the previous catalog, and replacement entries are parsed and validated before publication. The reader needs to know which catalog remains available during that work.

```go
// Cached catalogs are reused until a replacement has been parsed and validated.
// Refresh errors leave the previous entries available, so readers can continue
// model lookup while a valid replacement is being prepared.
```

## Design overview: an account slot's lifetime

Assume requests queue per account, cancellation can race with admission, and release is idempotent. Explain the lifetime of a slot before describing the coordination that protects it.

```go
// A request waiting for an account slot can be cancelled without stopping other
// requests. Each account admits requests in arrival order and counts a request
// as active until its caller releases the slot after reading or closing the
// response. Routing may choose another eligible account before a request joins
// the queue.
//
// Queue membership and occupied slots change under the same mutex. Cancellation
// removes a waiter that has not been admitted. When cancellation races with a
// grant, the unused slot is returned before the next waiter is admitted.
// Release is idempotent because normal completion and cleanup may both call it.
```

## Purpose overview: the editable unit

Assume the editor replaces provider tables it generated and preserves other TOML content. Make the unit of replacement and the user's retained content clear.

```go
// TOML provider tables are replaced as complete units. The editor owns the
// tables it generated. Text outside those tables stays as written, including
// comments and ordering, so reconnecting a provider preserves the user's
// surrounding settings.
```

## Purpose overview: a dependency limitation

Assume plugins run under a runtime that accepts HTTP proxies but rejects SOCKS5, and the application supplies a loopback bridge for SOCKS5 selections.

```go
// The plugin runtime accepts HTTP proxies, while the configured upstream proxy
// uses SOCKS5. A loopback HTTP proxy bridges the two protocols and forwards
// plugin requests through that SOCKS5 server using the user's chosen connection.
```

## Design overview: valid rules and configured rules

Assume compilation skips invalid IP patterns but retains their original count, and the API-key allowlist check rejects unmatched addresses whenever that count is positive. Separate parsing behavior from the configuration decision it supports.

```go
// An IP rule names either one address or a CIDR range. CompileIPRules parses
// a list for repeated checks; MatchesPattern parses one rule for a single check.
//
// Compilation trims whitespace and skips invalid entries, but PatternCount
// includes every supplied entry. API-key allowlist checks use this count to
// distinguish an absent allowlist from a configured list with no valid rules.
// A configured allowlist with no valid rules rejects every address.
//
// Single-pattern matching and validation parse their inputs as supplied.
// NormalizeIP trims an address and removes its port when SplitHostPort succeeds.
```

## Purpose overview: the stored records and their index

Assume this file is a facade over a ledger and index implemented elsewhere. The JSONL ledger is authoritative, readers use immutable index blocks, and an append copies at most the unfinished final block. The facade needs a purpose overview; the component that implements publication and consistency owns the design explanation.

```go
// The JSONL ledger holds the request records, and its in-memory index can be
// rebuilt from them. Readers decode the blocks that overlap their requested
// time range, while appending a record copies at most the unfinished final block.
```

## Design overview: a value split across stream events

Assume the implementation restores known placeholders, holds a possible unfinished placeholder, and continues it only with later text from the same channel. It emits an unmatched tail literally when the stream ends.

```go
// A placeholder can arrive in pieces: one text event ends with {{TOKEN_ and
// the next begins with name}}. Replacing values one event at a time would
// leave the placeholder visible to the caller, even though joining the text
// first would have found it.
//
// The writer holds a trailing fragment that could begin a placeholder and
// continues it with later text from the same channel. Complete text can be
// emitted immediately. An unfinished fragment stays buffered until it can be
// matched or ruled out; when the stream ends, any unmatched tail is emitted
// literally so the caller receives all of the original text.
```

## Design overview: elapsed work and operation counts

Assume the implementation merges completed intervals, clips them to the request window, and counts each operation once. A mutex protects recording and snapshot creation; merging and formatting use the copied snapshot.

```go
// Concurrent calls can overlap, so adding their durations overstates how long
// the request spent waiting. Two calls that each take 10 milliseconds and
// overlap for 5 occupy 15 milliseconds together, while still counting as two
// operations. Elapsed work is the union of their intervals within the request
// window; operation counts remain additive.
//
// Recording continues while the response header is prepared. The collector
// copies counts and intervals under its mutex, then releases the mutex before
// merging intervals and formatting the header. This keeps one header's inputs
// consistent while allowing later observations to be recorded for subsequent
// snapshots.
```
