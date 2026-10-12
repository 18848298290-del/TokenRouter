---
name: code-quality-review
description: Review and moderately improve code quality across a backend, subsystem, or codebase. Assign read-only parallel reviews by responsibility, consolidate findings and an implementation plan in the main agent, then implement and verify behavior-preserving changes within the authorized scope. Use for broad maintainability, coding style, and local design improvements. Use the appropriate separate workflow for single-issue fixes, individual PR reviews, or major architectural redesigns.
---

# Code quality review and optimization

Collect complete reports for every review area before deciding which changes to implement across the system. Reduce the cost of understanding, changing, and verifying code while preserving the overall architecture and business behavior.

## Define the scope

Inspect repository rules, directories, the branch, and working-tree state, then establish the following. Reuse clear answers already available. Ask once for tradeoffs that require the user's choice.

| Item | Default |
| --- | --- |
| Scope | Use the backend, module, or directory specified by the user. If unspecified, clarify against the project structure before expanding to the entire repository. |
| Stopping point | For a review, report, or plan, stop at the global report. For "review and optimize," continue after the report and implementation plan are ready. Invoking only the skill name starts with review and planning. |
| Change size | Preserve behavior and accept local changes with clear maintenance benefits. List behavioral defects separately. |
| Subagents | GPT-6 Astra, High. Use model ID `gpt-6-astra` and reasoning effort `high` when supported. |
| Concurrency | At most 6 subagents, subject to available slots, test resources, and the user's budget. |
| Branch, commits, and push | Follow repository rules and current authorization. Leave local changes uncommitted when delivery requirements are unspecified. |
| Acceptance | Use the repository's verification strategy and select local, integration, and full checks for the risks of this task. |

If the user asks only for a brief arrangement first, deliver the scope and decision summary. Begin the formal review when requested. In plan mode, follow its file-writing and implementation restrictions.

## Repository rules and evaluation criteria

- Read the target repository's `AGENTS.md` first. If it has a `project-doc` gate, load the skill and documentation index as required, then read necessary sections by responsibility and code anchor. Reuse content that remains in context.
- Load `code-complete` and `a-philosophy-of-software-design` from the repository or current session, starting with their mini specifications. Follow repository writing skills for reports, comments, documentation, and commit messages.
- Follow the gate when a required skill is missing. If the repository does not configure the two design skills above, state the conventions used and apply the criteria below. Install dependency skills only when requested.
- Assess whether a change reduces duplicated knowledge, hidden dependencies, locations that must change together, and state maintainers must remember at once. New functions, types, and files need a clear responsibility and maintenance benefit.
- Check caller-visible behavior: errors and first-error order, nil and empty values, defaults, serialization, field order, time sampling, lock and transaction scope, resource cleanup, retries, and ownership of copies.
- Long files, duplication, and names containing `legacy` are investigation leads. Base changes on evidence from callers, rules, and tests. Support quantified performance claims with before-and-after measurements.

## Phase 1: read-only reviews by area

1. Record the baseline commit and existing changes. Preserve the user's work and stage only task-owned differences when committing. Enumerate handwritten source, tests, and build entry points in scope. Identify generated code, historical migrations, third-party code, and build outputs. Count evidence files separately from primary review files.
2. Assign files by responsibility. Combine closely related small packages and split large packages by topic. Give each file one primary task; other tasks may read it to verify dependencies. Maintain a coverage manifest with tasks, files, owners, and status.
3. Create a new subagent for each task. Supply only the repository location, file list, instruction entry points, and task requirements. Let the agent read business documentation as needed. Use the [subagent briefs](references/agent-briefs.md).
4. Keep review agents read-only. They return findings to the main agent and finish. Close them when the tool supports it; otherwise, give them no further assignments after completion. Use new agents for implementation and later reviews. Route tasks discovered by subagents through the main agent.
5. The main agent saves task findings and controls concurrency and context use. It coordinates baseline tests so agents do not repeat full checks. Record known failures and environment requirements with verification results.
6. Each area report states its structural scan, focused reading, executed checks, and uncovered scope. Record file assignment, declaration scans, and passing tests as distinct evidence. None establishes a line-by-line audit.

If the requested model or multi-agent capability is unavailable, explain the limitation and agree on an alternative with the user. Continue directory and dependency investigation while waiting. Replace failed or incomplete tasks with new, narrowly scoped agents and keep the coverage manifest accurate.

## Phase 2: global analysis by the main agent

After collecting every area report, check coverage gaps and duplicate ownership. Complete missing areas, or have the user narrow the scope, before producing a complete implementation plan for that scope.

Assess each candidate's necessity, maintenance benefit, risk, callers, and verification cost:

- Merge duplicate reports of the same issue. Verify whether similar code in adjacent packages follows the same rules.
- Check whether a local extraction shifts complexity to callers or creates thin wrappers, mode switches, or cross-package coupling.
- Classify candidates as accepted, deferred, or unchanged, with reasons. Some packages may need no changes.
- List behavioral defects and unresolved compatibility decisions separately. Record static evidence, reproduction status, and impact. Leave these for separate fixes unless the current task authorizes them.
- Define dependency order, scope, behavior to preserve, verification commands, and commit units for accepted items. The main agent decides shared interfaces and cross-package design.

Produce the complete review report and implementation plan using [recording and reporting formats](references/reporting.md) before editing code. Continue when the user has already authorized implementation after review. Stop at the report when the user requires plan confirmation first.

Store reports and progress in the repository's designated local-materials directory, conversation output, or a temporary directory. Respect read-only and file-writing restrictions. If the repository requires a saved plan, save the finalized version verbatim before implementation and append progress at the end. Exclude temporary review records from code commits by default.

## Phase 3: implementation and verification

1. Follow the finalized plan. Each new subagent owns one clearly defined optimization with declared writable files. Run tasks sequentially when their write scopes overlap or they have dependencies; independent tasks may run in parallel.
2. The main agent retains control of shared interfaces, staging, and commits. Subagents run local tests and report actual results. The main agent schedules repository-wide formatting, code generation, and other commands that touch shared files after writing tasks stop.
3. For behavior vulnerable to refactoring, first add tests for outputs, call counts, timing, ownership, or persistence. Run them against the old implementation when possible. Source-string matching and tests that copy the implementation do not establish behavioral equivalence.
4. When merging duplicated SQL, Lua, or field mappings, compare contents and order before and after. Use the project's isolated fixtures for resource behavior requiring integration tests.
5. Run targeted local verification. Record unmatched tests, skips, and interrupted checks as unverified and correct the scope. For environment startup failures, read the logs and retry the affected check once. If the same cause persists, address the environment dependency, retain incomplete acceptance items, and continue independent work.
6. After all changes, use new subagents for a read-only review of the complete implementation diff. Focus on regressions, missed callers, shared data, evaluation timing, and scope expansion. Keep existing defects in a separate record.
7. The main agent runs repository-required formatting and checks. When commits are authorized, verify and stage exact files or hunks and create independently reviewable, reversible commits. Follow repository naming, branch, hook, and documentation rules.
8. Run the required final acceptance checks, fix issues introduced by this work, and reverify. Reuse passing results only while code and environment conditions still apply. Push when authorized and track the corresponding remote checks to a terminal state.

## Delivery

Deliver the coverage manifest, complete review report, finalized plan, change summary, and verification results. State each item's implementation status, commits if any, check results, failure and retry evidence, unverified scope, and working-tree state.

Give deferred items and behavioral issues traceable identifiers. When reporting elapsed time, record start and end times and say whether waiting and tests are included. Read token usage from available statistics. File counts, changed lines, and agent counts cannot establish usage.
