# Subagent briefs

Before dispatch, fill in the appropriate brief with the current repository and task scope. Create a new agent each time. Use the model and reasoning effort agreed for the task. Supply a short context and let the agent read evidence as needed instead of repeatedly forwarding the entire main conversation.

## Read-only area review

Provide the repository path, task ID, file manifest, baseline, instruction and documentation entry points, existing verification results, and review goal.

Requirements:

- Follow repository AGENTS instructions and skill gates first. Load design, coding, and writing conventions, then read necessary documents through indexes and code anchors.
- Inspect responsibilities and structure across the manifest. Read paths with high maintenance cost, their callers, and relevant tests in depth. Use cross-package reads as evidence; retain primary ownership from the manifest.
- Remain read-only. The main agent controls code generation, formatting, installation, commits, and report files. Deliver the report in the final response.
- Focus on behavior-preserving changes that reduce maintenance cost. Explain the concrete problem and smallest solution for each proposal. State when no change is worthwhile. Usually return a few valuable candidates, while also reporting independent serious issues.
- Return coverage method, focus areas, and unconfirmed scope; candidate locations, evidence, benefits, risks, and verification methods; behavioral issues in a separate list; and designs worth preserving.
- Report cross-area issues to the main agent. Finish after the assigned task and let the main agent coordinate other modules.

Example dispatch:

```text
Perform one read-only code quality review of the supplied manifest.
Repository: {repo}
Task: {task_id}
Primary files: {manifest}
Baseline and existing changes: {baseline}
Instruction entry points: {instructions_and_skills}
Documentation routing: {docs}
Checks completed by the main agent: {validation}
Follow the read-only review requirements in this skill's references/agent-briefs.md.
Preserve business behavior and return a compact report with evidence.
Do not write files, commit, or delegate work.
```

## Single-item implementation

Provide the accepted-item ID, problem and solution, writable files, cross-package calling constraints, behavior to preserve, required tests, and areas other agents are editing.

Requirements:

- Read the finalized plan for the item and repository rules. Report newly discovered behavioral defects to the main agent for a separate list.
- Implement within the declared scope. If another task's files need changes, explain the dependency to the main agent so it can adjust assignments.
- Keep implementation and local tests focused on this item. The main agent performs shared-file generation, repository-wide formatting, staging, and commits.
- Report changed files, why behavior is equivalent, actual test commands and results, unverified scope, documentation impact, and coordination needs. Provide accessible log paths and retain both failure and retry results.

Example dispatch:

```text
Implement accepted item {accepted_id} within the scope below.
Repository: {repo}
Finalized plan and evidence: {plan_and_finding}
Writable files: {write_scope}
Behavior to preserve: {invariants}
Verification requirements: {checks}
Other active write tasks: {concurrent_scopes}
Follow repository rules and this skill's single-item implementation requirements.
Return the changes and verification results when finished.
Do not commit, format the entire repository, or delegate other tasks.
```

## Review the implementation diff

Provide the baseline, finalized acceptance table, current diff and new files, known-defect list, and completed verification scope. Use a new agent to judge the result from current code.

Include this task's committed changes, uncommitted changes, and new files. Look for introduced errors, code ownership issues, and deviations from the plan. Check interfaces, fields, nil and empty values, time sampling, short-circuit order, transactions, resources, and responsibility for copies.

Keep the review read-only. Return file locations, triggers, impact, and the smallest correction. If no issues are found, state the actual review scope and checks not run. The main agent decides corrections and revalidation. Keep existing defects separate from the current change.
