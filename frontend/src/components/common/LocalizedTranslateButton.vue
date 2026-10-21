<template>
  <button
    type="button"
    class="inline-flex shrink-0 items-center gap-1 rounded-compact text-xs transition-colors duration-fast focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/50"
    :class="
      summary.stale || summary.sourceLocaleMissing
        ? 'text-amber-600 hover:text-amber-700 dark:text-amber-400 dark:hover:text-amber-300'
        : 'text-gray-500 hover:text-primary-600 dark:text-dark-400 dark:hover:text-primary-500'
    "
    :aria-label="t('localization.dialogTitle', { field: title })"
    @click="open = true"
  >
    <Icon name="globe" size="xs" />
    <template v-if="summary.sourceLocaleMissing">
      <span>{{ t('localization.chooseOriginalLanguage') }}</span>
      <Icon name="exclamationTriangle" size="xs" :animate-on-hover="false" />
    </template>
    <template v-else>
      <span>{{ text }}</span>
      <Icon v-if="summary.stale" name="exclamationTriangle" size="xs" :animate-on-hover="false" />
      <Icon v-else-if="summary.locales.length" name="check" size="xs" :animate-on-hover="false" />
      <span v-if="summary.stale">{{ t('localization.needsUpdate') }}</span>
    </template>
  </button>
  <LocalizedTranslationDialog
    :show="open"
    :title="title"
    :model-value="modelValue"
    :fallback-locale="fallbackLocale"
    :reference-fields="referenceFields"
    @close="open = false"
    @save="save"
  >
    <template #default="scope">
      <slot v-bind="scope" />
    </template>
  </LocalizedTranslationDialog>
</template>

<script setup lang="ts" generic="T">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import LocalizedTranslationDialog from '@/components/common/LocalizedTranslationDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { availableLocales } from '@/i18n/catalog'
import type { LocalizedUpdate } from '@/i18n/content'
import { translationSummary } from '@/i18n/contentEdit'

/**
 * LocalizedTranslateButton.vue summarizes available translations and opens their
 * editing dialog from a field label. Missing source language and stale translations
 * receive warning styling, while saving publishes the dialog value to the parent and
 * closes the local dialog state.
 */

/**
 * props supplies the localized value summarized by the trigger and the heading and
 * source reference configuration forwarded to its dialog.
 */
const props = defineProps<{
  modelValue: LocalizedUpdate<T> /* parent controlled value */
  title: string /* displayed heading text */
  fallbackLocale?: string /* source update language */

  /**
   * referenceFields forwards object field names and labels to the dialog so
   * translators can read each source property separately.
   */
  referenceFields?: {
    key: string // source property name
    label: string /* source reference label */
  }[]
}>()
/**
 * emit forwards the completed dialog value through the parent model binding before the
 * local dialog closes.
 */
const emit = defineEmits<{
  'update:modelValue': [value: LocalizedUpdate<T>] /* replacement parent controlled value */
}>()
/**
 * The default slot receives the current value, language, input identifier, and updater
 * so custom controls can edit either source or translation content.
 */
defineSlots<{
  /**
   * default renders the supplied editor value with its language and update callback.
   */
  default(props: {
    value: T /* current editor value */
    update: (value: T) => void /* editor update callback */
    locale: string | null /* editor language code */
    id: string /* input association identifier */
  }): unknown
}>()

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()
/**
 * open owns the local dialog visibility and is cleared after the dialog publishes a
 * saved content value.
 */
const open = ref(false)
/**
 * summary derives translation language coverage, staleness, and missing source
 * language from the parent supplied localized content.
 */
const summary = computed(() => translationSummary(props.modelValue))

/**
 * text names up to two translated languages, uses a count for larger sets, and offers
 * translation creation for an empty set.
 */
const text = computed(() => {
  const { locales } = summary.value
  if (!locales.length) return t('localization.addTranslation')
  /**
   * Limit explicit language names to two in compact field headers.
   */
  if (locales.length > 2) return t('localization.languageCount', { n: locales.length })
  return locales
    .map((code) => availableLocales.find((item) => item.code === code)?.name || code)
    .join(' · ')
})

/**
 * save publishes the dialog content through the controlled model update. The update is
 * emitted before closing the dialog so the parent receives the completed draft as the
 * editor returns to its summary.
 */
function save(value: LocalizedUpdate<T>): void {
  emit('update:modelValue', value)
  open.value = false
}
</script>
