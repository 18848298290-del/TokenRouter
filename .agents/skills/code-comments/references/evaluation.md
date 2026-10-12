# Comparing skill revisions

Use this procedure when evaluating a change to the skill. Ordinary comment work uses the review in SKILL.md. Evaluate comment quality by reading the implementation and output; no local comment checker is required.

## Keep the starting conditions fixed

Select a small set of handwritten files that collectively exercise compact definitions, shared field rules, parameter defaults, recovery, concurrency, ownership or reflection, and a design explanation spanning several related decisions. Include a routine library wrapper to check that it stays compact. Keep the exact starting contents, repository revision, applicable AGENTS.md, and skill versions for later comparisons. Keep temporary evaluation material outside the active checkout.

Run each version from a fresh copy of those starting files. Hold other repository instructions and the task prompt constant. New files can broaden coverage, but report them separately from the comparison on fixed files.

Use fresh agent contexts when delegation is available and authorized. Give each agent the checkout and an ordinary task, such as improving the readability of named files while preserving behavior. Let repository instructions route the agent to the skill. Keep expected findings, prior generated comments, and reminders about individual rules out of its prompt. Use the same model settings across versions and record any differences that could affect the result.

## Examine the unchanged output

Save each agent's original diff and validation results before editing its output. Review the artifacts against the code and relevant consumers. An agent's completion statement is evidence of what it reported, not proof that its comment review passed.

Read each file as a whole first. Compare whether the reader can understand its purpose, follow the explanation from problem to solution, and identify the decisions a later edit must preserve. Check whether a concrete example clarifies a difficult rule and whether secondary details crowd out the main idea. Lead the evaluation with those observations and representative passages.

Then inspect supporting findings on the same files:

- Unsupported or incorrect behavioral claims, especially ownership and exceptional cases.
- Required declarations, operations, or calling rules left unexplained.
- Facts repeated at adjacent levels without additional information.
- Compact fields expanded unnecessarily, or shared rules repeated separately.
- Design explanations that list unrelated facts or leave independent questions in one paragraph.
- Local comments that translate expressions without explaining a consequence or reason.

Record representative locations and the facts behind each finding. Word counts and line-length distributions can help locate padding or omissions; they do not measure explanatory quality. Group small presentation defects instead of making them the main verdict. A sample that leaves every field untouched cannot establish that the field rules work. Add a separate type-focused task when that coverage is missing. Treat results on examples already used to revise the skill as regression evidence; use separate unfamiliar files to evaluate whether the writing approach carries over.

Report program verification and comment review separately. Run the repository's relevant checks when the task changes code. Preserve raw trial output for inspection, and identify any later correction as a separate change. Treat improvement on one sample as evidence for that sample; retain unresolved failures in the report.
