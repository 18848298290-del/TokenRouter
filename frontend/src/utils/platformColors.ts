/**
 * platformColors.ts supplies platform labels and shared class strings for badges, text,
 * icons, and selected choices. Recognized platform identifiers receive their configured
 * palette. Unrecognized values use purpose-specific default colors, while label helpers
 * retain nonempty custom platform text.
 */

/**
 * Platform enumerates the identifiers with dedicated frontend platform palettes.
 */
export type Platform =
  | 'anthropic'
  | 'openai'
  | 'antigravity'
  | 'gemini'
  | 'qoder'
  | 'grok'
  | 'kimi'
  | 'zhipu'
  | 'deepseek'
  | 'jev'

/**
 * BADGE_DEFAULT and BADGE_LIGHT provide neutral and platform-specific soft badge
 * classes, including dark theme colors for platform indicators.
 */
const BADGE_DEFAULT = 'bg-slate-500/10 text-slate-600 border-slate-500/30 dark:text-slate-400'

const BADGE_LIGHT: Record<Platform, string> = {
  anthropic: 'bg-orange-500/10 text-orange-600 dark:bg-orange-500/10 dark:text-orange-300',
  openai: 'bg-green-500/10 text-green-600 dark:bg-green-500/10 dark:text-green-300',
  antigravity: 'bg-purple-500/10 text-purple-600 dark:bg-purple-500/10 dark:text-purple-300',
  gemini: 'bg-blue-500/10 text-blue-600 dark:bg-blue-500/10 dark:text-blue-300',
  qoder: 'bg-cyan-500/10 text-cyan-600 dark:bg-cyan-500/10 dark:text-cyan-300',
  grok: 'bg-zinc-800/10 text-zinc-800 dark:bg-zinc-500/10 dark:text-zinc-200',
  jev: 'bg-zinc-800/10 text-zinc-800 dark:bg-zinc-500/10 dark:text-zinc-200',
  kimi: 'bg-pink-500/10 text-pink-600 dark:bg-pink-500/10 dark:text-pink-300',
  zhipu: 'bg-indigo-500/10 text-indigo-600 dark:bg-indigo-500/10 dark:text-indigo-300',
  deepseek: 'bg-teal-500/10 text-teal-600 dark:bg-teal-500/10 dark:text-teal-300',
}

/**
 * TEXT and TEXT_DEFAULT provide numeric and label foreground colors, selecting primary
 * colors when the platform identifier is unrecognized.
 */
const TEXT: Record<Platform, string> = {
  anthropic: 'text-orange-600 dark:text-orange-400',
  openai: 'text-emerald-600 dark:text-emerald-400',
  antigravity: 'text-purple-600 dark:text-purple-400',
  gemini: 'text-blue-600 dark:text-blue-400',
  qoder: 'text-cyan-600 dark:text-cyan-400',
  grok: 'text-zinc-800 dark:text-zinc-200',
  jev: 'text-zinc-800 dark:text-zinc-200',
  kimi: 'text-pink-600 dark:text-pink-400',
  zhipu: 'text-indigo-600 dark:text-indigo-400',
  deepseek: 'text-teal-600 dark:text-teal-400',
}
const TEXT_DEFAULT = 'text-primary-600 dark:text-primary-400'

/**
 * ICON and ICON_DEFAULT provide platform-specific small-icon foreground colors, with
 * primary colors used when the identifier is unrecognized.
 */
const ICON: Record<Platform, string> = {
  anthropic: 'text-orange-500 dark:text-orange-400',
  openai: 'text-emerald-500 dark:text-emerald-400',
  antigravity: 'text-purple-500 dark:text-purple-400',
  gemini: 'text-blue-500 dark:text-blue-400',
  qoder: 'text-cyan-500 dark:text-cyan-400',
  grok: 'text-zinc-800 dark:text-zinc-200',
  jev: 'text-zinc-800 dark:text-zinc-200',
  kimi: 'text-pink-500 dark:text-pink-400',
  zhipu: 'text-indigo-500 dark:text-indigo-400',
  deepseek: 'text-teal-500 dark:text-teal-400',
}
const ICON_DEFAULT = 'text-primary-500 dark:text-primary-400'

/**
 * CHOICE_SELECTED and CHOICE_SELECTED_DEFAULT combine borders, backgrounds, and rings
 * for selected platform choices in both application color themes.
 */
