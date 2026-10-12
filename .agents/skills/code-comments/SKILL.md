---
name: code-comments
description: "Write English comments that explain behavior, design choices, and consequences when writing, modifying, or reviewing handwritten code. Require file, declaration, field, and operation coverage even in initially uncommented code. Select mandatory lengths by explanatory role, ground claims in the implementation, and review the whole file for clarity."
---

# Code Comments

Apply this skill whenever writing, modifying, or reviewing handwritten code, including changes that initially contain no comments. Write comments and docstrings in English. Follow the repository's documentation syntax and tooling conventions. An explicit user instruction takes precedence.

## Scope of each task

- In a new file, cover every declaration and operation required below.
- In an existing file, cover declarations and operations added or changed by the task, and update their affected comments. A change to a function body brings that function's documentation and specified operations into scope. A change to a type brings its documentation and added or changed fields or members into scope.
- Every production file edited by the task needs a file overview. Classify that file using its complete contents, including declarations outside the task's edit.
- Include callers and related explanations whose documented behavior the task changes. Leave unrelated declarations and concurrent work to their own tasks. A requested comment audit covers its selected files or symbols.
- Apply the rules to public and private declarations, including constructors, accessors, interface methods, `init`, `main`, `Error`, and `Unwrap`.
- Use the matching constructs in other languages. In formats without comment syntax, put the required explanation at the owning handwritten loader, generator, or schema; keep the data format valid.

## Establish the facts

Read the implementation being described. Follow callers, tests, or the owning design reference when a claim depends on them. Match the depth of inspection to the claim: a unit label needs less investigation than a concurrency guarantee.

- Check normal behavior, relevant exceptions, defaults, ownership, and failure paths. Pay particular attention to claims such as "always," "never," "atomic," "thread-safe," and "exactly once."
- Distinguish what the function guarantees from what its caller must provide or do afterward.
- For fields, inspect their writers and readers for zero values, units, encoding, override order, and mutable ownership. Name a particular consumer when a rule belongs to that consumer; do not turn its behavior into a guarantee of the whole type.
- For claims of preserved types, independent copies, atomic updates, or cancellation guarantees, check the exceptional branches and delegated operations. Use a small executable probe when reflection, aliasing, or an interleaving leaves the claim unresolved by inspection. Existing tests count as evidence only for cases they exercise. Qualify a claim to its verified scope; report unresolved behavior instead of promising it.
- Use supported reasons and observed failure mechanisms. Invented incidents, benchmark numbers, vendor behavior, and plausible-sounding design history do not belong in comments.
- Describe behavior that exists. Keep proposals and change narration in the appropriate issue, PR, or design proposal.
- When code and an existing comment disagree, investigate the intended behavior. Report an unresolved conflict that affects the task; rewriting the comment cannot establish correctness.

## Explain the problem the reader is trying to understand

Before drafting a long comment, identify the question that makes the code worth explaining: why an identifier carries extra data, why a file survives cleanup, how overlapping work affects a total, or what happens when a stream ends halfway through a value. Establish the concrete situation, follow its consequence, and explain the chosen handling. Each sentence should advance that explanation. A list of defaults, helpers, locks, and return values does not establish a main idea by itself.

- Explain design choices that a later maintainer could accidentally undo. When verified, state why deleting, reordering, combining, or replacing an operation would change behavior. Keep the relevant condition and consequence with the decision. Historical reports can support that explanation, but the comment must remain understandable without opening the report.
- Connect technical rules to their effect on the caller or user: whether a retry occurs, a setting survives an edit, a cost is counted twice, or a request reaches a different provider. Use the actual consumer and behavior. Generic claims about reliability or usability add no explanation.
- For a rule involving split input, encoding, overlapping quantities, or easily confused identities, include a small representative input or timeline when the prose otherwise requires the reader to reconstruct the case. State the assumed conditions and resulting behavior. A hypothetical example must illustrate verified behavior; it must not pose as production history.
- Keep one main question per paragraph. Develop related conditions together and start another paragraph when the reader moves to a different question. Sentences need not follow the same sequence in every file.
- Give the most space to design reasons, interactions, and traps. A definition, unit label, or routine wrapper should remain compact once its own required information is complete. Preserve caller-facing obligations without repeating their full background at every layer.

