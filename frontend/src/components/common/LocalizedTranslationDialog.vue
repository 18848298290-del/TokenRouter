<template>
  <BaseDialog
    :show="show"
    :title="t('localization.dialogTitle', { field: title })"
    :subtitle="t('localization.dialogHint')"
    width="wide"
    :body-scroll="false"
    flush
    :z-index="Z_INDEX.MODAL_NESTED"
    @close="emit('close')"
  >
    <div
      v-if="draft"
      class="flex min-h-0 flex-1 flex-col overflow-y-auto md:h-[32rem] md:flex-initial md:flex-row md:overflow-hidden"
    >
      <aside
        :aria-label="t('localization.languages')"
        class="flex shrink-0 flex-col gap-4 border-b border-gray-200 bg-gray-50/70 p-3 dark:border-dark-600 dark:bg-dark-950 sm:p-4 md:w-60 md:overflow-y-auto md:border-b-0 md:border-r"
      >
        <div class="space-y-1">
          <p class="px-3 text-xs font-medium text-gray-500 dark:text-dark-400">
            {{ t('localization.original') }}
          </p>
          <button
            type="button"
            class="flex w-full items-center gap-2 rounded-control px-3 py-2 text-left text-sm transition-colors duration-fast"
            :class="itemClass('source')"
            :aria-current="selected === 'source' || undefined"
            @click="selected = 'source'"
          >
            <Icon
              :name="draft.source_locale ? 'document' : 'exclamationTriangle'"
              size="sm"
              :class="draft.source_locale ? '' : 'text-amber-600 dark:text-amber-400'"
              :animate-on-hover="false"
            />
            <span class="min-w-0 flex-1 truncate">{{
              draft.source_locale
                ? languageName(draft.source_locale)
                : t('localization.originalLanguageUnset')
            }}</span>
          </button>
        </div>
        <div class="space-y-1">
          <p class="px-3 text-xs font-medium text-gray-500 dark:text-dark-400">
            {{ t('localization.translations') }}
          </p>
          <button
            v-for="item in targets"
            :key="item.code"
            type="button"
            class="flex w-full items-center gap-2 rounded-control px-3 py-2 text-left text-sm transition-colors duration-fast"
            :class="itemClass(item.code)"
            :aria-current="selected === item.code || undefined"
            @click="selected = item.code"
          >
            <Icon
              :name="STATUS_ICONS[item.status]"
              size="sm"
              :class="STATUS_CLASSES[item.status]"
              :animate-on-hover="false"
            />
            <span class="min-w-0 flex-1 truncate">{{ item.name }}</span>
            <span class="shrink-0 text-xs" :class="STATUS_CLASSES[item.status]">{{
              t(`localization.status.${item.status}`)
            }}</span>
          </button>
        </div>
      </aside>

      <section
        class="flex min-w-0 flex-col gap-4 px-4 py-5 sm:px-6 md:min-h-0 md:flex-1 md:overflow-y-auto"
      >
        <template v-if="selected === 'source'">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h4 class="text-sm font-semibold text-primary-900 dark:text-dark-50">
              {{ t('localization.original') }}
            </h4>
            <Select
              class="w-44"
              :model-value="draft.source_locale || ''"
              :options="languageOptions"
              :placeholder="t('localization.originalLanguage')"
              :aria-label="t('localization.originalLanguage')"
              @update:model-value="changeSourceLocale(String($event))"
            />
          </div>
          <SettingsNotice v-if="conflictLocale" tone="warning">
            <p>
              {{ t('localization.languageConflict', { language: languageName(conflictLocale) }) }}
            </p>
            <div class="flex flex-wrap gap-x-4 gap-y-1">
              <button
                type="button"
                class="font-medium underline underline-offset-2"
                @click="keepSource(conflictLocale)"
              >
                {{ t('localization.keepOriginal') }}
              </button>
              <button
                type="button"
                class="font-medium underline underline-offset-2"
                @click="promote(conflictLocale)"
              >
                {{
                  t('localization.useTranslationAsOriginal', {
                    language: languageName(conflictLocale),
                  })
                }}
              </button>
            </div>
          </SettingsNotice>
          <SettingsNotice v-else-if="!draft.source_locale" tone="warning">{{
            t('localization.unknownOriginal')
          }}</SettingsNotice>
          <p v-else class="text-xs text-gray-500 dark:text-dark-400">
            {{ t('localization.originalHint') }}
          </p>
          <slot
            :value="draft.source"
            :update="updateDraftSource"
            :locale="draft.source_locale"
            :id="`${uid}-source`"
          />
        </template>

        <template v-else-if="current">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div class="flex min-w-0 items-center gap-2">
              <h4 class="text-sm font-semibold text-primary-900 dark:text-dark-50">
                {{ current.name }}
              </h4>
              <span
                class="inline-flex items-center gap-1 text-xs"
                :class="STATUS_CLASSES[current.status]"
              >
                <Icon :name="STATUS_ICONS[current.status]" size="xs" :animate-on-hover="false" />
                {{ t(`localization.status.${current.status}`) }}
              </span>
            </div>
            <div v-if="current.status !== 'missing'" class="flex items-center gap-1">
              <button
                type="button"
                class="btn btn-ghost btn-sm gap-1"
                @click="promote(current.code)"
              >
                <Icon name="arrowUp" size="sm" />
                {{ t('localization.useAsOriginal') }}
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-sm gap-1 hover:text-red-600 dark:hover:text-red-400"
                @click="apply(removeTranslation(draft, current.code))"
              >
                <Icon name="trash" size="sm" />
                {{ t('localization.removeTranslation') }}
              </button>
            </div>
          </div>

          <div
            class="rounded-control border border-primary-900/10 bg-gray-50/70 px-4 py-3 dark:border-dark-600 dark:bg-dark-800/60"
          >
            <p class="mb-2 text-xs font-medium text-gray-500 dark:text-dark-400">
              {{
                t('localization.reference', {
                  language: draft.source_locale
                    ? languageName(draft.source_locale)
                    : t('localization.originalLanguageUnset'),
                })
              }}
            </p>
            <dl class="max-h-40 space-y-2 overflow-y-auto text-sm">
              <div v-for="entry in referenceEntries" :key="entry.key">
                <dt v-if="entry.label" class="text-xs text-gray-500 dark:text-dark-400">
                  {{ entry.label }}
                </dt>
                <dd
                  dir="auto"
                  class="whitespace-pre-wrap break-words"
                  :class="
                    entry.value
                      ? 'text-primary-900 dark:text-dark-100'
                      : 'text-gray-400 dark:text-dark-500'
                  "
                >
                  {{ entry.value || t('localization.emptyValue') }}
                </dd>
              </div>
            </dl>
          </div>

          <div
            v-if="current.status === 'missing'"
            class="flex flex-col items-center gap-3 rounded-control border border-dashed border-primary-900/15 px-4 py-8 text-center dark:border-dark-600"
          >
            <p class="text-sm text-gray-500 dark:text-dark-400">
              {{ t('localization.missingHint', { language: current.name }) }}
            </p>
            <button type="button" class="btn btn-primary btn-sm gap-1" @click="add(current.code)">
              <Icon name="plus" size="sm" />
              {{ t('localization.startTranslation') }}
            </button>
          </div>
          <template v-else>
            <SettingsNotice v-if="current.status === 'stale'" tone="warning">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <span>{{ t('localization.staleNotice') }}</span>
                <button
                  type="button"
                  class="shrink-0 font-medium underline underline-offset-2"
                  @click="apply(markStillValid(draft, current.code))"
                >
                  {{ t('localization.stillValid') }}
                </button>
              </div>
            </SettingsNotice>
            <div ref="editorRef">
              <slot
                :value="draft.translations[current.code].value"
                :update="(value: T) => apply(updateTranslation(draft!, current!.code, value))"
                :locale="current.code"
                :id="`${uid}-${current.code}`"
              />
            </div>
          </template>
        </template>
      </section>
    </div>

    <footer
      class="flex shrink-0 items-center justify-end gap-2 rounded-b-surface border-t border-gray-200 bg-gray-50/70 px-4 py-3 dark:border-dark-600 dark:bg-dark-950 sm:rounded-b-dialog sm:px-6"
    >
      <button type="button" class="btn btn-secondary" @click="emit('close')">
        {{ t('common.cancel') }}
      </button>
      <button type="button" class="btn btn-primary" @click="save">
        {{ t('localization.done') }}
      </button>
    </footer>
  </BaseDialog>
