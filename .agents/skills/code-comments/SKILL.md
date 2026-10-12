---
name: code-comments
description: "Write, revise, and review source-code comments and docstrings. Use whenever a coding task adds or changes comments, including API documentation, field notes, inline explanations, and test intent. Decide what needs a comment, choose its location and length, and verify its claims against the code."
---

# Code Comments

Apply these rules to every comment or docstring you add or edit. Help a future reader call, diagnose, or change the code correctly. Write comments and docstrings in English. Follow the repository's documentation format and tooling conventions; an explicit user preference takes precedence.

## Establish the facts

Read the implementation being described. Follow callers, tests, or the owning design reference when a claim depends on them. Match the depth of inspection to the claim: a unit label needs less investigation than a concurrency guarantee.

- Check normal behavior, relevant exceptions, defaults, ownership, and failure paths. Pay particular attention to claims such as "always," "never," "atomic," "thread-safe," and "exactly once."
- Distinguish what the function guarantees from what its caller must provide or do afterward.
- Use supported reasons and observed failure mechanisms. Invented incidents, benchmark numbers, vendor behavior, and plausible-sounding design history do not belong in comments.
- Describe behavior that exists. Keep proposals and change narration in the appropriate issue, PR, or design proposal.
- When code and an existing comment disagree, investigate the intended behavior. Report an unresolved conflict that affects the task; rewriting the comment cannot establish correctness.

## Decide whether to comment

Add a comment when it supplies information the intended reader needs:

- **Use and meaning:** a public API's purpose, a domain concept, return-value interpretation, units, identity, representation, or the meaning of a missing value.
- **Caller obligations:** shared or owned data, mutation, cleanup, cancellation, lock requirements, or valid call order.
- **Reasons and constraints:** a non-obvious choice, compatibility condition, failure mechanism, algorithm assumption, approximation, or accepted tradeoff.
- **Relationships:** rules spanning several fields, functions, processes, or systems that are expensive to reconstruct locally.
- **Test intent:** the scenario a fixture represents, the condition being reproduced, and the behavior an assertion protects.

A short purpose statement can help API readers who should not need to inspect the implementation. Explain both behavior and reasons where they are useful; assess each against the reader's needs.

Leave routine local code uncommented when its names and operations already communicate the needed information. Ordinary initialization, iteration, assignment, error propagation, and simple forwarding rarely need narration. Avoid repeating a shared rule at every use. If a confusing name or structure causes the difficulty, consider an authorized code improvement or flag it separately.

## Put information where it is needed

| Location | Information to put there | Starting length |
| --- | --- | --- |
| Package, module, or file overview | Purpose, responsibility, shared model, and rules spanning its implementation | A short paragraph; several focused paragraphs for a substantial shared model |
| Type or interface | What an instance represents and the rules its users or implementations rely on | One or two sentences; expand for a real contract |
| Function or method | Purpose, results, caller obligations, meaningful defaults, errors, and side effects | One or two sentences; add conditions or reasons as needed |
| Field, parameter, or constant | Meaning, unit, source, ownership, special values, or relationships | A short phrase or sentence; a preceding block for several related rules |
| Local block or branch | The condition, reason, invariant, or consequence that explains this operation | Usually one or two lines |
| End of a code line | A compact qualifier such as an encoding, unit, or special value | A short phrase; move longer explanations above the code |
| Test or fixture | Scenario, unusual setup, and observable behavior being protected | One to three sentences; let clear case names carry simple cases |

These are drafting defaults. Physical line counts depend on language and wrapping. Keep a required condition even when it makes a comment longer. A one-line implementation may need a paragraph when it encodes a rule shared across the system.

Give each fact a clear home. Keep API-facing guarantees visible at the declaration; put branch-specific reasons beside the branch. Move substantial cross-component explanations to the owning design reference and link to named symbols or stable sections. Reference source symbols rather than line numbers.

## Write direct sentences

- Start declaration comments with the symbol and its meaning or action: `Entry is ...`, `Read returns ...`, `Save writes ...`, `Ready reports whether ...`. Use the language's native documentation convention where it differs.
- Describe current behavior in the present tense. Use past tense for a verified event that explains a current constraint.
- Put the main behavior first, then the relevant condition or reason. Use `when`, `before`, `after`, `until`, and `unless` to state actual rules. Separate sentences when nested conditions become hard to follow.
- Use concrete actors and verbs: the caller releases, the reader waits, the parser returns, the file stores. Use passive wording when the object's state is the point and the actor is clear or irrelevant.
- At field and line-end positions, fragments such as `milliseconds`, `base64`, or `empty means unlimited` are sufficient. Use full sentences for explanations that must stand alone.
- Name objects again when `it`, `one`, or `this` could refer to several things. Prefer `reports whether` to awkward constructions such as `is whether`.
- State a constraint with its condition and practical consequence. A precise exclusion that prevents misuse is useful; unrelated assurances add noise.
- Replace praise and vague labels such as "robust handling," "special case," or "ensure correctness" with the behavior, trigger, or failure they refer to.

For a non-obvious choice, include the relevant parts of this explanation: the triggering condition, the behavior of a dependency, the chosen action, and what would fail without it. Use only the parts the reader needs. A relevant issue or test name can support the explanation; keep enough meaning locally for the comment to stand on its own.

## Review before finishing

Check the comments affected by the change:

1. Does each claim agree with the implementation, including the relevant exceptions?
2. What useful information would the intended reader lose if this comment disappeared?
3. Can that reader find the information at the point of use, without duplicate explanations drifting apart?
4. Are the units, defaults, ownership, and timing unambiguous where they matter?
5. Can any repetition or tangled sentence be removed while preserving the useful information?

Update or remove nearby comments made stale by the change. Preserve license notices, generated markers, compiler and lint directives, and documentation syntax that tools consume. Treat changes to those comments according to their effect on tooling or behavior.

Keep comment-only work scoped to comments. Run verification appropriate to any affected documentation tooling or behavior; ordinary prose edits do not need a new test suite or a separate report.

For worked examples and borderline decisions, read [references/examples.md](references/examples.md) when useful.