## Mandatory coverage and minimum lengths

Every in-scope item in this table requires a comment. Meet both its numerical minimum and its required information. Private visibility or a descriptive name cannot waive coverage. Select the tier from the explanation's role under the rules below; implementation size alone does not raise a tier.

| Item | Minimum | Required information |
| --- | --- | --- |
| Purpose file overview | 30 words and 2 complete sentences | The problem or domain rule and how the file's pieces address it |
| Design file overview | 80 words and 2 prose paragraphs | The problem, the consequential design choice, and the interacting rules that explain the solution |
| Definition function or method | 8 words and 1 complete sentence | Purpose and result or effect, including the consumer when that establishes its role |
| Calling-rule function or method | 25 words and 2 complete sentences | Purpose and the applicable calling rules listed below |
| Design function or method | 60 words and 3 complete sentences | Purpose, applicable calling rules, and the condition and consequence that explain its design |
| Named type or interface | 8 words and 1 complete sentence | What a value represents or what its implementations do; state additional shared rules at the type or field group that owns them |
| Compact field explanation | 3 words; a unit or encoding label may use 1 word | Meaning, source, unit, encoding, or a literal special value; use a line-end comment |
| Field with a conditional rule, ownership obligation, precedence, or lifecycle constraint | 12 words | Meaning and the condition with its effect; use a preceding block |
| Adjacent fields governed by one shared rule | 20 words for the shared explanation, plus a compact explanation for each field | Name every participating field and state their relationship once; use a preceding group block and line-end meanings |
| Each purpose group of top-level constants or variables | 12 words and 1 complete sentence | What the values govern and how the group is used |
| Local explanation of a fixed threshold, unit conversion, truncation, or approximation | 6 words and 1 sentence | What the limit or calculation means and its consequence |
| Other specified operation below | 15 words | Its triggering condition and the reason for the handling |
| Test, benchmark, or fuzz entry point | 20 words and 2 complete sentences | The situation being reproduced and the observable behavior being protected |

Test entry points use the test minimum; test helpers and interface methods use the function tiers. Named test types and their fields follow the same declaration rules. Cover enum values' individual meanings within the group or beside each value.

Function documentation must state the applicable calling rules: inputs and meaningful defaults; returned values and special results; state changes; ownership and cleanup; cancellation; errors and recovery; ordering and locking requirements. Explain a special result when it changes the caller's decision or permitted use. Routine propagation of an error need not enumerate every zero-valued companion result when callers discard them on error. Keep facts supported by the implementation and its consumers; omit empty "not applicable" sections.

Classify fields in this order: a shared rule covering adjacent fields uses the group tier; an independent conditional, ownership, precedence, or lifecycle rule uses the preceding-block tier; all remaining fields use the compact tier. A unit, encoding, enum label, or literal special value alone stays compact. If a field also has an independent rule, document that rule in a preceding block. Count the shared rule once for the group, with each field's identity in its line-end explanation. A label such as "state fields" supplies no shared rule.

### Select tiers by the explanation required

A design explanation is required where the code itself implements any of these relationships:

- Coordination between independently progressing work, where custom state and ordering decide which result, resource, or cancellation wins.
- A transformation that must retain information or identity across split input, round trips, or incompatible representations, with special handling to prevent loss or misassociation.
- Persistent updates whose ordering, recovery, or rollback keeps multiple steps consistent.
- A calculation or selection rule that combines observations or states whose interaction changes the result, such as overlapping durations or distinguishing absent configuration from configured entries that all failed validation.

Apply the design function tier when the function implements one of these relationships. Explain its relevant decision at that function; put the shared background at the file or type. Calling an established library operation, acquiring a pooled object, or delegating to a helper that owns the design does not by itself require this tier. A function's number of lines, branches, or imported mechanisms cannot promote it.

Otherwise apply the calling-rule tier when the function has any meaningful default, special input or result, ownership or cleanup obligation, cancellation behavior, error recovery, side effect whose ordering matters, or caller synchronization requirement. All remaining functions use the definition tier. Interface methods use these same criteria for the behavior they require of implementations.