</template>

<script setup lang="ts" generic="T">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import Icon from '@/components/icons/Icon.vue'
import { Z_INDEX } from '@/constants/overlay'
import { availableLocales } from '@/i18n/catalog'
import type { LocalizedUpdate } from '@/i18n/content'
import {
  addTranslation,
  adoptSourceLocale,
  markStillValid,
  promoteToOriginal,
  removeTranslation,
  setSourceLocale,
  translationStatus,
  updateSource,
  updateTranslation,
  withDefaultSource,
  type TranslationStatus,
} from '@/i18n/contentEdit'

/**
 * LocalizedTranslationDialog.vue edits a temporary localized content value with source
 * and translation panels. Opening clones the parent value through JSON serialization
 * so cancellation leaves the parent form untouched. The selected language determines
 * which editor receives the shared slot, and each target carries a status derived from
 * its source revision. Source locale conflicts remain visible until the administrator
 * chooses which text becomes the source.
 *
 * Content transformations delegate to contentEdit, including promotion, removal, and
 * review acknowledgement. The dialog controls when the draft becomes visible to its
 * parent: save emits the current draft, while close only requests dismissal. Adding a
 * translation waits for Vue to render its editor before focusing an input.
 */

/**
 * dialogSequence supplies the identifier suffix used by this setup invocation when
 * constructing source and translation editor IDs.
 */
