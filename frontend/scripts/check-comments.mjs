import { basename } from 'node:path'
import { pathToFileURL } from 'node:url'
import { parseSource, runSourceCheck, sourceLocation } from './source-analysis.mjs'

// check-comments.mjs checks explanatory comment language and handwritten file
// overview names across frontend source languages. Parser tokens distinguish
// comments from user text and test inputs. Declaration coverage and explanation
// quality remain manual review responsibilities under the repository comment skill.

// CHINESE_PROSE identifies Han characters in comment tokens; license notices and
// generated source headers retain their upstream wording under repository rules.
const CHINESE_PROSE = /\p{Script=Han}/u
const LICENSE_NOTICE = /@license\b|SPDX-License-Identifier:|\bcopyright\b/i

/** cleanComment removes block-comment decoration before inspecting explanatory prose. */
function cleanComment(value) {
  return value.replace(/^\s*\* ?/gm, '').trim()
}

/**
 * checkComments returns language and overview violations for one source file.
 * Source and test files require filename-led overviews; their declaration and
 * test-entry explanations are reviewed manually. A generated header before the
 * first source unit exempts the generated file; marker text inside a string has
 * no effect.
 */
export function checkComments(text, filename) {
  const parsed = parseSource(text, filename)
  const firstUnit = Math.min(...parsed.units.map((unit) => unit.range[0]))
  if (
    parsed.comments.some(
      (comment) =>
        comment.range[0] < firstUnit &&
        /\bCode generated\b[^\n]*\bDO NOT EDIT\./.test(comment.value),
    )
  )
    return []
  const issues = []
  for (const comment of parsed.comments) {
    if (CHINESE_PROSE.test(comment.value) && !LICENSE_NOTICE.test(comment.value)) {
      issues.push({
        ...sourceLocation(text, comment.range[0]),
        message: 'Write explanatory comments in English.',
      })
    }
  }
  const expected = basename(filename)
  const overview = parsed.comments.find((comment) => {
    const prose = cleanComment(comment.value)
    return prose.startsWith(`${expected} `) && /[A-Za-z]/.test(prose.slice(expected.length))
  })
  if (!overview) {
    issues.push({
      line: 1,
      column: 1,
      message: `Begin a file overview with "${expected}" and its responsibility.`,
    })
  }
  return issues
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = runSourceCheck('check:comments', checkComments)
}
