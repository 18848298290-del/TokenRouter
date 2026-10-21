# Frontend comment coverage

Apply the coverage, explanatory roles, minimum lengths, and factual checks from
`SKILL.md` to handwritten frontend code and tests. This reference describes their
placement in Vue, TypeScript, and browser assets.

## Files and declarations

- In a Vue SFC, place the filename-led overview after script imports and before
  declarations. A template-only component uses an HTML comment before its root
  template. A component with script and style blocks needs one file overview;
  explain independent style constraints next to the affected declarations.
- Preserve mandatory directives, imports, licenses, and upstream attribution.
  JavaScript and TypeScript file overviews follow imports. CSS overviews follow
  mandatory charset/import directives; keep rule and layer order unchanged.
- Document named functions, named arrow-function bindings, composables, stores,
  interfaces, aliases, and classes. Apply the function tiers to their behavior.
  A composable using `watch` does not automatically require the design tier;
  custom ordering that decides which response or resource wins does.
- Props, emits payloads, DTO properties, local draft types, and named test
  fixture types follow the field rules. Explain units, nullability, omission,
  clearing values, mutable ownership, and conditional use at their owning field
  or shared field group. A UI draft and an HTTP request can assign different
  meanings to an empty string; document each verified consumer rule.
- Group adjacent top-level reactive state or constants by purpose. Explain the
  group's ownership and use once, then add individual explanations where a
  member has its own lifecycle, precedence, or conditional rule.

## Reactive work and resources

Document caller obligations for component setup, injected contexts, initialization
order, and cleanup. Explain request generations, stale-result rejection,
AbortController ownership, timers, event listeners, Blob URLs, IndexedDB writes,
SDK teardown, cache invalidation, and persistence ordering at the function that
owns them. Local comments explain why the specific ordering or exception is
needed. Preserve the distinction between hidden panels and unmounted panels.

For coordinated operations, verify both completion and interruption: a late
response, component unmount, changed user or provider identity, failed persistence,
and a retry can follow different paths. State guarantees supported by those paths.

## Tests and content

Handwritten test files also use a filename-led overview explaining the behavior
and fixture boundary covered by the file. Place it after imports and before
suite setup, keeping individual test-entry explanations beside their tests.

Each `it`, `test`, and parameterized test entry has a nearby explanation meeting
the test-entry minimum: the situation and observable behavior it protects. A
test name or a `describe` label does not replace that explanation. Named helpers
use the function tiers; anonymous test callbacks need no separate duplicated
function documentation beyond the test-entry explanation.

Keep user-facing strings, locale values, test input, and quoted source fixtures
in their intended language. A comment inside a source-code string is test input,
not a comment on the surrounding test. Retain legal notices and tool-consumed
directives; explanatory prose added after a directive follows the English rule.
JSON values and other formats without comments put their explanation in the
handwritten loader, schema, or validator that owns the data.

## Verification

The parser-based frontend check detects comment-language and filename-overview
violations. It does not determine whether a declaration needs the definition,
calling-rule, or design tier, whether an explanation is accurate, or whether all
required fields and local operations are covered. Review those requirements using
the file's declarations, callers, tests, and the full skill checklist. Record
unreviewed files separately from files that passed the mechanical check.
