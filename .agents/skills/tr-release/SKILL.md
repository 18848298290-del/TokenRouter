---
name: tr-release
description: Release TokenRouter at a requested version, or the next patch tag when no version is specified, with structured English release notes. Use when the user invokes $tr-release or asks to publish a TokenRouter version, increment the latest vMAJOR.MINOR.PATCH tag, populate the GitHub Release changelog, or verify the release workflow.
---

# TR Release

## Goal and scope

Publish the requested TokenRouter version, or increment the patch version when none is specified. Write release notes in English and cover every external change from the previous release to the target commit accurately. The workflow passes the annotated tag body to GoReleaser to produce the Release description.

For requests to revise the skill or review notes, complete that edit or review. Publishing a new version and modifying an existing Release each require the corresponding user request.

## Establish the repository and version range

1. Follow repository instructions from the TokenRouter root. Use the `project-doc` release route first and `humanizer` when writing notes.
2. Confirm the release destination with `git remote get-url origin` and derive `release_repo` from the URL (currently `TokenFlux/TokenRouter`). Pass `--repo "$release_repo"` to every `gh` command; a fork's default `gh` repository may be upstream. If origin differs from the user's intended destination, resolve that before continuing.
3. Run the preparation command. It fetches and checks origin tags. Tag creation and pushing happen during publication:

```bash
python3 .agents/skills/tr-release/scripts/tr_release.py --prepare
# For a specified target version:
python3 .agents/skills/tr-release/scripts/tr_release.py --prepare --version 0.2.0
```

The script selects the highest `vMAJOR.MINOR.PATCH` from origin and ignores tags that exist only locally or upstream. An explicit version must be higher; both `0.2.0` and `v0.2.0` are accepted. Save the output values `previous_tag`, `previous_commit`, `new_tag`, `commit`, `commit_count`, and the fixed-SHA `change_range`.

4. Verify the published Release for `previous_tag` in the target repository. If the tag has no Release, publication has not succeeded, or it is not the actual previous release for that version line, investigate before describing the range as this update. The script already requires the baseline to be an ancestor of the target commit. Existing Release notes provide context; verify this release against its code.
5. Before drafting notes, complete [Change coverage and fact checking](references/release-notes.md). Build a coverage manifest from the fixed SHA range, inspect every commit and the final file diff, and map each change to a release entry or an evidence-based exclusion. Read truncated output in batches until complete. Sampling cannot establish full coverage.

## Write and review the notes

Complete the coverage manifest first, then use the structure below and keep categories with content. For configuration removal, incompatible APIs, billing changes, or migration requirements, place an evidence-based `## Upgrade notes` section before the feature categories.

```markdown
One sentence describing the most important user impact, risk, or reason to upgrade.

## New features

- Describe the capability, who can use it, and the result.

## Improvements

- Describe the prior problem and the effect of the improvement.

## Bug fixes

- Describe the failure scenario, impact, and corrected behavior.
```

Organize entries by capability and use case. Combine successive corrections to one capability while preserving independent capabilities, failures, and upgrade impacts. Prioritize completeness over length, with no fixed entry limit. Avoid compressing independent changes into "UI improvements" or "better stability." Classify additions, changes, and fixes against the previous release's actual behavior.

State scope, defaults, and effects supported by code and tests. Refactoring alone does not establish a performance gain; new tests do not establish a fix; new configuration does not establish that a feature is enabled by default. Investigate uncertain facts. Keep unverified user impacts pending until resolved. Security conclusions, downtime requirements, and rollback limits also need evidence.

GoReleaser supplies the Release title, product introduction, standard installation instructions, and documentation links. Keep the notes focused on changes. Commit SHAs and the internal coverage manifest stay outside the Release body. Exclude tests, mechanical refactoring, formatting, and developer-tool changes after confirming that they have no external impact. Commit prefixes and directories alone are insufficient grounds for exclusion.

Perform the reference document's two-way review: every external change has an entry, and every description traces to evidence at the fixed target commit. Save the final notes as a UTF-8 file and publish from that same file to preserve the reviewed detail. Briefly show the version range and notes before publication. Continue directly when publication is already authorized.

## Publish and verify

Pass the full SHA and previous tag recorded during preparation. If another release appears remotely or local HEAD changes, the script rejects the stale range and the notes need review again. Take the example variables from preparation output and point `notes_file` at the reviewed notes:

```bash
python3 .agents/skills/tr-release/scripts/tr_release.py \
  --expected-head "$release_commit" \
  --expected-previous-tag "$previous_tag" \
  --notes-file "$notes_file"
```

When the user specifies a version, add `--version VERSION` to the same command. Otherwise, retain automatic patch incrementing. Use `--notes-file -` to provide the full notes through standard input.

1. Wait for the `Release` Actions run triggered by `new_tag` to succeed in the explicit target repository. Verify the tag and commit to distinguish this release from other runs on the same commit.
2. Read the final body with `gh release view "$new_tag" --repo "$release_repo" --json body,url,tagName`. Compare the complete notes block, allowing only line-ending differences and whitespace at the block's edges. The summary, every category, and every list item must remain intact. A present title or nonempty body does not establish acceptance.
3. If the workflow succeeds but the body is missing, truncated, or different, preserve the generated introduction and installation/documentation footer. Replace the change-notes block with the reviewed file using `gh release edit "$new_tag" --repo "$release_repo" --notes-file ...`, then reread it. If block boundaries are unclear, someone else has edited the body, or the correction still differs, report the specific problem and stop repeated overwrites. Keep the existing tag fixed.
4. Complete the other checks required by the repository's release route. Report unrun and failed checks accurately. Return the previous tag, new tag, commit SHA, Actions result, Release link, and full-body comparison result.

## Script constraints

- Before publication, tracked files must have no staged or unstaged changes. Untracked files stay outside the release commit. If an upstream exists, the current branch must match it.
- Create an annotated tag at the reviewed commit with title `TokenRouter MAJOR.MINOR.PATCH`, preserve the notes verbatim in its body, and push it to origin.
- Reject existing local or remote tags. Delete the newly created local tag if annotation validation or pushing fails.
- The script checks basic Markdown structure. The evidence manifest and two-way review establish factual accuracy and change coverage.
- Create a release commit only when requested. Keep existing tags fixed. A successful push is one step in publication; complete release verification afterward.
