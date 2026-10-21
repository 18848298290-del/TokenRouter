import * as literalEmbeds from './scripts/prettier-literal-embeds.mjs'

// prettier.config.mjs defines the shared layout for handwritten frontend files.
// Vue and HTML source blocks use the literal-embedding printer so script and style
// syntax is formatted while string payloads remain unchanged. Standalone scripts
// disable embedding directly. Vue preserves sensitive template text boundaries,
// while ordinary HTML retains its default whitespace policy.
export default {
  plugins: [literalEmbeds],
  tabWidth: 2,
  useTabs: false,
  singleQuote: true,
  semi: false,
  printWidth: 100,
  trailingComma: 'all',
  endOfLine: 'lf',
  vueIndentScriptAndStyle: false,
  embeddedLanguageFormatting: 'auto',
  overrides: [
    {
      files: ['*.vue'],
      options: { htmlWhitespaceSensitivity: 'strict' },
    },
    {
      files: ['*.ts', '*.tsx', '*.js', '*.jsx', '*.mjs', '*.cjs'],
      options: { embeddedLanguageFormatting: 'off' },
    },
  ],
}