A file uses the design overview tier when it owns a relationship listed above, including a design distributed among its declarations. Other files use the purpose overview tier. A facade that delegates a design describes its role and caller-visible behavior without reproducing the implementation's design explanation.

For example, a pooled-buffer wrapper with a nil/type fallback uses the calling-rule tier; the pool's synchronization is provided by the library. Its file needs a purpose overview. A queue that resolves a race between admission and cancellation owns that coordination and uses the design tiers even when its implementation is short.

### Organize function documentation

Start with the function's purpose and result. Develop the most consequential calling rule next, then the conditions and consequences needed to understand it. Group related defaults, errors, and lifecycle obligations together. Separate paragraphs when a design function covers independent questions. Use concrete special results instead of phrases such as "empty success" when the distinction matters to callers. Describe internal phases only when they explain a calling rule or design choice.

### Counting length

Count whitespace-separated English words after stripping comment delimiters and Markdown decoration. Count identifiers and hyphenated words as one word. Count prose and explanatory list items, including a small example explained in a sentence; exclude headings, bare reference links, code samples, licenses, tool directives, and `@project-doc` markers. Count complete prose sentences separately; a wrapped line or list label is not a sentence. Separate paragraphs with a blank comment line.

The table sets hard lower bounds for the selected role. Keep additional supported information when needed, and stop once the explanation is complete. Aim to wrap standalone prose near 80 columns. Repeated statements, generic filler, and line-by-line narration do not satisfy the information requirement. If a comment needs repetition to reach its minimum, recheck the role against the rules above and inspect the facts it requires; never invent an extra constraint to justify a longer tier.

## File overviews and placement

Keep package documentation separate from file overviews. Follow the repository's package-documentation requirement. In TokenRouter, packages with at least 15 handwritten source files require `doc.go`, with the package purpose, reading entry points, and file groups. That structure satisfies `doc.go`'s file-level documentation requirement.

Place a Go file overview after all imports and before the first other declaration. With no imports, place it after the `package` clause. Separate it from declaration documentation with a blank line. In other languages, use the corresponding location after mandatory module directives and imports.

A purpose overview establishes the domain problem or behavior and how the relevant pieces work together. A design overview develops the design question and follows the conditions and consequences that explain its solution. Bring in failure recovery, concurrency, ordering, or compatibility where they advance that explanation.

### Start with the subject being explained

- Make the opening assertion about a domain object, behavior, data format, dependency limitation, or concrete failure condition. The first two sentences must establish a file-specific fact beyond the filename and a list of declarations.
- File overviews must not open with `This file`, `This module`, `This code`, `The following code`, or equivalent descriptions of the file or module. Replacing these with a filename or phrases such as "the IP utilities" still fails when the sentence merely announces what the code contains or provides. A domain label such as `Affinity:` is acceptable when followed immediately by the actual rule.
- Follow the opening fact with the condition, reason, consequence, or next phase it leads to. Let that relationship determine paragraph order. Use a list for a sequence or set of cases when it helps the reader follow the rule; retain the required prose paragraphs for a design overview.
- Mention a function when its role in that relationship matters, such as one function producing the state another consumes. A catalog of function names and their individual jobs is insufficient as an overview.
- Put shared reasons and relationships in the overview, calling rules at declarations, and local handling reasons beside the operation. Preserve necessary caller-facing guarantees, but give each layer its own explanation instead of expanding the same fact repeatedly to reach its word minimum.
- Reach the length minimum with verified behavior, conditions, consequences, or relationships. Phrases such as "supports processing," "uses the corresponding standard library operation," and "handles the relevant cases" add no explanation by themselves.
- Compare overviews written in the same task. Reject repeated introductions that differ only in filenames or nouns. Different subjects can share ordinary grammar; changing synonyms or cycling through a list of opening phrases does not improve the explanation.

Before drafting a file overview, read or reuse [references/file-overviews.md](references/file-overviews.md). Its examples show different facts to start from; each example's assumptions require verification in the code being documented.

### Place supporting explanations

