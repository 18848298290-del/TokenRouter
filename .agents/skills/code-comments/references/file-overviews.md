# Explaining the rules shared by a file

Start from the fact a reader needs before following the implementation. Explain the condition, consequence, or relationship that follows from it. Choose the subject from the code and its callers. Repeating a different introductory formula in every file would recreate the same problem.

The mandatory minima still apply: a purpose overview needs 30 words and 2 complete sentences; a design overview needs 80 words and 2 prose paragraphs. Select the role using SKILL.md's design criteria. Establish the problem and explain its consequences before adding secondary facts. Keep an existing explanation at its owning declaration when the overview can instead explain why the surrounding pieces work together.

## Choosing the opening subject

Choose a subject from the behavior verified in the implementation and its callers. The following situations illustrate how a concrete opening leads to an explanation; use a situation only when the code actually implements it.

| Opening subject | How the explanation develops |
| --- | --- |
| A conversation staying with the account that answered it | Explain cache reuse, when routing can choose again, and whether the choice survives a restart |
| One allowance represented by two records | Explain how duplicates affect totals, how the records are matched, and what a failed read means |
| Whole configuration tables as the unit of replacement | Identify which tables the editor owns and what surrounding content it preserves |
| Two components supporting different proxy protocols | Name the incompatibility and explain the adapter that connects them |
| Environment-variable references in settings | Follow a value from its stored syntax to the consumer's syntax and explain unsupported expansion |
| A persistent ledger and its disposable index | Establish which data can be rebuilt and how the index reduces append and read work |
| An update the computer refuses to run | Explain the startup check, recovery behavior, and conditions for a later retry |
| Downloading an update through a mirror | Explain the transport choice and how the download is checked against independently obtained metadata |

These subjects lead to different explanations because they answer different questions. Function names appear where they locate a step or rule. An overview does not need to enumerate every function to establish its purpose.

## Purpose overview: a refresh failure

Assume a failed refresh retains the previous catalog, and replacement entries are parsed and validated before publication. The reader needs to know which catalog remains available during that work.

```go
// Cached catalogs are reused until a replacement has been parsed and validated.
// Refresh errors leave the previous entries available, so readers can continue
// model lookup while a valid replacement is being prepared.
```

The opening establishes the availability rule. The next sentence explains its consequence for readers.

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

The paragraphs cover lifetime and coordination. Individual function comments can then state their callers' obligations without repeating this whole flow.

## Purpose overview: the editable unit

Assume the editor replaces provider tables it generated and preserves other TOML content. Make the unit of replacement and the user's retained content clear.

```go
// TOML provider tables are replaced as complete units. The editor owns the
// tables it generated. Text outside those tables stays as written, including
// comments and ordering, so reconnecting a provider preserves the user's
// surrounding settings.
```

The data format is the subject. The text explains the editing rule instead of listing parser and writer helpers.

## Purpose overview: a dependency limitation

Assume plugins run under a runtime that accepts HTTP proxies but rejects SOCKS5, and the application supplies a loopback bridge for SOCKS5 selections.

```go
// The plugin runtime accepts HTTP proxies, while the configured upstream proxy
// uses SOCKS5. A loopback HTTP proxy bridges the two protocols and forwards
// plugin requests through that SOCKS5 server using the user's chosen connection.
```

The limitation gives the bridge a reason to exist. Keep the names and behavior specific to the dependencies the implementation actually uses.

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

The count's consequence explains why it survives parsing. The final paragraph distinguishes the entry points whose whitespace handling differs.

## Purpose overview: the stored records and their index

Assume this file is a facade over a ledger and index implemented elsewhere. The JSONL ledger is authoritative, readers use immutable index blocks, and an append copies at most the unfinished final block. The facade needs a purpose overview; the component that implements publication and consistency owns the design explanation.

```go
// The JSONL ledger holds the request records, and its in-memory index can be
// rebuilt from them. Readers decode the blocks that overlap their requested
// time range, while appending a record copies at most the unfinished final block.
```

The opening identifies which data can be reconstructed. The second sentence explains the work the index saves.

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

The split example establishes why buffering exists. The following sentences explain the state that buffering preserves and how it ends. A catalog of the writer's methods would not convey this relationship.

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

The first paragraph explains what the numbers mean. The second follows the consequence of calculating them while new work is recorded. Entry-specific defaults and serialization details stay with their declarations.

## Rejected introductions

- `This file parses IP access rules ...` announces the file's contents. Start with the rule and the distinction the reader needs.
- `The IP utilities provide parsing and matching capabilities ...` replaces the container's name while keeping the same generic introduction.
- `Compile parses, Match matches, and Validate validates ...` inventories declarations. Explain the relationship that makes them worth describing together.
- `Rules use the corresponding standard library operation ...` leaves the operation and its significance unnamed. State the actual condition or consequence, or remove the sentence.
- Several overviews that differ only in a domain noun and a list of helper names fail the review even when each passes its word minimum.

Choose supported facts before writing the sentences. If an overview repeats a declaration's details to fill space, follow the callers or shared state to find the relationship the overview is meant to explain. Report missing evidence if it cannot be established; invented reasons cannot satisfy the minimum.
