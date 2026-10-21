import { parsers as htmlParsers, printers as htmlPrinters } from 'prettier/plugins/html'

// prettier-literal-embeds.mjs formats Vue and HTML source blocks while retaining
// literal examples inside those blocks. Prettier's global embedded-language
// switch also controls Vue script and style formatting, so disabling it for the
// complete component leaves executable code outside the project's layout rules.
// The HTML printer must enter those source blocks before further embedding stops.
//
// This plugin delegates block discovery and printing to the pinned Prettier
// implementation. Its text-to-document callback disables nested embedding when
// formatting each block, preserving tagged template and annotated string payloads
// used by tests and application content. The normal script and stylesheet parsers
// still format their syntax. Template whitespace follows the configured HTML
// policy, and imports retain their original execution order. Multi-statement
// event bindings keep semicolons because Vue uses those separators to recognize
// a statement handler instead of attempting to parse a single expression.

/**
 * embedLiteralSources delegates a source block with nested embedding disabled.
 * Prettier calls this printer hook while visiting a Vue or HTML syntax tree.
 * Synchronous documents and unsupported nodes retain the built-in result; queued
 * block callbacks receive a text-to-document adapter that formats source syntax
 * but preserves embedded string payloads. The callback forwards all other parser
 * options and printing arguments, so the pinned printer continues to own block
 * indentation, language selection, and error handling.
 */
function embedLiteralSources(path, options) {
  const embedded = htmlPrinters.html.embed(path, options)
  if (typeof embedded !== 'function') return embedded
  return (textToDoc, print, nodePath, nodeOptions) =>
    embedded(
      (source, parserOptions) =>
        textToDoc(source, {
          ...parserOptions,
          embeddedLanguageFormatting: 'off',
          ...(parserOptions.parser?.includes('event_binding') ? { semi: true } : {}),
        }),
      print,
      nodePath,
      nodeOptions,
    )
}

// printers replaces the HTML embedding hook while retaining the pinned parser's
// remaining printing operations for Vue components and ordinary HTML documents.
export const printers = {
  html: { ...htmlPrinters.html, embed: embedLiteralSources },
}

// parsers associates the pinned HTML and Vue parsers with this plugin's printer;
// Prettier otherwise selects the built-in printer that owns those parser exports.
export const parsers = htmlParsers
