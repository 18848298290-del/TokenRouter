# Recording and reporting formats

Use the repository's report and plan conventions. If none exist, an authorized local output directory may contain `coverage.json`, area reports, `report.md`, `implementation-plan.md`, `results.md`, and validation logs. These filenames are examples. Combine documents for small tasks while retaining the same information.

## Coverage manifest

Record the baseline, task scope, generated/third-party exclusions, and each task's responsibility, file list, owner, status, and report location. Assign source and corresponding tests together where possible. Assign cross-component scenario tests by their primary behavior.

Check that every primary file is covered and belongs to exactly one task. Cross-package evidence reads may overlap.

Possible statuses are unassigned, reviewing, returned for verification, complete, and follow-up needed. File assignment alone does not complete a review. Resume after interruption from the actual state.

## Area report

- Coverage: file count, structural scan method, paths read in depth, checks actually run, and unconfirmed areas.
- Candidates: location and baseline, evidence of the problem, smallest solution, maintenance benefit, risk, callers, and verification method.
- Behavioral issues: triggers, impact, static evidence or reproduction results, and conditions still requiring confirmation. Distinguish confirmed current behavior from an unresolved decision about whether it is a defect.
- Unchanged designs: reasons and evidence for preserving the existing design.

Line numbers refer to the specified baseline. After code changes, locate findings through symbols or commits instead of treating old line numbers as current.

## Global review report

1. Scope, baseline, and coverage depth. Distinguish structural scans, focused reading, and executed checks.
2. Global assessment: cross-package relationships, shared rules, differences to preserve, and reasons for the chosen change size.
3. Acceptance table: stable ID, reason, affected scope, solution, benefit, risk, and verification.
4. Deferred and unchanged items: link candidates and give concrete reasons such as insufficient benefit, behavioral differences, missing evidence, or verification cost.
5. Behavioral and compatibility issues: separate IDs, deduplicated impact, evidence strength, and follow-up verification. Recommend priority checks for serious issues while distinguishing static analysis from runtime reproduction.

The main agent verifies and filters candidates using area reports as evidence. When several packages propose the same abstraction, first check whether their callers share the same rules. A package with no accepted changes is a valid result.

## Finalized implementation plan

List accepted items, order and dependencies, writable scope, behavior to preserve, required tests, documentation/generated-file impact, and commit units. Remove behavior changes requiring user decisions from implementation items.

Prepare and show the plan before editing code. Continue when implementation is already authorized; wait when confirmation was requested. Append progress after the finalized plan and explain adjustments based on new evidence and remaining work.

## Verification and results

| Item | Information to record |
| --- | --- |
| Each optimization | Changes, related accepted item, status, and commit if any |
| Each check | Command, toolchain, scope, exit result, and log location |
| Failure | Failed phase, evidence of cause, correction or environment repair, and retry result |
| Skipped or unrun check | Reason and unverified scope; include commands that matched no tests |
| Independent review | Baseline/diff scope, findings and resolutions, review limits |
| Final state | Working tree, commit/push status, and remote checks when applicable |

When proof that committed content matches accepted content is needed, save content hashes and compare them after committing under the repository's rules. Report performance benefits with before-and-after measurements and their conditions. Reports and plans are local working materials by default; the current task and repository rules determine whether they enter version control.

## Invocation examples

```text
Use $code-quality-review to review the entire backend. Produce a report and
optimization plan first, and leave code unchanged for now.
```

```text
Use $code-quality-review to review and optimize backend/ while preserving business
behavior. Complete the global report and plan first, then implement directly.
Commit each item on the repository's required branch and leave the commits local.
```

```text
Use $code-quality-review to review and optimize the cache subsystem and its direct
callers. Implement behavior-preserving changes with clear benefits, then leave the
local diff for me to inspect.
```
