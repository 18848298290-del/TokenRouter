import autoprefixer from 'autoprefixer'
import tailwindcss from 'tailwindcss'

// postcss.config.js adjusts the bundled CJK font faces before generating and
// prefixing application styles. The configured Noto font uses a limited weight
// range, while characters marked Emoji_Presentation should reach the system's
// emoji fallback fonts. Font-source path matching limits both transformations to
// the selected bundled family; other font faces retain their declarations.
//
// Removing code points can split an advertised Unicode interval into several
// kept intervals or leave a font slice empty. The range conversion preserves the
// remaining code-point order, and empty slices are removed with their owning rule.
// Tailwind and autoprefixer then process the adjusted stylesheet in their declared
// order, preserving the source of the application's shared font policy.

// notoSansScCssPattern identifies the bundled CJK font sources whose weight and coverage are adjusted.
const notoSansScCssPattern = /@fontsource-variable[\\/]noto-sans-sc[\\/]/

// notoSansScMaxWeight caps the advertised CJK font-face range at semibold weight,
// limiting the bundled variable font independently of the requested utility weight.
const notoSansScMaxWeight = 600

// emojiPresentationPattern identifies Unicode code points with default emoji presentation for removal from CJK coverage.
const emojiPresentationPattern = /\p{Emoji_Presentation}/u

// parseUnicodeRange reads explicit hexadecimal code points and inclusive ranges.
// The bundled font supplies comma-separated U+ entries; a single code point uses
// the same lower and upper bound in the returned pair.
const parseUnicodeRange = (value) =>
  value.split(',').map((part) => {
    const [start, end = start] = part.trim().replace(/^U\+/i, '').split('-')
    return [parseInt(start, 16), parseInt(end, 16)]
  })

// formatUnicodeRange serializes inclusive code-point pairs into a CSS unicode-range declaration.
const formatUnicodeRange = (ranges) =>
  ranges
    .map(([start, end]) =>
      start === end ? `U+${start.toString(16)}` : `U+${start.toString(16)}-${end.toString(16)}`,
    )
    .join(',')

// removeEmojiCodePoints filters default emoji characters from supplied inclusive ranges.
// Callers provide the ordered explicit ranges used by the bundled font slices.
// Each surviving code point extends the immediately preceding kept run when it
// is adjacent, otherwise it starts another run. The function leaves input pairs
// unchanged and returns an empty list when every visited code point is removed.
// That result lets the caller remove an entire font-face slice instead of emitting
// an empty coverage declaration.
const removeEmojiCodePoints = (ranges) => {
  const kept = []
  for (const [start, end] of ranges) {
    for (let codePoint = start; codePoint <= end; codePoint++) {
      if (emojiPresentationPattern.test(String.fromCodePoint(codePoint))) continue

      const last = kept[kept.length - 1]
      if (last && last[1] === codePoint - 1) {
        last[1] = codePoint
      } else {
        kept.push([codePoint, codePoint])
      }
    }
  }
  return kept
}

// adjustNotoSansScFontFace creates the PostCSS hook for bundled CJK font policy.
// Matching font faces receive the configured maximum weight and filtered Unicode
// ranges. Slices with no remaining coverage are removed before later plugins run.
const adjustNotoSansScFontFace = () => ({
  postcssPlugin: 'adjust-noto-sans-sc-font-face',
  AtRule: {
    // font-face adjusts matching source rules in place during PostCSS traversal.
    // Other font sources are ignored. A slice whose filtered coverage is empty
    // is removed with its declarations rather than left as an unusable font face.
    'font-face': (rule) => {
      const source = rule.source?.input.file ?? ''
      if (!notoSansScCssPattern.test(source)) return

      // Retain each slice's lower bound while applying the shared maximum weight.
      rule.walkDecls('font-weight', (decl) => {
        const [min] = decl.value.trim().split(/\s+/)
        decl.value = `${min} ${notoSansScMaxWeight}`
      })
      rule.walkDecls('unicode-range', (decl) => {
        const ranges = removeEmojiCodePoints(parseUnicodeRange(decl.value))
        // An emoji-only slice has no remaining CJK coverage, so remove its
        // complete font rule instead of advertising an empty Unicode interval.
        if (ranges.length === 0) {
          rule.remove()
          return
        }
        decl.value = formatUnicodeRange(ranges)
      })
    },
  },
})
adjustNotoSansScFontFace.postcss = true

export default {
  plugins: [adjustNotoSansScFontFace(), tailwindcss(), autoprefixer()],
}
