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

// 回车、逗号和失焦时提交草稿。空列表按继承处理，删掉最后一项时发出 undefined。
const props = defineProps<{ modelValue?: string[]; id?: string; placeholder?: string }>()
const emit = defineEmits<{ 'update:modelValue': [string[] | undefined] }>()
const { t } = useI18n()
const draft = ref('')

function update(values: string[]) {
  emit('update:modelValue', values.length ? values : undefined)
}
function add(text: string) {
  const current = props.modelValue ?? []
  const added = text.split(/[,，\n]/).map(item => item.trim()).filter(Boolean)
  const next = [...new Set([...current, ...added])]
  if (next.length !== current.length) update(next)
}
function commit() {
  add(draft.value)
  draft.value = ''
}
function remove(tag: string) {
  update((props.modelValue ?? []).filter(value => value !== tag))
}
function onKeydown(event: KeyboardEvent) {
  if (event.isComposing) return
  if (event.key === 'Enter' || event.key === ',' || event.key === '，') {
    event.preventDefault()
    commit()
  } else if (event.key === 'Backspace' && !draft.value && props.modelValue?.length) {
    update(props.modelValue.slice(0, -1))
  }
}
function onPaste(event: ClipboardEvent) {
  const text = event.clipboardData?.getData('text') ?? ''
  if (!/[,，\n]/.test(text)) return
  event.preventDefault()
  add(`${draft.value},${text}`)
  draft.value = ''
}
</script>
