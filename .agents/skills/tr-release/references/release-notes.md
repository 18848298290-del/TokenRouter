# Change coverage and fact checking

Use this workflow whenever writing or revising a Release description. Write the main description in English and append a collapsed Simplified Chinese translation using the layout in [SKILL.md](../SKILL.md#chinese-translation-and-final-layout). Save the coverage manifest and complete command output in a temporary directory outside the release commit. Use them for review and keep them separate from the Release body.

## Collect the complete range

Use the fixed baseline commit `base` and target commit `target` from preparation throughout the analysis. A moving `HEAD` cannot identify the reviewed target reliably. Collect at least:

```bash
git log --reverse --topo-order --format='%H %P%n%B' "$base..$target"
git diff --stat "$base" "$target"
git diff --name-status --find-renames "$base" "$target"
git rev-list --count "$base..$target"
```

The complete log's commit count should match preparation output. Define the range from all commits; `--no-merges`, first-parent-only traversal, commit-prefix filters, date filters, and the latest N entries omit part of that range. Save large output to files and read it to the end in commit or module batches. Retrieve every truncated portion.

Identify candidate changes by commit, then verify that they are present in the final diff:

- For ordinary commits, read the full message, changed files, and relevant patch. One commit may contain several independent changes.
- For merges, inspect both the introduced commits and the diff against the first parent to check conflict-resolution behavior. History marked as merged may not survive in final code. Reachability alone does not establish a released feature.
- Track implementation, corrections, reverts, and reimplementation of the same capability together. Describe the net change from baseline to target. Record an exclusion for fully reverted changes and describe the surviving portion of partial reverts.
- Use the final file manifest to find omissions from log analysis. Account for new and deleted files, configuration, migrations, dependencies, deployment assets, and frontend text. Group generated files with their sources, while still inspecting handwritten schemas and migrations.

## Build the coverage manifest

Use one row per independently describable behavior change:

| Field | Requirement |
| --- | --- |
| Change and source | Related commit SHAs and changed paths; split multiple features from one commit into separate rows |
| Baseline and target behavior | Trigger, affected users or objects, prior behavior, target behavior, and remaining limits |
| Evidence | Implementation location at the target commit and relevant tests, callers, configuration, or migrations |
| Disposition | Matching release entry or exclusion reason, such as fully reverted, duplicate, formatting only, or no external impact |

Assign a disposition to every commit in the range and account for every file in the final diff. Exclude a batch only after verifying that it is one mechanical operation, and list its scope. Commit-title prefixes such as `refactor`, `chore`, `test`, and `docs` do not prove absence of external impact.

Consider ordinary users, administrators, API callers, and deployment maintainers. Configuration removal, default changes, compatibility strategies, price sources, billing, error codes, permissions, import/export, interactions, and layouts may each need an entry. Purely internal changes may be excluded. Preserve the practical upgrade impact of architectural or dependency changes.

## Check accuracy

Support each entry with direct implementation evidence. For cross-module capabilities, trace entry points, calls, configuration, and results, using `project-doc` routing when needed. Use documentation and tests to aid understanding and match the final description to the target commit's actual implementation. Existing tests are separate from tests run during this task.

Check model/protocol/platform scope, activation conditions, defaults, permissions, billing units, price fallback, data migration, and compatibility entry points. Keep claims scoped to the repaired platform or path. Claims such as "fully resolved," "significantly faster," or "lossless migration" need corresponding evidence. Without measurements, describe the repeated operations removed and leave performance figures out.

For example, record credential rotation and list styling separately when one commit changes both. "Improve API key management" hides that distinction. When a migration removes an old field, inspect data transfer and compatibility reads before deciding whether upgrade notes are needed. Field removal alone does not establish downtime or data loss.

## Two-way review and completion criteria

First compare the manifest to the body: every external change has an identifiable entry, and combined entries preserve independent capabilities, conditions, and risks. Then trace every sentence back to evidence, including the summary. Keep claims within the reviewed manifest.

Scan module boundaries once more using the final file manifest. Look for small fixes obscured by large refactors, features lost in combined entries, and upgrade impacts from removed configuration or APIs. Record total commits, reviewed commits, change items, published items, and exclusion reasons. These counts help expose gaps; they supplement review of meaning.

Proceed to publication after the full commit set and final file manifest have been checked, no changes remain unresolved, every statement has evidence, and every external change is recognizable in the notes. Continue investigating when scope is unclear or output remains unread. Publish from the completed, reviewed notes.

## Translation review

After the English description includes GoReleaser's generated sections, compare its Chinese translation section by section and item by item. Cover the summary, upgrade instructions, changes, installation text, and documentation links. Preserve qualifications, defaults, affected platforms, billing units, migration requirements, and rollback limits. Keep version numbers, identifiers, URLs, and executable code blocks identical. Each Chinese claim needs a matching English claim; correct omissions or differences before saving the final body.

Check that the Release description ends with exactly one `中文翻译` block, collapsed by default. Repeat the full-body comparison after uploading it so acceptance covers both languages and the generated sections.