let dialogSequence = 0

/**
 * props supplies dialog visibility, the parent content copied on opening, and the
 * metadata used to label source references.
 */
const props = defineProps<{
  show: boolean /* dialog visibility flag */
  title: string /* displayed heading text */
  modelValue: LocalizedUpdate<T> /* parent controlled value */

  /**
   * fallbackLocale initializes new source metadata and supplies the language used when
   * editing source content whose language is unknown.
   */
  fallbackLocale?: string

  /**
   * referenceFields expands object source content into labeled rows; omission displays
   * the source as a single string reference.
   */
  referenceFields?: {
    key: string // source property name
    label: string /* source reference label */
  }[]
}>()
/**
 * emit reports close requests or delivers the current draft on save; the parent
 * decides how to persist or discard the dialog value.
 */
const emit = defineEmits<{
  close: [] // dialog dismissal request
  save: [value: LocalizedUpdate<T>] /* completed localized draft */
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
 * uid connects the slot editors to source and target language identifiers within this
 * dialog instance.
 */
// eslint-disable-next-line no-useless-assignment -- The generated identifier uses this counter before rendering input labels.
const uid = `localized-${++dialogSequence}`
/**
 * draft owns the JSON cloned content for the current opening; parent model changes are
 * applied when the dialog opens again.
 */
const draft = ref<LocalizedUpdate<T>>()
/**
 * selected names the source panel or a target locale and determines which editor the
 * dialog slot receives.
 */
const selected = ref('source')
/**
 * conflictLocale holds the proposed source language while an existing translation
 * requires the administrator to choose source ownership.
 */
const conflictLocale = ref('')
/**
 * editorRef locates the rendered translation editor so add can focus its first text
 * input after Vue updates.
 */
const editorRef = ref<HTMLElement>()
/**
 * languageOptions adapts the supported locale catalog to the value and label records
 * consumed by the source language selector.
 */
const languageOptions = availableLocales.map((item) => ({ value: item.code, label: item.name }))

/**
 * STATUS_CLASSES gives translated, stale, and missing entries distinct text colors in
 * the language list and editor heading.
 */
const STATUS_CLASSES: Record<TranslationStatus, string> = {
  translated: 'text-emerald-600 dark:text-emerald-400',
  stale: 'text-amber-600 dark:text-amber-400',
  missing: 'text-gray-400 dark:text-dark-500',
}
/**
 * STATUS_ICONS pairs the same translation states with check, warning, and empty circle
 * indicators for visible status recognition.
 */
const STATUS_ICONS = {
  translated: 'checkCircle',
  stale: 'exclamationTriangle',
  missing: 'circle',
} as const

/**
 * targets includes every supported language except the current source language,
 * attaching revision status even when a translation is missing.
 */
const targets = computed(() => {
  const content = draft.value
  if (!content) return []
  return availableLocales
    .filter((item) => item.code !== content.source_locale)
    .map((item) => ({
      code: item.code,
      name: item.name,
      status: translationStatus(content, item.code),
    }))
})
/**
 * current finds the selected target language entry and stays undefined while the
 * source panel or an unknown language is selected.
 */
const current = computed(() => targets.value.find((item) => item.code === selected.value))

/**
 * referenceEntries expands configured object fields into labeled source references or
 * exposes scalar source content as one text entry.
 */
const referenceEntries = computed(() => {
  const source = draft.value?.source as unknown
  if (props.referenceFields && source && typeof source === 'object') {
    const values = source as Record<string, unknown>
    return props.referenceFields.map((field) => ({
      key: field.key,
      label: field.label,
      value: String(values[field.key] ?? ''),
    }))
  }
  return [{ key: 'value', label: '', value: String(source ?? '') }]
})

/**
 * Opening copies the current parent content into a temporary JSON draft, so cancelling
 * discards edits and reopening reads the latest form value.
 */
watch(
  () => props.show,
  (open) => {
    if (!open) return
    const content = withDefaultSource(
      JSON.parse(JSON.stringify(props.modelValue)) as LocalizedUpdate<T>,
      props.fallbackLocale,
    )
    draft.value = content
    conflictLocale.value = ''
    selected.value = initialSelection(content)
  },
  { immediate: true },
)

/**
 * initialSelection chooses the first panel needing translation attention when opening.
 * An unknown source language selects the source; otherwise stale translations precede
 * existing translations, then the first supported target, with source as the empty
 * catalog result.
 */
function initialSelection(content: LocalizedUpdate<T>): string {
  if (!content.source_locale) return 'source'
  const codes = availableLocales
    .map((item) => item.code)
    .filter((code) => code !== content.source_locale)
  return (
    codes.find((code) => translationStatus(content, code) === 'stale') ||
    codes.find((code) => translationStatus(content, code) === 'translated') ||
    codes[0] ||
    'source'
  )
}

/**
 * languageName returns the catalog language name or the supplied code. Unknown codes
 * stay visible in source reference labels, allowing existing content metadata to
 * remain identifiable even when absent from the catalog.
 */
function languageName(code: string): string {
  return availableLocales.find((item) => item.code === code)?.name || code
}
/**
 * itemClass returns the selected or ordinary language button color classes.
 */
function itemClass(key: string): string {
  return selected.value === key
    ? 'bg-primary-100 text-primary-700 dark:bg-primary-500/8 dark:text-primary-500'
    : 'text-gray-700 hover:bg-gray-100 dark:text-dark-100 dark:hover:bg-dark-800'
}
/**
 * apply replaces the dialog draft with the supplied localized content.
 */
function apply(next: LocalizedUpdate<T>): void {
  draft.value = next
}
/**
 * updateDraftSource replaces the source through the localized content editing helper.
 * Callers invoke it while an opened draft exists, and fallbackLocale supplies the
 * language used when the source metadata is unknown.
 */
function updateDraftSource(value: T): void {
  apply(updateSource(draft.value!, value, props.fallbackLocale))
}

/**
 * changeSourceLocale proposes a source language and records any translation conflict.
 * Empty codes leave the draft untouched; other codes use setSourceLocale and retain
 * its returned content while the administrator resolves a conflict.
 */
function changeSourceLocale(code: string): void {
  if (!code) return
  const result = setSourceLocale(draft.value!, code)
  conflictLocale.value = result.conflict ? code : ''
  apply(result.content)
}
/**
 * keepSource adopts the selected source locale while keeping the current source text.
 * It delegates translation handling to adoptSourceLocale and clears the conflict
 * prompt after replacing the opened draft.
 */
function keepSource(code: string): void {
  apply(adoptSourceLocale(draft.value!, code))
  conflictLocale.value = ''
}
/**
 * promote makes the requested translation the source through promoteToOriginal. It
 * requires an opened draft, clears the conflict prompt, and returns selection to the
 * source panel after applying the transformation.
 */
function promote(code: string): void {
  apply(promoteToOriginal(draft.value!, code))
  conflictLocale.value = ''
  selected.value = 'source'
}

/**
 * add creates a translation from source content in the opened draft. After Vue renders
 * the changed editor, it focuses the first nonfile input or textarea if such an
 * element is available.
 */
async function add(code: string): Promise<void> {
  apply(addTranslation(draft.value!, code))
  /**
   * Wait for the added translation editor to mount before querying its input,
   * otherwise focus would target an absent or previous editor.
   */
  await nextTick()
  editorRef.value?.querySelector<HTMLElement>('input:not([type="file"]), textarea')?.focus()
}
/**
 * save emits the current draft when the dialog has one. The parent receives the draft
 * object and owns dismissal or persistence; invoking save before a draft exists has no
 * effect.
 */
function save(): void {
  if (draft.value) emit('save', draft.value)
}
</script>