const CHOICE_SELECTED: Record<Platform, string> = {
  anthropic:
    'border-orange-500 bg-orange-50 ring-1 ring-orange-500 dark:border-orange-500/60 dark:bg-orange-500/10 dark:ring-orange-500/60',
  openai:
    'border-emerald-500 bg-emerald-50 ring-1 ring-emerald-500 dark:border-emerald-500/60 dark:bg-emerald-500/10 dark:ring-emerald-500/60',
  antigravity:
    'border-purple-500 bg-purple-50 ring-1 ring-purple-500 dark:border-purple-500/60 dark:bg-purple-500/10 dark:ring-purple-500/60',
  gemini:
    'border-blue-500 bg-blue-50 ring-1 ring-blue-500 dark:border-blue-500/60 dark:bg-blue-500/10 dark:ring-blue-500/60',
  qoder:
    'border-cyan-500 bg-cyan-50 ring-1 ring-cyan-500 dark:border-cyan-500/60 dark:bg-cyan-500/10 dark:ring-cyan-500/60',
  grok: 'border-zinc-800 bg-zinc-50 ring-1 ring-zinc-800 dark:border-zinc-400/60 dark:bg-zinc-500/10 dark:ring-zinc-400/60',
  jev: 'border-zinc-800 bg-zinc-50 ring-1 ring-zinc-800 dark:border-zinc-400/60 dark:bg-zinc-500/10 dark:ring-zinc-400/60',
  kimi: 'border-pink-500 bg-pink-50 ring-1 ring-pink-500 dark:border-pink-500/60 dark:bg-pink-500/10 dark:ring-pink-500/60',
  zhipu:
    'border-indigo-500 bg-indigo-50 ring-1 ring-indigo-500 dark:border-indigo-500/60 dark:bg-indigo-500/10 dark:ring-indigo-500/60',
  deepseek:
    'border-teal-500 bg-teal-50 ring-1 ring-teal-500 dark:border-teal-500/60 dark:bg-teal-500/10 dark:ring-teal-500/60',
}
const CHOICE_SELECTED_DEFAULT =
  'border-primary-500 bg-primary-50 ring-1 ring-primary-500 dark:border-primary-500/60 dark:bg-primary-500/8 dark:ring-primary-500/60'

/**
 * SOLID and SOLID_DEFAULT provide filled icon-tile backgrounds with contrasting text
 * colors for the currently selected platform choice.
 */
const SOLID: Record<Platform, string> = {
  anthropic: 'bg-orange-500 text-white',
  openai: 'bg-emerald-600 text-white',
  antigravity: 'bg-purple-500 text-white',
  gemini: 'bg-blue-500 text-white',
  qoder: 'bg-cyan-600 text-white',
  grok: 'bg-zinc-800 text-white dark:bg-zinc-600',
  jev: 'bg-zinc-800 text-white dark:bg-zinc-600',
  kimi: 'bg-pink-500 text-white',
  zhipu: 'bg-indigo-500 text-white',
  deepseek: 'bg-teal-500 text-white',
}
const SOLID_DEFAULT = 'bg-primary-600 text-white'

/**
 * isPlatform recognizes exact identifiers with dedicated platform labels and palettes.
 */
function isPlatform(p: string): p is Platform {
  return (
    p === 'anthropic' ||
    p === 'openai' ||
    p === 'antigravity' ||
    p === 'gemini' ||
    p === 'qoder' ||
    p === 'grok' ||
    p === 'kimi' ||
    p === 'zhipu' ||
    p === 'deepseek' ||
    p === 'jev'
  )
}

/**
 * platformBadgeLightClass returns the shared soft badge classes for a recognized
 * platform. Unrecognized or empty identifiers receive the neutral slate classes defined
 * for that display purpose.
 */
export function platformBadgeLightClass(p: string): string {
  return isPlatform(p) ? BADGE_LIGHT[p] : BADGE_DEFAULT
}

/**
 * platformTextClass returns the shared text foreground classes for a recognized
 * platform. Unrecognized or empty identifiers receive the primary text classes defined
 * for that display purpose.
 */
export function platformTextClass(p: string): string {
  return isPlatform(p) ? TEXT[p] : TEXT_DEFAULT
}

/**
 * platformIconClass returns the shared small-icon foreground classes for a recognized
 * platform. Unrecognized or empty identifiers receive the primary icon classes defined
 * for that display purpose.
 */
export function platformIconClass(p: string): string {
  return isPlatform(p) ? ICON[p] : ICON_DEFAULT
}

/**
 * platformLabel returns the display spelling of a known platform identifier. Unknown
 * nonempty strings pass through unchanged, and empty input uses API as the generic
 * platform label.
 */
export function platformLabel(p: string): string {
  switch (p) {
    case 'anthropic':
      return 'Anthropic'
    case 'openai':
      return 'OpenAI'
    case 'antigravity':
      return 'Antigravity'
    case 'gemini':
      return 'Gemini'
    case 'qoder':
      return 'Qoder'
    case 'grok':
      return 'Grok'
    case 'kimi':
      return 'Kimi'
    case 'zhipu':
      return 'Zhipu GLM'
    case 'deepseek':
      return 'DeepSeek'
    case 'jev':
      return 'Jev'
    default:
      return p || 'API'
  }
}

/**
 * usagePlatformLabel uses Claude for the anthropic platform on usage surfaces. All
 * other values delegate to platformLabel, retaining its known brand spelling and custom
 * or empty-value behavior.
 */
export function usagePlatformLabel(p: string): string {
  return p === 'anthropic' ? 'Claude' : platformLabel(p)
}

/**
 * platformChoiceSelectedClass returns the shared selected choice classes for a
 * recognized platform. Unrecognized or empty identifiers receive the primary border,
 * background, and ring classes defined for that display purpose.
 */
export function platformChoiceSelectedClass(p: string): string {
  return isPlatform(p) ? CHOICE_SELECTED[p] : CHOICE_SELECTED_DEFAULT
}

/**
 * platformSolidClass returns the shared filled icon tile classes for a recognized
 * platform. Unrecognized or empty identifiers receive the primary background with white
 * text classes defined for that display purpose.
 */
export function platformSolidClass(p: string): string {
  return isPlatform(p) ? SOLID[p] : SOLID_DEFAULT
}
