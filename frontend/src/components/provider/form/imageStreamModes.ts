import type { ProtocolID } from '@/api/protocols.types'

// imageStreamModes.ts supplies the provider forms' Images capability drafts.
// Generation and editing retain independent values even while their protocol is
// disabled. Missing saved values use both modes, matching the backend default
// and keeping older provider records editable without a data migration.

// ImageStreamMode declares which request modes one native Images endpoint accepts.
export type ImageStreamMode = 'non_streaming' | 'streaming' | 'both'

// ImageStreamModes stores separate capabilities using their persisted credential keys.
export interface ImageStreamModes {
  images_generations_mode: ImageStreamMode // Generation endpoint request modes.
  images_edits_mode: ImageStreamMode // Editing endpoint request modes.
}

// IMAGE_MODE_FIELDS pairs each independent bulk field with its existing protocol label.
export const IMAGE_MODE_FIELDS = [
  {
    key: 'images_generations_mode',
    protocol: 'openai_images_generations',
    label: 'generationMode',
  },
  { key: 'images_edits_mode', protocol: 'openai_images_edits', label: 'editMode' },
] as const

// readImageStreamModes creates an independent draft using the server's defaults.
// Unknown stored values display both so an administrator can replace invalid
// historical data; the backend validates every submitted capability value.
export function readImageStreamModes(credentials?: Record<string, unknown>): ImageStreamModes {
  return {
    images_generations_mode: readMode(credentials?.images_generations_mode),
    images_edits_mode: readMode(credentials?.images_edits_mode),
  }
}

// readMode accepts the three persisted enum values and defaults absent values.
function readMode(value: unknown): ImageStreamMode {
  return value === 'streaming' || value === 'non_streaming' ? value : 'both'
}

// imageModeField finds the credential field belonging to an Images protocol.
export function imageModeField(id: ProtocolID): keyof ImageStreamModes | undefined {
  return IMAGE_MODE_FIELDS.find((field) => field.protocol === id)?.key
}

// imageStreamModeOptions builds localized choices for provider image mode controls.
export function imageStreamModeOptions(t: (key: string) => string) {
  return (['both', 'non_streaming', 'streaming'] as const).map((value) => ({
    value,
    label: t(`admin.protocols.imageModes.${value}`),
  }))
}