Put declaration comments immediately before their declarations. Apply the field tiers above to select line-end, preceding, or group placement. A compact explanation that exceeds the repository's line width can move immediately above its field while keeping the compact minimum. Put an operation's reason immediately before the relevant block. Explain a coordinated sequence once at its start, naming the operations it covers; separate mechanisms and separate code paths need their own explanations.

Place each fact according to the reader's question:

| Information | Primary location | What another location must add |
| --- | --- | --- |
| Defaults, special inputs, return values, caller obligations | Function documentation | A local explanation identifies the mechanism or failure consequence at that operation |
| A lock, publication order, or ownership rule shared by fields | Type or adjacent field group | Each field identifies its own value; the operation explains why its ordering matters |
| A race, recovery choice, or sequencing requirement at one operation | Immediately before that operation | Function documentation states the caller-visible result or obligation |
| Relationships among entry points or shared phases | File overview | Each declaration states its own calling rules |

Caller-facing guarantees must remain available at the function declaration. When a fact appears in another location, identify the additional information supplied there. If both passages answer the same question with the same fact, keep the full explanation at its primary location and revise the other passage to cover its own required information. Synonyms and extra words do not create an additional explanation. A field comment can name the constructor that defines a default without repeating its numeric value.

Keep caller-facing guarantees at the declaration even when the owning design reference holds their background. Link substantial cross-component explanations to named symbols or stable sections. Bare links cannot replace required local explanations or count toward their minimum length.

After formatting or declaration reordering, inspect file overviews and declaration comments again. TokenRouter's `tools/declorder` can move detached comments with declarations. Restore a displaced overview to the required position and verify the formatted result before reporting completion.

## Operations that require local explanations

For each in-scope occurrence, explain the condition and the reason for:

- Retry, backoff, or fallback selection.
- Suppressing an error, or converting a failed operation to an empty value or default result.
- Transaction commit, rollback, or compensation ordering.
- Coordination of goroutines, channels, cancellation, and closing resources.
- Cache invalidation, shared-object mutation under synchronization, and persistence ordering.
- Protocol compatibility transformations or exceptions.
- Thresholds, tolerances, truncation, or approximate calculations.

Ordinary parameter defaults, nil guards that implement the documented input contract, initialization, assignment, iteration, and direct error propagation use their declaration documentation. Routine use of a library-provided pool, lock, or resource API also uses the calling contract; its presence alone does not require a local lesson about that API. Explain local ordering, shared-state interactions, or recovery choices when the surrounding code implements them. Do not add a local paraphrase of that contract. An independent mechanism from the list above still requires its local explanation, including a nil or default branch that handles an error or race.

Use the 6-word tier only when the operation is a fixed threshold, unit conversion, truncation, or approximation and none of the other mechanisms above applies. Otherwise use the 15-word tier. Explain the actual consequence, such as a discarded suffix or a count that continues after storage stops. A purpose label or a restatement of the expression fails either tier. A numeric default's value alone does not establish why that value was chosen.

Every required local explanation must add at least one verified fact beyond translating the adjacent condition or statements: the rule's source, the reason for ordering, the affected data or callers, what remains available after partial work, or the failure the handling prevents. Identify that fact during review. For example, "stop when the header exceeds the limit" restates a comparison; explaining which summary metrics remain when detail entries are omitted adds information. This test applies to local explanations; a compact field label or declaration's opening definition can directly identify its value or action.

When a required operation lacks a supported explanation, inspect its callers, consumer, or owning rule. Keep the coverage requirement in force and report missing evidence if it remains unresolved. Inventing a reason or deleting the required comment cannot resolve the gap.

## Exhaustive exceptions

The following exceptions apply automatically:

- Generated code identified by a generation marker. Handwritten files inside a directory that also contains generated code remain covered.
- Third-party code kept verbatim.
- Source text stored as test input. The surrounding handwritten test code remains covered.
- Separate function documentation for anonymous callbacks. Their specified operations remain covered through local explanations.

The dedicated `doc.go` structure and formats without comment syntax follow the placement rules above. Additional exceptions require an explicit user instruction. Preserve license notices, generated markers, compiler and lint directives, and documentation syntax consumed by tools.

