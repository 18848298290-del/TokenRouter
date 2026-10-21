<template>
  <SettingsTagInput
    :id="id"
    v-model:draft="draft"
    :tags="modelValue ?? []"
    :placeholder="modelValue?.length ? '' : placeholder"
    :remove-label="(tag) => `${t('common.delete')} ${tag}`"
    @remove="remove"
    @keydown="onKeydown"
    @blur="commit"
    @paste="onPaste"
  />
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import SettingsTagInput from '@/components/common/settings/SettingsTagInput.vue'

// ModelAttributeListInput.vue edits optional attribute lists using removable tags and a
// temporary text draft. Commas, newlines, Enter, and blur commit trimmed unique values.
// Removing the last item emits undefined so the owning attribute can inherit its value.

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  // modelValue contains configured attribute tags. Removing the final value emits undefined so
  // the owning attribute can restore inheritance.
  modelValue?: string[]
  id?: string // input element identifier
  placeholder?: string // empty input hint
}>()
// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  'update:modelValue': [string[] | undefined] // replacement parent-owned field value
}>()
// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()
// draft keeps unfinished tag input separate from the parent list until a separator, Enter, or
// blur commits it.
const draft = ref('')

// update publishes the current tag list to the attribute editor. An empty list becomes
// undefined, which lets the owning attribute remove the override and resume inherited values.
function update(values: string[]) {
  emit('update:modelValue', values.length ? values : undefined)
}
// add splits incoming text on supported separators and appends unique nonempty tags. It
// preserves existing tag order and emits an update when at least one additional value remains
// after normalization.
function add(text: string) {
  const current = props.modelValue ?? []
  const added = text
    .split(/[,，\n]/)
    .map((item) => item.trim())
    .filter(Boolean)
  const next = [...new Set([...current, ...added])]
  if (next.length !== current.length) update(next)
}
// commit adds the current text draft and clears the input afterward.
function commit() {
  add(draft.value)
  draft.value = ''
}
// remove publishes the attribute list after excluding every occurrence of the tag.
function remove(tag: string) {
  update((props.modelValue ?? []).filter((value) => value !== tag))
}
// onKeydown commits separators or removes the final tag from an empty input. Composition events
// pass through so IME text can finish before Enter or comma triggers a tag commit.
function onKeydown(event: KeyboardEvent) {
  if (event.isComposing) return
  if (event.key === 'Enter' || event.key === ',' || event.key === '，') {
    event.preventDefault()
    commit()
  } else if (event.key === 'Backspace' && !draft.value && props.modelValue?.length) {
    update(props.modelValue.slice(0, -1))
  }
}
// onPaste commits clipboard text containing separators together with the current draft.
// Single-value clipboard text follows the browser's normal paste path, and handled multi-value
// input clears the draft afterward.
function onPaste(event: ClipboardEvent) {
  const text = event.clipboardData?.getData('text') ?? ''
  if (!/[,，\n]/.test(text)) return
  event.preventDefault()
  add(`${draft.value},${text}`)
  draft.value = ''
}
</script>
