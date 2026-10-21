<template>
  <span
    class="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-compact"
    :class="toneClass"
    :title="label"
    :data-state="state"
    role="img"
    :aria-label="label"
  >
    <Icon :name="iconName" size="xs" :stroke-width="2.5" />
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { IconName } from '@/components/icons/registry'
import type { ModelModality } from '@/utils/modelCapabilities'

/**
 * ModalityIcon.vue renders a named model input or output modality with an accessible
 * label. Supported choices use category colors; unsupported and unknown choices use
 * different neutral appearances, while inverse tone keeps supported icons readable on
 * dark tooltip backgrounds.
 */

/**
 * props combines the represented modality, support evidence, and background tone
 * before selecting the icon appearance and accessible name.
 */
const props = withDefaults(
  defineProps<{
    modality: ModelModality /* represented modality category */

    /**
     * tone selects inverse colors for permanently dark tooltip backgrounds; auto
     * follows the normal light and dark theme palettes.
     */
    tone?: 'auto' | 'inverse'

    /**
     * state distinguishes missing support evidence from explicit lack of support
     * through separate neutral appearances and accessible state labels.
     */
    state?: 'supported' | 'unsupported' | 'unknown'
  }>(),
  {
    tone: 'auto',
    state: 'supported',
  },
)

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()

/**
 * ICONS maps each recognized modality to the shared glyph used in both input and
 * output summaries.
 */
const ICONS: Record<ModelModality, IconName> = {
  text: 'modalityText',
  image: 'modalityImage',
  audio: 'modalityAudio',
  video: 'modalityVideo',
  pdf: 'modalityPdf',
}

/**
 * AUTO_CLASSES assigns modality category colors in both themes for supported icons on
 * ordinary content backgrounds.
 */
const AUTO_CLASSES: Record<ModelModality, string> = {
  text: 'bg-blue-500/10 text-blue-600 dark:bg-blue-400/10 dark:text-blue-300',
  image: 'bg-green-500/10 text-green-600 dark:bg-green-400/10 dark:text-green-300',
  audio: 'bg-amber-500/10 text-amber-600 dark:bg-amber-400/10 dark:text-amber-300',
  video: 'bg-rose-500/10 text-rose-600 dark:bg-rose-400/10 dark:text-rose-300',
  pdf: 'bg-violet-500/10 text-violet-600 dark:bg-violet-400/10 dark:text-violet-300',
}

/**
 * INVERSE_CLASSES keeps supported modality colors legible on dark tooltip surfaces
 * even when the application theme is light.
 */
const INVERSE_CLASSES: Record<ModelModality, string> = {
  text: 'bg-blue-400/15 text-blue-300',
  image: 'bg-green-400/15 text-green-300',
  audio: 'bg-amber-400/15 text-amber-300',
  video: 'bg-rose-400/15 text-rose-300',
  pdf: 'bg-violet-400/15 text-violet-300',
}

/**
 * iconName reads the glyph for the supplied modality from the complete modality icon
 * map.
 */
const iconName = computed(() => ICONS[props.modality])
/**
 * toneClass combines support evidence and surface tone, reserving dashed borders for
 * unknown choices and reduced opacity for unsupported choices.
 */
const toneClass = computed(() =>
  props.state === 'supported'
    ? (props.tone === 'inverse' ? INVERSE_CLASSES : AUTO_CLASSES)[props.modality]
    : [
        'bg-gray-500/10 text-gray-500 dark:text-dark-400',
        props.state === 'unsupported' ? 'opacity-40' : 'border border-dashed border-gray-500',
      ],
)
/**
 * label names the modality and appends its translated evidence state when that
 * modality is not supported.
 */
const label = computed(() => {
  const name = t(`admin.modelAttributes.modalities.${props.modality}`)
  return props.state === 'supported'
    ? name
    : `${name} · ${t(`admin.modelAttributes.${props.state}`)}`
})
</script>