## Write direct sentences

- Start declaration comments with the symbol and its meaning or action: `Entry is ...`, `Read returns ...`, `Save writes ...`, `Ready reports whether ...`. Use the language's native documentation convention where it differs.
- Describe current behavior in the present tense. Use past tense for a verified event that explains a current constraint.
- Put the main behavior first, then the relevant condition or reason. Use `when`, `before`, `after`, `until`, and `unless` to state actual rules. Separate sentences when nested conditions become hard to follow.
- Use concrete actors and verbs: the caller releases, the reader waits, the parser returns, the file stores. Use passive wording when the object's state is the point and the actor is clear or irrelevant.
- At field and line-end positions, use phrases that meet the relevant minimum and state the field's meaning. Explain units, encoding, and special values in a preceding block when the explanation needs more room. Declaration explanations use complete sentences.
- Name objects again when `it`, `one`, or `this` could refer to several things. Prefer `reports whether` to awkward constructions such as `is whether`.
- State a constraint with its condition and practical consequence. A precise exclusion that prevents misuse is useful; unrelated assurances add noise.
- Replace praise and vague labels such as "robust handling," "special case," or "ensure correctness" with the behavior, trigger, or failure they refer to.

For a non-obvious choice, explain its triggering condition, the chosen action, and the failure it prevents. Include dependency behavior when that explains the choice. A verified issue or test name can support the explanation.

## Review before finishing

Inspect the in-scope code, including declarations that currently have no comments:

1. Read the file's comments as a continuous explanation alongside its code. Check whether a new reader can identify the problem, follow the main flow, and understand the decisions they must preserve. Review usefulness, accuracy, and the distribution of detail before counting words.
2. Enumerate the required file overviews, declarations, fields, test entries, and specified operations. Select their explanation roles using the tier rules.
3. Verify that every required item has an explanation in the required location.
4. Count words, complete sentences, and paragraphs against the applicable minima. Count shared field rules once, and check each field's compact explanation separately.
5. Check the required information against the implementation, callers, and relevant tests. Verify field defaults, precedence, and ownership at their consumers. Check exceptions to behavioral guarantees; record any probe needed to settle an uncertain claim. Unsupported claims and filler fail this check.
6. Compare new comments across all positions. Compact fields must use the compact tier; shared rules must be grouped. For each design explanation, identify its main question and check that each paragraph advances it. Keep compact definitions compact; a longer explanation must contribute another supported fact. For each local explanation, identify the fact added beyond the adjacent code. For each repeated fact, check its primary location and the additional information at the other location. Identical paragraph lengths are a signal to inspect for padding, not a reason to rewrite accurate prose for visual variety.
7. Compare the new file overviews together. Each must open on its concrete subject, explain a shared rule or relationship, and avoid an interchangeable introduction or repeated declaration summaries.
8. Update stale comments and verify placement after formatting. Keep references valid and preserve tool-consumed syntax.

Any missing explanation, insufficient length, incorrect placement, or inaccurate claim leaves the comment check incomplete. Fix failures within the authorized task. If a requirement remains unresolved, report incomplete validation with the affected item and reason. A read-only review reports these findings without editing files.

Perform the comment review manually against the code and the rules above. Report actual gaps when checks fail; passing package tests or formatting does not establish comment accuracy or completeness. Keep the review notes proportional to the task. In the final report, lead with whether the comments help readers understand the code and which material gaps remain; group minor presentation issues together. Passing individual checks does not establish that the whole file reads well. Ordinary prose edits need documentation checks, not an unrelated test suite. Keep comment-only work scoped to comments.

Read [references/examples.md](references/examples.md) when distinguishing function tiers or documenting fields, tests, and local operations. Use [references/file-overviews.md](references/file-overviews.md) for file overviews. Before the final comment review, read or reuse [references/review-examples.md](references/review-examples.md) for failures that can survive a word-count check.

When evaluating revisions of this skill, use [references/evaluation.md](references/evaluation.md) to compare versions on fixed starting files. This maintenance procedure is separate from ordinary code work.
